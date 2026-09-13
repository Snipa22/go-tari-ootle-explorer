package explorer

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/Snipa22/go-tari-ootle-explorer/internal/db"
	"github.com/Snipa22/go-tari-ootle-explorer/internal/indexerclient"
)

// TestBackfill_AgainstLiveIndexerAndRealPostgres is the real end-to-end integration
// test the v1 scope brief asks for: a genuine Backfill run against the LIVE testnet
// indexer (https://ootle-indexer-a.tari.com, esmeralda), persisting into a REAL
// Postgres database, then checking rows actually landed.
//
// Both preconditions are gated behind environment variables so this test SKIPS
// cleanly (rather than failing the whole suite, or - worse - fabricating a pass) when
// either dependency is unavailable, per DISPATCH_BRIEF.md's explicit instruction:
//   - TEST_LIVE_INDEXER_URL: the indexer base URL to hit. Unset -> skip (no assumed
//     live-network dependency for the default `go test ./...` run in CI or an
//     offline dev machine).
//   - TEST_POSTGRES_DSN: a reachable Postgres DSN. Unset or unreachable -> skip.
//
// This dispatch's sandbox DID have both available - see the dispatch report for the
// real command used to run this test and its real output.
func TestBackfill_AgainstLiveIndexerAndRealPostgres(t *testing.T) {
	indexerURL := os.Getenv("TEST_LIVE_INDEXER_URL")
	if indexerURL == "" {
		t.Skip("SKIP: TEST_LIVE_INDEXER_URL not set - no live indexer to test Backfill against")
	}
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("SKIP: TEST_POSTGRES_DSN not set - no Postgres to test Backfill against")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	database, err := db.Connect(ctx, dsn)
	if err != nil {
		t.Skipf("SKIP: could not connect to Postgres at %s: %v", dsn, err)
	}
	defer database.Close()

	// Clean slate so this test is repeatable against a persistent dev Postgres
	// instance, matching internal/db's own live-Postgres test convention.
	if _, err := database.Pool.Exec(ctx, `DROP TABLE IF EXISTS schema_migrations, ootle_blocks, validators, burn_claims, template_registry CASCADE`); err != nil {
		t.Fatalf("cleanup before test: %v", err)
	}
	if err := database.Migrate(ctx); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}

	indexer := indexerclient.New(indexerURL)

	// Sanity-check the live indexer is actually reachable and looks like a real
	// tari_indexer before running the full Backfill against it, so a network hiccup
	// surfaces as a clear skip/fail here rather than a confusing failure deep inside
	// Backfill.
	info, err := indexer.GetInfo(ctx)
	if err != nil {
		t.Skipf("SKIP: live indexer at %s not reachable: %v", indexerURL, err)
	}
	t.Logf("live indexer info: network=%s current_epoch=%d version=%s", info.Network, info.CurrentEpoch, info.Version)

	e := New(indexer, database)
	if err := e.Backfill(ctx); err != nil {
		t.Fatalf("Backfill() error = %v", err)
	}

	var validatorCount int
	if err := database.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM validators`).Scan(&validatorCount); err != nil {
		t.Fatalf("count validators: %v", err)
	}
	if validatorCount == 0 {
		t.Errorf("expected at least one validator row after Backfill against a live network, got 0")
	}
	t.Logf("validators table: %d rows", validatorCount)

	var blockCount int
	if err := database.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM ootle_blocks`).Scan(&blockCount); err != nil {
		t.Fatalf("count ootle_blocks: %v", err)
	}
	if blockCount != 1 {
		t.Errorf("expected exactly 1 ootle_blocks row after a single Backfill, got %d", blockCount)
	}
	t.Logf("ootle_blocks table: %d rows", blockCount)

	var templateCount int
	if err := database.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM template_registry`).Scan(&templateCount); err != nil {
		t.Fatalf("count template_registry: %v", err)
	}
	if templateCount == 0 {
		t.Errorf("expected at least one template_registry row after Backfill against a live network, got 0")
	}
	t.Logf("template_registry table: %d rows", templateCount)
}
