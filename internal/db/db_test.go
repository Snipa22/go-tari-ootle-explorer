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

	for _, version := range []string{"0001_init", "0002_template_registry_correction"} {
		var got string
		if err := database.Pool.QueryRow(ctx, `SELECT version FROM schema_migrations WHERE version = $1`, version).Scan(&got); err != nil {
			t.Fatalf("expected schema_migrations to record %s: %v", version, err)
		}
	}

	// Running Migrate again must be a clean no-op (the "already applied" skip path),
	// not a duplicate-table error.
	if err := database.Migrate(ctx); err != nil {
		t.Fatalf("second Migrate() call error = %v, want nil (idempotent no-op)", err)
	}
}

// TestUpsertValidatorHealth_MergesWithoutClobberingOtherSourcesFields is the real,
// live-Postgres proof DISPATCH_BRIEF.md (step 4) asks for: internal/explorer's
// UpsertValidator (source A) and internal/vnhealth's UpsertValidatorHealth (source B)
// write the SAME validators row keyed on public_key, from two independent upstream
// sources that don't necessarily have the same fields available on any given poll -
// this proves B upserting a PARTIAL row (missing shard_group_start/end_inclusive,
// simulating a vnhealth poll where get_epoch_manager_stats's committee_info came back
// nil) does not null out - or otherwise clobber - the values A already wrote for
// those columns, while still applying B's own fields (consensus_status,
// last_seen_epoch) on top.
//
// Gated behind a reachable real Postgres exactly like
// TestMigrate_AppliesCleanlyAgainstRealPostgres above (see that test's doc comment
// for why this sandbox has none, and DISPATCH_BRIEF.md's report for the explicit
// "not run against a real Postgres this round" callout) - SKIPS rather than either
// failing the suite or fabricating a pass.
func TestUpsertValidatorHealth_MergesWithoutClobberingOtherSourcesFields(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		dsn = "postgres://tari_ootle_explorer:tari_ootle_explorer@localhost:5432/tari_ootle_explorer?sslmode=disable"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	database, err := Connect(ctx, dsn)
	if err != nil {
		t.Skipf("SKIP: no reachable Postgres to test real upsert merge semantics against (tried %s): %v", dsn, err)
	}
	defer database.Close()

	if _, err := database.Pool.Exec(ctx, `DROP TABLE IF EXISTS schema_migrations, ootle_blocks, validators, burn_claims, template_registry CASCADE`); err != nil {
		t.Fatalf("cleanup before test: %v", err)
	}
	if err := database.Migrate(ctx); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}

	const pubKey = "10780fe1b4d31f8350e4b881dccd8f8210ce056050ac6dd8cca88ca16710b044"

	// Source A (internal/explorer, from the indexer's REST /validators): writes
	// public_key/last_seen_epoch/shard_group_start/shard_group_end_inclusive. Never
	// touches consensus_status (see UpsertValidator's own doc comment).
	if err := database.UpsertValidator(ctx, Validator{
		PublicKey:              pubKey,
		LastSeenEpoch:          10990,
		ShardGroupStart:        1,
		ShardGroupEndInclusive: 256,
	}); err != nil {
		t.Fatalf("UpsertValidator() [source A] error = %v", err)
	}

	// Source B (internal/vnhealth, from the validator's own JSON-RPC): a PARTIAL
	// poll result - only consensus_status is known this round (e.g.
	// get_consensus_status succeeded but get_epoch_manager_stats's committee_info
	// came back nil, so shard_group_start/end_inclusive/last_seen_epoch are all
	// genuinely unknown THIS poll, not zero).
	consensusStatus := "Running"
	if err := database.UpsertValidatorHealth(ctx, ValidatorHealth{
		PublicKey:       pubKey,
		ConsensusStatus: &consensusStatus,
	}); err != nil {
		t.Fatalf("UpsertValidatorHealth() [source B, partial] error = %v", err)
	}

	var gotLastSeenEpoch int64
	var gotShardStart, gotShardEnd int32
	var gotConsensusStatus *string
	err = database.Pool.QueryRow(ctx,
		`SELECT last_seen_epoch, shard_group_start, shard_group_end_inclusive, consensus_status FROM validators WHERE public_key = $1`,
		pubKey,
	).Scan(&gotLastSeenEpoch, &gotShardStart, &gotShardEnd, &gotConsensusStatus)
	if err != nil {
		t.Fatalf("query merged row: %v", err)
	}

	// Source A's fields must have survived source B's partial upsert untouched.
	if gotLastSeenEpoch != 10990 {
		t.Errorf("last_seen_epoch = %d, want 10990 (source A's value clobbered)", gotLastSeenEpoch)
	}
	if gotShardStart != 1 || gotShardEnd != 256 {
		t.Errorf("shard_group = [%d,%d], want [1,256] (source A's value clobbered)", gotShardStart, gotShardEnd)
	}
	// Source B's own field must have been applied.
	if gotConsensusStatus == nil || *gotConsensusStatus != "Running" {
		t.Errorf("consensus_status = %v, want \"Running\" (source B's value not applied)", gotConsensusStatus)
	}

	// A subsequent source B poll WITH real shard_group/epoch data must correctly
	// update those columns too (merge semantics aren't "B can never write these
	// columns", just "a nil field on a given call doesn't clobber").
	lastSeenEpoch := uint64(10991)
	shardStart := uint32(1)
	shardEnd := uint32(256)
	newStatus := "Syncing"
	if err := database.UpsertValidatorHealth(ctx, ValidatorHealth{
		PublicKey:              pubKey,
		LastSeenEpoch:          &lastSeenEpoch,
		ShardGroupStart:        &shardStart,
		ShardGroupEndInclusive: &shardEnd,
		ConsensusStatus:        &newStatus,
	}); err != nil {
		t.Fatalf("UpsertValidatorHealth() [source B, full] error = %v", err)
	}
	err = database.Pool.QueryRow(ctx,
		`SELECT last_seen_epoch, consensus_status FROM validators WHERE public_key = $1`,
		pubKey,
	).Scan(&gotLastSeenEpoch, &gotConsensusStatus)
	if err != nil {
		t.Fatalf("query row after full B poll: %v", err)
	}
	if gotLastSeenEpoch != 10991 {
		t.Errorf("last_seen_epoch = %d, want 10991 after a full source B poll", gotLastSeenEpoch)
	}
	if gotConsensusStatus == nil || *gotConsensusStatus != "Syncing" {
		t.Errorf("consensus_status = %v, want \"Syncing\" after a full source B poll", gotConsensusStatus)
	}

	// Finally: a brand-new public_key vnhealth observes before internal/explorer
	// ever does (e.g. a VN not yet in the indexer's roster) must insert cleanly with
	// 0 placeholders for the NOT NULL shard/epoch columns, not fail the insert.
	const newPubKey = "c480fe3a76f03dcdce8d7c85ac7d0001ec97b667260172709b23818c8707e503"
	onlineStatus := "Running"
	if err := database.UpsertValidatorHealth(ctx, ValidatorHealth{
		PublicKey:       newPubKey,
		ConsensusStatus: &onlineStatus,
	}); err != nil {
		t.Fatalf("UpsertValidatorHealth() [new public_key, partial] error = %v", err)
	}
	var newShardStart, newShardEnd int32
	var newLastSeenEpoch int64
	err = database.Pool.QueryRow(ctx,
		`SELECT last_seen_epoch, shard_group_start, shard_group_end_inclusive FROM validators WHERE public_key = $1`,
		newPubKey,
	).Scan(&newLastSeenEpoch, &newShardStart, &newShardEnd)
	if err != nil {
		t.Fatalf("query newly-inserted row: %v", err)
	}
	if newLastSeenEpoch != 0 || newShardStart != 0 || newShardEnd != 0 {
		t.Errorf("new row = last_seen_epoch=%d shard=[%d,%d], want all 0 placeholders", newLastSeenEpoch, newShardStart, newShardEnd)
	}
}
