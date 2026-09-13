package burnclaim

import (
	"context"
	"crypto/tls"
	"os"
	"testing"
	"time"

	"github.com/Snipa22/go-tari-ootle-explorer/internal/db"
	"github.com/Snipa22/go-tari-ootle-explorer/internal/indexerclient"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// TestScan_AgainstLiveL1AndLiveIndexerAndRealPostgres is the real end-to-end
// integration test DISPATCH_BRIEF.md's verification bar asks for: a genuine Scan run
// against a LIVE public L1 base-node GRPC endpoint and the LIVE public indexer,
// persisting into a REAL Postgres database.
//
// Every dependency is gated behind an environment variable so this test SKIPS
// cleanly (never fabricates a pass) when unavailable, same pattern as
// internal/explorer's own TestBackfill_AgainstLiveIndexerAndRealPostgres:
//   - TEST_LIVE_L1_GRPC_HOST: an L1 base-node GRPC "host:port" to scan. Unset ->
//     skip. This dispatch used grpc.esmeralda.tari.com:443 (confirmed reachable via
//     TLS - see TEST_LIVE_L1_GRPC_TLS below).
//   - TEST_LIVE_L1_GRPC_TLS: "1" if the above host is TLS-terminated (it is, for the
//     public esmeralda endpoint used in this dispatch - grpc.NewClient's default
//     insecure credentials do NOT work against it).
//   - TEST_LIVE_INDEXER_URL: the indexer base URL. Unset -> skip.
//   - TEST_POSTGRES_DSN: a reachable Postgres DSN. Unset or unreachable -> skip.
//
// IMPORTANT, per this package's own doc comment: this test's L1 range is real chain
// data but is NOT known to contain a real burn (none were found in the ranges
// checked during this dispatch - see the dispatch report) - so this test only
// proves the SCAN ITSELF runs cleanly against live data end to end (GetTipInfo,
// GetBlockByHeight, persisting whatever it finds, and a CheckClaims/DetectStuck
// pass against real Postgres), not that a real burn was correctly detected,
// claim-checked, and marked. That gap is covered by the mock-based tests elsewhere
// in this package instead - see scanner_test.go/claimcheck_test.go/stuck_test.go.
func TestScan_AgainstLiveL1AndLiveIndexerAndRealPostgres(t *testing.T) {
	l1Host := os.Getenv("TEST_LIVE_L1_GRPC_HOST")
	if l1Host == "" {
		t.Skip("SKIP: TEST_LIVE_L1_GRPC_HOST not set - no live L1 base-node GRPC endpoint to test Scan against")
	}
	indexerURL := os.Getenv("TEST_LIVE_INDEXER_URL")
	if indexerURL == "" {
		t.Skip("SKIP: TEST_LIVE_INDEXER_URL not set - no live indexer to test CheckClaims against")
	}
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("SKIP: TEST_POSTGRES_DSN not set - no Postgres to test Scan against")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	database, err := db.Connect(ctx, dsn)
	if err != nil {
		t.Skipf("SKIP: could not connect to Postgres at %s: %v", dsn, err)
	}
	defer database.Close()

	if _, err := database.Pool.Exec(ctx, `DROP TABLE IF EXISTS schema_migrations, ootle_blocks, validators, burn_claims, template_registry CASCADE`); err != nil {
		t.Fatalf("cleanup before test: %v", err)
	}
	if err := database.Migrate(ctx); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}

	var dialOpts []grpc.DialOption
	if os.Getenv("TEST_LIVE_L1_GRPC_TLS") == "1" {
		dialOpts = append(dialOpts, grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{})))
	}
	l1, err := NewGRPCClient([]string{l1Host}, dialOpts...)
	if err != nil {
		t.Fatalf("NewGRPCClient: %v", err)
	}
	defer l1.Close()

	// Sanity-check the live L1 endpoint is actually reachable before running the
	// full Scan against it, so a network hiccup surfaces as a clear skip/fail here
	// rather than a confusing failure deep inside Scan.
	tip, err := l1.GetTipInfo(ctx)
	if err != nil {
		t.Skipf("SKIP: live L1 GRPC endpoint %s not reachable: %v", l1Host, err)
	}
	tipHeight := tip.GetMetadata().GetBestBlockHeight()
	t.Logf("live L1 tip: height=%d", tipHeight)
	if tipHeight < ScanBatchSize {
		t.Skipf("SKIP: live L1 tip height %d is too low to scan a meaningful range", tipHeight)
	}

	indexer := indexerclient.New(indexerURL)
	info, err := indexer.GetInfo(ctx)
	if err != nil {
		t.Skipf("SKIP: live indexer at %s not reachable: %v", indexerURL, err)
	}
	t.Logf("live indexer info: network=%s current_epoch=%d version=%s", info.Network, info.CurrentEpoch, info.Version)

	lag, ok := KnownBaseLayerConfirmations[info.Network]
	if !ok {
		lag = 100 // fall back to a documented-safe default rather than fail the live test outright over an unrecognized network name
	}

	tracker := New(l1, indexer, database, lag, DefaultExtraStuckBlocks)

	// Scan a real, recent range - deliberately small (one ScanBatchSize) to keep
	// this live test fast; see this test's doc comment for why zero real burns in
	// this range is an expected, acceptable outcome.
	from := tipHeight - ScanBatchSize + 1
	found, err := tracker.Scan(ctx, from, tipHeight)
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	t.Logf("live Scan([%d,%d]): found %d burn(s)", from, tipHeight, found)

	var burnClaimsRowCount int
	if err := database.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM burn_claims`).Scan(&burnClaimsRowCount); err != nil {
		t.Fatalf("count burn_claims: %v", err)
	}
	if burnClaimsRowCount != found {
		t.Errorf("burn_claims row count = %d, want %d (== Scan's own found count)", burnClaimsRowCount, found)
	}
	t.Logf("burn_claims table: %d row(s) after live scan", burnClaimsRowCount)
}
