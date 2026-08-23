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
		return fmt.Errorf("migrate: create schema_migrations: %w", err)
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
		// Check if already applied.
		var count int
		if err := pool.QueryRow(ctx,
			"SELECT COUNT(*) FROM schema_migrations WHERE filename = $1", name,
		).Scan(&count); err != nil {
			return fmt.Errorf("migrate: check %s: %w", name, err)
		}
		if count > 0 {
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
		if _, err := tx.Exec(ctx,
			"INSERT INTO schema_migrations (filename) VALUES ($1)", name,
		); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("migrate: record %s: %w", name, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("migrate: commit %s: %w", name, err)
		}

		slog.Info("migration applied", "file", name)
	}

	slog.Info("migrations complete", "applied", len(files))
	return nil
}
