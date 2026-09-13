package db

import (
	"context"
	"errors"
	"math"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// testDSN mirrors db_test.go's own DSN resolution (TEST_POSTGRES_DSN override, else
// the documented local-dev default) - kept as a small helper here purely to avoid
// repeating the same six lines across every test in this file.
func testDSN() string {
	if dsn := os.Getenv("TEST_POSTGRES_DSN"); dsn != "" {
		return dsn
	}
	return "postgres://tari_ootle_explorer:tari_ootle_explorer@localhost:5432/tari_ootle_explorer?sslmode=disable"
}

// connectForReadTests connects and migrates a clean-slate database, skipping the
// calling test (not failing it) if no Postgres is reachable - the exact same
// skip-cleanly-if-unavailable convention every other live test in this package (see
// db_test.go) and every other package in this repo (per AGENTS.md) already follows.
func connectForReadTests(t *testing.T) *DB {
	t.Helper()

	dsn := testDSN()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	database, err := Connect(ctx, dsn)
	if err != nil {
		t.Skipf("SKIP: no reachable Postgres to test real read methods against (tried %s): %v", dsn, err)
	}
	t.Cleanup(database.Close)

	if _, err := database.Pool.Exec(ctx, `DROP TABLE IF EXISTS schema_migrations, ootle_blocks, validators, burn_claims, template_registry CASCADE`); err != nil {
		t.Fatalf("cleanup before test: %v", err)
	}
	if err := database.Migrate(ctx); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	return database
}

// TestListAndGetOotleBlocks_AgainstRealPostgres is the real, live-Postgres proof for
// this dispatch's ootle_blocks read methods: ListOotleBlocks' before-cursor + DESC
// pagination (mirroring go-tari-explorer's own blocks-list HTMX "load more" shape -
// see DISPATCH_BRIEF.md) and GetOotleBlock's single-row lookup, including its
// pgx.ErrNoRows behavior for an unknown block_id.
func TestListAndGetOotleBlocks_AgainstRealPostgres(t *testing.T) {
	database := connectForReadTests(t)
	ctx := context.Background()

	for i, height := range []uint64{10, 20, 30} {
		if err := database.UpsertOotleBlock(ctx, OotleBlock{
			BlockID:      "block-" + string(rune('a'+i)),
			Height:       height,
			Epoch:        100,
			Timestamp:    1000 + int64(height),
			CommandCount: int32(i),
		}); err != nil {
			t.Fatalf("UpsertOotleBlock(height=%d): %v", height, err)
		}
	}

	// First page (before=MaxInt64, limit=2): highest heights first.
	page1, err := database.ListOotleBlocks(ctx, math.MaxInt64, 2)
	if err != nil {
		t.Fatalf("ListOotleBlocks(first page): %v", err)
	}
	if len(page1) != 2 || page1[0].Height != 30 || page1[1].Height != 20 {
		t.Fatalf("page1 = %+v, want heights [30, 20] in DESC order", page1)
	}

	// Second page, cursored off the last row of page 1.
	page2, err := database.ListOotleBlocks(ctx, int64(page1[len(page1)-1].Height), 2)
	if err != nil {
		t.Fatalf("ListOotleBlocks(second page): %v", err)
	}
	if len(page2) != 1 || page2[0].Height != 10 {
		t.Fatalf("page2 = %+v, want exactly [height=10]", page2)
	}

	got, err := database.GetOotleBlock(ctx, "block-a")
	if err != nil {
		t.Fatalf("GetOotleBlock(block-a): %v", err)
	}
	if got.Height != 10 || got.Epoch != 100 {
		t.Errorf("GetOotleBlock(block-a) = %+v, want height=10 epoch=100", got)
	}

	if _, err := database.GetOotleBlock(ctx, "does-not-exist"); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("GetOotleBlock(unknown) error = %v, want wrapped pgx.ErrNoRows", err)
	}
}

