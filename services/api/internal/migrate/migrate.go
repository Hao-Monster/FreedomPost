// Package migrate provides a simple SQL migration runner.
// It applies *.sql files from a directory in lexicographic order,
// tracking applied migrations in a schema_migrations table.
package migrate

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

const createMigrationsTable = `
CREATE TABLE IF NOT EXISTS schema_migrations (
	filename   TEXT PRIMARY KEY,
	applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);`

// Run connects to the database and applies any pending migration files
// found in migrationsDir (*.sql, applied in lexicographic order).
// It is safe to call repeatedly; already-applied migrations are skipped.
func Run(ctx context.Context, databaseURL, migrationsDir string) error {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return fmt.Errorf("migrate: connect: %w", err)
	}
	defer pool.Close()

	// Ensure the tracking table exists.
	if _, err := pool.Exec(ctx, createMigrationsTable); err != nil {
		slog.Info("migrate: create schema_migrations note", "info", err)
	}

	// In case schema_migrations was created with legacy columns (e.g. 'version' instead of 'filename'):
	_, _ = pool.Exec(ctx, `
		ALTER TABLE schema_migrations ADD COLUMN IF NOT EXISTS filename TEXT;
		ALTER TABLE schema_migrations ADD COLUMN IF NOT EXISTS applied_at TIMESTAMPTZ DEFAULT NOW();
	`)

	// Track whether 'version' column exists in case it has NOT NULL constraints.
	hasVersionCol := false
	appliedSet := make(map[string]bool)

	// Read existing applied records dynamically from schema_migrations
	rows, err := pool.Query(ctx, "SELECT * FROM schema_migrations")
	if err == nil {
		fieldDescs := rows.FieldDescriptions()
		for _, f := range fieldDescs {
			if string(f.Name) == "version" {
				hasVersionCol = true
			}
		}
		for rows.Next() {
			vals, valErr := rows.Values()
			if valErr == nil {
				for _, v := range vals {
					if v != nil {
						strVal := fmt.Sprintf("%v", v)
						appliedSet[strVal] = true
					}
				}
			}
		}
		rows.Close()
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

		if hasVersionCol {
			_, err = tx.Exec(ctx, `
				INSERT INTO schema_migrations (version, filename) VALUES ($1, $1)
				ON CONFLICT DO NOTHING
			`, name)
		} else {
			_, err = tx.Exec(ctx, `
				INSERT INTO schema_migrations (filename) VALUES ($1)
				ON CONFLICT DO NOTHING
			`, name)
		}
		if err != nil {
			slog.Warn("migrate: record insert warning", "file", name, "error", err)
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
