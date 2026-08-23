// Package migrate provides a simple SQL migration runner.
// It applies *.sql files from a directory in lexicographic order,
// tracking applied migrations in a go_schema_migrations table.
package migrate

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const createMigrationsTable = `
CREATE TABLE IF NOT EXISTS go_schema_migrations (
	filename   TEXT PRIMARY KEY,
	applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);`

// Run connects to the database and applies any pending migration files
// found in migrationsDir (*.sql, applied in lexicographic order).
// It is safe to call repeatedly; already-applied migrations are skipped.
func Run(ctx context.Context, databaseURL, migrationsDir string) error {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return fmt.Errorf("migrate: parse database URL: %w", err)
	}

	var pool *pgxpool.Pool
	var lastErr error
	for attempt := 1; attempt <= 30; attempt++ {
		pool, err = pgxpool.NewWithConfig(ctx, cfg)
		if err == nil {
			if pingErr := pool.Ping(ctx); pingErr == nil {
				lastErr = nil
				break
			} else {
				lastErr = pingErr
				pool.Close()
			}
		} else {
			lastErr = err
		}
		slog.Info("migrate: waiting for database readiness...", "attempt", attempt, "error", lastErr)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(1 * time.Second):
		}
	}
	if lastErr != nil {
		return fmt.Errorf("migrate: connect after 30 attempts: %w", lastErr)
	}
	defer pool.Close()

	// Ensure the dedicated tracking table exists.
	if _, err := pool.Exec(ctx, createMigrationsTable); err != nil {
		return fmt.Errorf("migrate: create go_schema_migrations: %w", err)
	}

	appliedSet := make(map[string]bool)

	// 1. Read already-applied migrations from go_schema_migrations
	rows, err := pool.Query(ctx, "SELECT filename FROM go_schema_migrations")
	if err == nil {
		for rows.Next() {
			var fn string
			if err := rows.Scan(&fn); err == nil {
				appliedSet[fn] = true
			}
		}
		rows.Close()
	}

	// 2. Read any existing records from legacy schema_migrations table (if it exists)
	legacyRows, err := pool.Query(ctx, "SELECT * FROM schema_migrations")
	if err == nil {
		for legacyRows.Next() {
			vals, valErr := legacyRows.Values()
			if valErr == nil {
				for _, v := range vals {
					if v != nil {
						strVal := fmt.Sprintf("%v", v)
						appliedSet[strVal] = true
					}
				}
			}
		}
		legacyRows.Close()
	}

	// Read all *.sql files.
	entries, err := os.ReadDir(migrationsDir)
	if err != nil {
		return fmt.Errorf("migrate: read dir %q: %w", migrationsDir, err)
	}
	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)

	for _, name := range files {
		// Check if already applied via filename, prefix number (e.g. "0001"), or unpadded number (e.g. "1")
		prefix := strings.Split(name, "_")[0]
		unpaddedPrefix := strings.TrimLeft(prefix, "0")
		if unpaddedPrefix == "" {
			unpaddedPrefix = "0"
		}

		if appliedSet[name] || appliedSet[prefix] || appliedSet[unpaddedPrefix] {
			slog.Info("migration already applied, skipping", "file", name)
			// Ensure it's recorded in go_schema_migrations as well
			_, _ = pool.Exec(ctx, `
				INSERT INTO go_schema_migrations (filename) VALUES ($1)
				ON CONFLICT (filename) DO NOTHING
			`, name)
			continue
		}

		// Read and execute the migration.
		path := filepath.Join(migrationsDir, name)
		sql, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("migrate: read %s: %w", name, err)
		}

		tx, err := pool.Begin(ctx)
		if err != nil {
			return fmt.Errorf("migrate: begin tx for %s: %w", name, err)
		}

		if _, err := tx.Exec(ctx, string(sql)); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("migrate: apply %s: %w", name, err)
		}

		if _, err := tx.Exec(ctx, `
			INSERT INTO go_schema_migrations (filename) VALUES ($1)
			ON CONFLICT (filename) DO NOTHING
		`, name); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("migrate: record %s: %w", name, err)
		}

		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("migrate: commit %s: %w", name, err)
		}

		appliedSet[name] = true
		slog.Info("migration applied", "file", name)
	}

	slog.Info("migrations complete", "total_files", len(files))
	return nil
}
