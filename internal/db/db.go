// Package db provides this repo's Postgres access layer: a minimal, hand-rolled
// embedded-SQL migration runner, mirroring go-tari-explorer's internal/db/db.go pattern
// (embedded *.up.sql files via //go:embed, a schema_migrations tracking table, a
// DB.Migrate method) rather than pulling in a full migration framework like
// golang-migrate.
//
// Schema is intentionally minimal-viable for v1 (see migrations/0001_init.up.sql) and
// is expected to grow incrementally as internal/explorer, internal/vnhealth,
// internal/burnclaim, and internal/registry gain real functionality - don't over-build
// this layer ahead of what's actually needed yet.
package db

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.up.sql
var migrationFS embed.FS

// DB wraps a pgx connection pool.
type DB struct {
	Pool *pgxpool.Pool
}

// Connect opens a pgx pool against dsn and returns a ready-to-use DB. Callers are
// responsible for calling Close when done. dsn is a standard Postgres connection
// string, e.g. "postgres://user:pass@localhost:5432/tari_ootle_explorer?sslmode=disable".
func Connect(ctx context.Context, dsn string) (*DB, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("db: connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("db: ping: %w", err)
	}
	return &DB{Pool: pool}, nil
}

// Close releases the underlying connection pool.
func (d *DB) Close() {
	d.Pool.Close()
}

// Migrate applies every embedded *.up.sql migration that hasn't already been recorded
// in schema_migrations, in filename order, each inside its own transaction. See the
// package doc comment for why this is a small hand-rolled runner rather than
// golang-migrate: v1 only has one migration file, and migrations/*.up.sql is already
// named in golang-migrate's <version>_<name>.up.sql convention, so swapping to it later
// (if this schema ever grows enough to earn golang-migrate's down-migration/dirty-state
// tooling) is a drop-in change, not a rewrite.
func (d *DB) Migrate(ctx context.Context) error {
	if _, err := d.Pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version TEXT PRIMARY KEY, applied_at TIMESTAMPTZ NOT NULL DEFAULT now())`); err != nil {
		return fmt.Errorf("db: migrate: create schema_migrations: %w", err)
	}

	entries, err := fs.Glob(migrationFS, "migrations/*.up.sql")
	if err != nil {
		return fmt.Errorf("db: migrate: glob: %w", err)
	}
	sort.Strings(entries)

	for _, entry := range entries {
		version := strings.TrimSuffix(strings.TrimPrefix(entry, "migrations/"), ".up.sql")

		var alreadyApplied bool
		err := d.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version = $1)`, version).Scan(&alreadyApplied)
		if err != nil {
			return fmt.Errorf("db: migrate: check %s: %w", version, err)
		}
		if alreadyApplied {
			continue
		}

		sqlBytes, err := migrationFS.ReadFile(entry)
		if err != nil {
			return fmt.Errorf("db: migrate: read %s: %w", entry, err)
		}

		tx, err := d.Pool.Begin(ctx)
		if err != nil {
			return fmt.Errorf("db: migrate: begin %s: %w", version, err)
		}
		if _, err := tx.Exec(ctx, string(sqlBytes)); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("db: migrate: apply %s: %w", version, err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, version); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("db: migrate: record %s: %w", version, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("db: migrate: commit %s: %w", version, err)
		}
	}
	return nil
}
