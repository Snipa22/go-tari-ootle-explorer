package db

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	pgquery "github.com/pganalyze/pg_query_go/v5"
)

// TestMigrationsParseAsValidPostgresSQL is the "dry parse" validation this package's
// migration files always get, regardless of whether a live Postgres is reachable: each
// embedded *.up.sql / *.down.sql file is run through pg_query_go, which vendors real
// Postgres's own libpg_query C parser (github.com/pganalyze/pg_query_go) - so this is
// genuine Postgres SQL syntax validation, not a hand-rolled approximation, even though
// it never opens a network connection. This is NOT a substitute for TestMigrate_
// AppliesCleanlyAgainstRealPostgres below (a syntactically valid statement can still
// fail against a real server - wrong types, missing extensions, etc.) - see that test's
// doc comment for the live-apply coverage this one deliberately doesn't provide.
func TestMigrationsParseAsValidPostgresSQL(t *testing.T) {
	entries, err := os.ReadDir("migrations")
	if err != nil {
		t.Fatalf("read migrations dir: %v", err)
	}

	var sqlFiles []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			sqlFiles = append(sqlFiles, e.Name())
		}
	}
	if len(sqlFiles) == 0 {
		t.Fatal("no *.sql migration files found under migrations/ - expected at least 0001_init.up.sql/.down.sql")
	}

	for _, name := range sqlFiles {
		name := name
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("migrations", name))
			if err != nil {
				t.Fatalf("read %s: %v", name, err)
			}
			if strings.TrimSpace(string(raw)) == "" {
				t.Fatalf("%s is empty", name)
			}
			if _, err := pgquery.Parse(string(raw)); err != nil {
				t.Fatalf("%s: not valid Postgres SQL: %v", name, err)
			}
		})
	}
}

// TestMigrate_AppliesCleanlyAgainstRealPostgres is the real live-apply integration test
// the v1 scope brief asks for: Connect + Migrate against an actual Postgres server,
// confirming both migration files apply without error and are correctly recorded in
// schema_migrations (including the "already applied" no-op path on a second run).
//
// This sandbox has no reachable Postgres server (no local postgres/pg_ctl binary
// installed, no docker available to run testcontainers-go, and nothing vendored
// elsewhere in this ecosystem's go.sum providing one - see the dispatch report for
// exactly what was checked) and no live one was available to test against, so this
// test SKIPS rather than either failing the whole suite or - worse - fabricating a
// pass. It activates automatically the moment a real DSN is available via
// TEST_POSTGRES_DSN (CI/dev-machine Postgres) or the package's own DefaultPostgresDSN
// default is actually reachable, so this is real coverage, gated behind an environment
// precondition, not a permanently-skipped test.
func TestMigrate_AppliesCleanlyAgainstRealPostgres(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		dsn = "postgres://tari_ootle_explorer:tari_ootle_explorer@localhost:5432/tari_ootle_explorer?sslmode=disable"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	database, err := Connect(ctx, dsn)
	if err != nil {
		t.Skipf("SKIP: no reachable Postgres to test a real migration apply against (tried %s): %v", dsn, err)
	}
	defer database.Close()

	// Start from a clean slate so this test is repeatable against a persistent dev
	// Postgres instance, not just a throwaway one.
	if _, err := database.Pool.Exec(ctx, `DROP TABLE IF EXISTS schema_migrations, ootle_blocks, validators, burn_claims, template_registry CASCADE`); err != nil {
		t.Fatalf("cleanup before test: %v", err)
	}

	if err := database.Migrate(ctx); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}

	wantTables := []string{"ootle_blocks", "validators", "burn_claims", "template_registry"}
	for _, table := range wantTables {
		var exists bool
		err := database.Pool.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = $1)`, table,
		).Scan(&exists)
		if err != nil {
			t.Fatalf("check table %s exists: %v", table, err)
		}
		if !exists {
			t.Errorf("expected table %q to exist after Migrate(), it does not", table)
		}
	}

	var version string
	if err := database.Pool.QueryRow(ctx, `SELECT version FROM schema_migrations WHERE version = '0001_init'`).Scan(&version); err != nil {
		t.Fatalf("expected schema_migrations to record 0001_init: %v", err)
	}

	// Running Migrate again must be a clean no-op (the "already applied" skip path),
	// not a duplicate-table error.
	if err := database.Migrate(ctx); err != nil {
		t.Fatalf("second Migrate() call error = %v, want nil (idempotent no-op)", err)
	}
}