// TestListAndGetValidators_AgainstRealPostgres is the real, live-Postgres proof for
// this dispatch's validators read methods: ListValidators' full-roster ordering,
// GetValidator's single-row lookup (including consensus_status/last_checked_at, not
// carried by db.Validator/db.ValidatorHealth's own write-side structs), and
// GetLiveValidatorEpochSummary's current-epoch liveness summary (including its
// empty-table zero-value, non-error behavior).
func TestListAndGetValidators_AgainstRealPostgres(t *testing.T) {
	database := connectForReadTests(t)
	ctx := context.Background()

	// Empty-table case first: GetLiveValidatorEpochSummary must return a zero-value
	// summary, not an error, before anything has ever been upserted.
	emptySummary, err := database.GetLiveValidatorEpochSummary(ctx)
	if err != nil {
		t.Fatalf("GetLiveValidatorEpochSummary(empty table): %v", err)
	}
	if emptySummary.Epoch != 0 || emptySummary.Count != 0 {
		t.Errorf("GetLiveValidatorEpochSummary(empty table) = %+v, want zero-value", emptySummary)
	}

	if err := database.UpsertValidator(ctx, Validator{
		PublicKey: "pubkey-a", LastSeenEpoch: 100, ShardGroupStart: 1, ShardGroupEndInclusive: 128,
	}); err != nil {
		t.Fatalf("UpsertValidator(pubkey-a): %v", err)
	}
	if err := database.UpsertValidator(ctx, Validator{
		PublicKey: "pubkey-b", LastSeenEpoch: 101, ShardGroupStart: 129, ShardGroupEndInclusive: 256,
	}); err != nil {
		t.Fatalf("UpsertValidator(pubkey-b): %v", err)
	}
	status := "Running"
	if err := database.UpsertValidatorHealth(ctx, ValidatorHealth{PublicKey: "pubkey-b", ConsensusStatus: &status}); err != nil {
		t.Fatalf("UpsertValidatorHealth(pubkey-b): %v", err)
	}

	list, err := database.ListValidators(ctx)
	if err != nil {
		t.Fatalf("ListValidators: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("len(ListValidators()) = %d, want 2", len(list))
	}
	// Ordered by last_seen_epoch DESC: pubkey-b (101) before pubkey-a (100).
	if list[0].PublicKey != "pubkey-b" || list[1].PublicKey != "pubkey-a" {
		t.Errorf("ListValidators() order = [%s, %s], want [pubkey-b, pubkey-a]", list[0].PublicKey, list[1].PublicKey)
	}
	if list[0].ConsensusStatus == nil || *list[0].ConsensusStatus != "Running" {
		t.Errorf("list[0].ConsensusStatus = %v, want \"Running\"", list[0].ConsensusStatus)
	}

	got, err := database.GetValidator(ctx, "pubkey-a")
	if err != nil {
		t.Fatalf("GetValidator(pubkey-a): %v", err)
	}
	if got.ShardGroupStart != 1 || got.ShardGroupEndInclusive != 128 {
		t.Errorf("GetValidator(pubkey-a) = %+v, want shard=[1,128]", got)
	}
	if got.ConsensusStatus != nil {
		t.Errorf("GetValidator(pubkey-a).ConsensusStatus = %v, want nil (never set)", got.ConsensusStatus)
	}

	if _, err := database.GetValidator(ctx, "does-not-exist"); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("GetValidator(unknown) error = %v, want wrapped pgx.ErrNoRows", err)
	}

	summary, err := database.GetLiveValidatorEpochSummary(ctx)
	if err != nil {
		t.Fatalf("GetLiveValidatorEpochSummary: %v", err)
	}
	if summary.Epoch != 101 || summary.Count != 1 {
		t.Errorf("GetLiveValidatorEpochSummary() = %+v, want {Epoch:101 Count:1}", summary)
	}
}

// TestListAndGetBurnClaims_AgainstRealPostgres is the real, live-Postgres proof for
// this dispatch's burn_claims read methods: ListBurnClaims' status filter (including
// the empty-status "show all" case per DISPATCH_BRIEF.md) and its DESC (most-recent-
// mined-first) ordering - deliberately the opposite of the write-side
// ListBurnClaimsByStatus's ASC ordering, see ListBurnClaims' own doc comment - plus
// GetBurnClaim's single-row lookup.
func TestListAndGetBurnClaims_AgainstRealPostgres(t *testing.T) {
	database := connectForReadTests(t)
	ctx := context.Background()

	if err := database.InsertPendingBurnClaim(ctx, BurnClaim{L1BurnTxHash: "kernel-1", Commitment: "commit-1", BurnHeight: 100}); err != nil {
		t.Fatalf("InsertPendingBurnClaim(kernel-1): %v", err)
	}
	if err := database.InsertPendingBurnClaim(ctx, BurnClaim{L1BurnTxHash: "kernel-2", Commitment: "commit-2", BurnHeight: 200}); err != nil {
		t.Fatalf("InsertPendingBurnClaim(kernel-2): %v", err)
	}
	if err := database.MarkBurnStuck(ctx, "kernel-1"); err != nil {
		t.Fatalf("MarkBurnStuck(kernel-1): %v", err)
	}

	all, err := database.ListBurnClaims(ctx, "")
	if err != nil {
		t.Fatalf("ListBurnClaims(\"\"): %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("len(ListBurnClaims(\"\")) = %d, want 2", len(all))
	}
	// DESC by burn_height: kernel-2 (200) before kernel-1 (100).
	if all[0].L1BurnTxHash != "kernel-2" || all[1].L1BurnTxHash != "kernel-1" {
		t.Errorf("ListBurnClaims(\"\") order = [%s, %s], want [kernel-2, kernel-1]", all[0].L1BurnTxHash, all[1].L1BurnTxHash)
	}

	stuck, err := database.ListBurnClaims(ctx, "stuck")
	if err != nil {
		t.Fatalf("ListBurnClaims(stuck): %v", err)
	}
	if len(stuck) != 1 || stuck[0].L1BurnTxHash != "kernel-1" {
		t.Fatalf("ListBurnClaims(stuck) = %+v, want exactly [kernel-1]", stuck)
	}

	pending, err := database.ListBurnClaims(ctx, "pending")
	if err != nil {
		t.Fatalf("ListBurnClaims(pending): %v", err)
	}
	if len(pending) != 1 || pending[0].L1BurnTxHash != "kernel-2" {
		t.Fatalf("ListBurnClaims(pending) = %+v, want exactly [kernel-2]", pending)
	}

	got, err := database.GetBurnClaim(ctx, "kernel-2")
	if err != nil {
		t.Fatalf("GetBurnClaim(kernel-2): %v", err)
	}
	if got.Commitment != "commit-2" || got.BurnHeight != 200 {
		t.Errorf("GetBurnClaim(kernel-2) = %+v, want commitment=commit-2 burn_height=200", got)
	}

	if _, err := database.GetBurnClaim(ctx, "does-not-exist"); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("GetBurnClaim(unknown) error = %v, want wrapped pgx.ErrNoRows", err)
	}
}

// TestListAndGetTemplateRegistry_AgainstRealPostgres is the real, live-Postgres proof
// for this dispatch's template_registry read methods: ListTemplateRegistry's before-
// cursor + DESC (by first_seen_at) pagination and GetTemplateRegistryEntry's single-
// row lookup, including fields that only exist on the metadata-server side
// (code_size/is_featured) merged into the same row as the indexer-catalogue side
// (template_name/author_public_key/at_epoch) - proving TemplateRegistryRow really
// does read back the full merged row, not just one source's columns.
func TestListAndGetTemplateRegistry_AgainstRealPostgres(t *testing.T) {
	database := connectForReadTests(t)
	ctx := context.Background()

	if err := database.UpsertTemplateRegistryEntry(ctx, TemplateRegistryEntry{
		TemplateAddress: "addr-1", TemplateName: "First", AuthorPublicKey: "author-1", BinaryHash: "hash-1", AtEpoch: 10,
	}); err != nil {
		t.Fatalf("UpsertTemplateRegistryEntry(addr-1): %v", err)
	}
	// Ensure addr-2 gets a strictly later first_seen_at than addr-1 for a
	// deterministic DESC-order assertion below.
	time.Sleep(10 * time.Millisecond)
	if err := database.UpsertTemplateRegistryEntry(ctx, TemplateRegistryEntry{
		TemplateAddress: "addr-2", TemplateName: "Second", AuthorPublicKey: "author-2", BinaryHash: "hash-2", AtEpoch: 20,
	}); err != nil {
		t.Fatalf("UpsertTemplateRegistryEntry(addr-2): %v", err)
	}
	codeSize := int64(4096)
	isFeatured := true
	if err := database.UpsertTemplateRegistryMetadata(ctx, TemplateRegistryMetadataEntry{
		TemplateAddress: "addr-2", CodeSize: &codeSize, IsFeatured: &isFeatured,
	}); err != nil {
		t.Fatalf("UpsertTemplateRegistryMetadata(addr-2): %v", err)
	}

	list, err := database.ListTemplateRegistry(ctx, time.Now(), 10)
	if err != nil {
		t.Fatalf("ListTemplateRegistry: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("len(ListTemplateRegistry()) = %d, want 2", len(list))
	}
	// DESC by first_seen_at: addr-2 (observed later) before addr-1.
	if list[0].TemplateAddress != "addr-2" || list[1].TemplateAddress != "addr-1" {
		t.Errorf("ListTemplateRegistry() order = [%s, %s], want [addr-2, addr-1]", list[0].TemplateAddress, list[1].TemplateAddress)
	}
	if list[0].CodeSize == nil || *list[0].CodeSize != 4096 || !list[0].IsFeatured {
		t.Errorf("list[0] (addr-2) = %+v, want code_size=4096 is_featured=true", list[0])
	}

	// Pagination: before=list[0].FirstSeenAt should yield only addr-1.
	page2, err := database.ListTemplateRegistry(ctx, list[0].FirstSeenAt, 10)
	if err != nil {
		t.Fatalf("ListTemplateRegistry(before addr-2's first_seen_at): %v", err)
	}
	if len(page2) != 1 || page2[0].TemplateAddress != "addr-1" {
		t.Fatalf("page2 = %+v, want exactly [addr-1]", page2)
	}

	got, err := database.GetTemplateRegistryEntry(ctx, "addr-2")
	if err != nil {
		t.Fatalf("GetTemplateRegistryEntry(addr-2): %v", err)
	}
	if got.TemplateName != "Second" || got.AtEpoch != 20 || got.CodeSize == nil || *got.CodeSize != 4096 {
		t.Errorf("GetTemplateRegistryEntry(addr-2) = %+v, want name=Second at_epoch=20 code_size=4096", got)
	}

	if _, err := database.GetTemplateRegistryEntry(ctx, "does-not-exist"); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("GetTemplateRegistryEntry(unknown) error = %v, want wrapped pgx.ErrNoRows", err)
	}
}
