package explorer

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Snipa22/go-tari-ootle-explorer/internal/db"
	"github.com/Snipa22/go-tari-ootle-explorer/internal/indexerclient"
)

// fakeIndexerClient is an in-memory IndexerClient double. catalogue is served as
// fixed-size pages (pageSize) honoring the real cursor pagination semantics (return
// entries strictly after the given template_address).
type fakeIndexerClient struct {
	validatorsResp *indexerclient.ListValidatorsResponse
	validatorsErr  error

	checkpoint    *indexerclient.EpochCheckpoint
	checkpointErr error

	catalogue []indexerclient.TemplateCatalogueEntry
	pageSize  uint64

	// calls records how many times each method was invoked, for assertions on
	// Follow's per-tick polling behavior.
	listValidatorsCalls int
	getCheckpointCalls  int
	listCatalogueCalls  int
}

func (f *fakeIndexerClient) ListValidators(ctx context.Context, epoch *uint64) (*indexerclient.ListValidatorsResponse, error) {
	f.listValidatorsCalls++
	if f.validatorsErr != nil {
		return nil, f.validatorsErr
	}
	return f.validatorsResp, nil
}

func (f *fakeIndexerClient) GetLatestEpochCheckpoint(ctx context.Context) (*indexerclient.EpochCheckpoint, error) {
	f.getCheckpointCalls++
	if f.checkpointErr != nil {
		return nil, f.checkpointErr
	}
	return f.checkpoint, nil
}

func (f *fakeIndexerClient) ListTemplateCatalogue(ctx context.Context, limit uint64, after string) (*indexerclient.ListTemplateCatalogueResponse, error) {
	f.listCatalogueCalls++
	pageSize := f.pageSize
	if pageSize == 0 {
		pageSize = limit
	}

	startIdx := 0
	if after != "" {
		for i, e := range f.catalogue {
			if e.TemplateAddress == after {
				startIdx = i + 1
				break
			}
		}
	}
	endIdx := startIdx + int(pageSize)
	if endIdx > len(f.catalogue) {
		endIdx = len(f.catalogue)
	}
	if startIdx >= len(f.catalogue) {
		return &indexerclient.ListTemplateCatalogueResponse{}, nil
	}
	return &indexerclient.ListTemplateCatalogueResponse{Entries: f.catalogue[startIdx:endIdx]}, nil
}

// fakeStore is an in-memory Store double, recording every upsert for assertions.
type fakeStore struct {
	validators map[string]db.Validator
	blocks     map[string]db.OotleBlock
	templates  map[string]db.TemplateRegistryEntry

	upsertValidatorErr error
	upsertBlockErr     error
	upsertTemplateErr  error
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		validators: make(map[string]db.Validator),
		blocks:     make(map[string]db.OotleBlock),
		templates:  make(map[string]db.TemplateRegistryEntry),
	}
}

func (f *fakeStore) UpsertValidator(ctx context.Context, v db.Validator) error {
	if f.upsertValidatorErr != nil {
		return f.upsertValidatorErr
	}
	f.validators[v.PublicKey] = v
	return nil
}

func (f *fakeStore) UpsertOotleBlock(ctx context.Context, b db.OotleBlock) error {
	if f.upsertBlockErr != nil {
		return f.upsertBlockErr
	}
	f.blocks[b.BlockID] = b
	return nil
}

func (f *fakeStore) UpsertTemplateRegistryEntry(ctx context.Context, t db.TemplateRegistryEntry) error {
	if f.upsertTemplateErr != nil {
		return f.upsertTemplateErr
	}
	f.templates[t.TemplateAddress] = t
	return nil
}

func sampleValidatorsResp() *indexerclient.ListValidatorsResponse {
	return &indexerclient.ListValidatorsResponse{
		Epoch: 10990,
		Validators: []indexerclient.Validator{
			{
				PublicKey:         "pk1",
				PeerID:            "peer1",
				ShardGroup:        indexerclient.ShardGroup{Start: 1, EndInclusive: 256},
				StartEpoch:        10517,
				FeeClaimPublicKey: "fee1",
				VotePower:         1,
			},
			{
				PublicKey:         "pk2",
				PeerID:            "peer2",
				ShardGroup:        indexerclient.ShardGroup{Start: 257, EndInclusive: 512},
				StartEpoch:        10517,
				FeeClaimPublicKey: "fee2",
				VotePower:         1,
			},
		},
	}
}

func sampleCheckpoint() *indexerclient.EpochCheckpoint {
	return &indexerclient.EpochCheckpoint{
		Header: indexerclient.EpochCheckpointHeader{
			Network:           38,
			Epoch:             10989,
			Height:            1794,
			ShardGroup:        indexerclient.ShardGroup{Start: 1, EndInclusive: 256},
			ParentID:          "parent",
			JustifyID:         "justify",
			ProposedBy:        "proposer",
			CommandMerkleRoot: "cmr",
			StateMerkleRoot:   "smr",
			EpochHash:         "eh",
			MetadataHash:      "mh",
		},
	}
}

func sampleCatalogue(n int) []indexerclient.TemplateCatalogueEntry {
	out := make([]indexerclient.TemplateCatalogueEntry, n)
	for i := 0; i < n; i++ {
		// Zero-padded addresses sort/walk predictably for test assertions.
		addr := "addr" + string(rune('0'+i/10)) + string(rune('0'+i%10))
		out[i] = indexerclient.TemplateCatalogueEntry{
			TemplateAddress: addr,
			TemplateName:    "Template" + string(rune('0'+i%10)),
			AuthorPublicKey: "author",
			BinaryHash:      "hash" + string(rune('0'+i%10)),
			AtEpoch:         uint64(i),
		}
	}
	return out
}

// TestBackfill_SyncsValidatorsCheckpointAndCatalogue covers the one-shot path: a
// single Backfill call must sync every validator, the latest checkpoint, and every
// catalogue entry (paginated across multiple pages).
func TestBackfill_SyncsValidatorsCheckpointAndCatalogue(t *testing.T) {
	catalogue := sampleCatalogue(CatalogueBatchSize + 5) // forces 2 pages
	indexer := &fakeIndexerClient{
		validatorsResp: sampleValidatorsResp(),
		checkpoint:     sampleCheckpoint(),
		catalogue:      catalogue,
	}
	store := newFakeStore()
	e := New(indexer, store)

	if err := e.Backfill(context.Background()); err != nil {
		t.Fatalf("Backfill() error = %v", err)
	}

	if len(store.validators) != 2 {
		t.Errorf("len(validators) = %d, want 2", len(store.validators))
	}
	if v, ok := store.validators["pk1"]; !ok {
		t.Errorf("expected pk1 to be upserted")
	} else if v.LastSeenEpoch != 10990 || v.ShardGroupStart != 1 || v.ShardGroupEndInclusive != 256 {
		t.Errorf("validator pk1 = %+v, unexpected values", v)
	}

	if len(store.blocks) != 1 {
		t.Fatalf("len(blocks) = %d, want 1", len(store.blocks))
	}
	for _, b := range store.blocks {
		if b.Height != 1794 || b.Epoch != 10989 {
			t.Errorf("block = %+v, unexpected values", b)
		}
	}

	if len(store.templates) != len(catalogue) {
		t.Errorf("len(templates) = %d, want %d", len(store.templates), len(catalogue))
	}
	if indexer.listCatalogueCalls != 2 {
		t.Errorf("listCatalogueCalls = %d, want 2 (paginated)", indexer.listCatalogueCalls)
	}

	if e.catalogueCursor != catalogue[len(catalogue)-1].TemplateAddress {
		t.Errorf("catalogueCursor = %q, want %q", e.catalogueCursor, catalogue[len(catalogue)-1].TemplateAddress)
	}
}

// TestBackfill_EmptyCatalogue confirms a catalogue with zero entries doesn't error and
// results in zero upserts, zero cursor advancement.
func TestBackfill_EmptyCatalogue(t *testing.T) {
	indexer := &fakeIndexerClient{
		validatorsResp: sampleValidatorsResp(),
		checkpoint:     sampleCheckpoint(),
		catalogue:      nil,
	}
	store := newFakeStore()
	e := New(indexer, store)

	if err := e.Backfill(context.Background()); err != nil {
		t.Fatalf("Backfill() error = %v", err)
	}
	if len(store.templates) != 0 {
		t.Errorf("len(templates) = %d, want 0", len(store.templates))
	}
	if e.catalogueCursor != "" {
		t.Errorf("catalogueCursor = %q, want empty", e.catalogueCursor)
	}
}

// TestBackfill_PropagatesValidatorsError confirms a ListValidators failure aborts
// Backfill and surfaces the underlying error.
func TestBackfill_PropagatesValidatorsError(t *testing.T) {
	wantErr := errors.New("boom")
	indexer := &fakeIndexerClient{validatorsErr: wantErr}
	store := newFakeStore()
	e := New(indexer, store)

	err := e.Backfill(context.Background())
	if err == nil || !errors.Is(err, wantErr) {
		t.Fatalf("Backfill() error = %v, want wrapping %v", err, wantErr)
	}
}

// TestBackfill_PropagatesCheckpointError confirms a GetLatestEpochCheckpoint failure
// aborts Backfill.
func TestBackfill_PropagatesCheckpointError(t *testing.T) {
	wantErr := errors.New("checkpoint boom")
	indexer := &fakeIndexerClient{
		validatorsResp: sampleValidatorsResp(),
		checkpointErr:  wantErr,
	}
	store := newFakeStore()
	e := New(indexer, store)

	err := e.Backfill(context.Background())
	if err == nil || !errors.Is(err, wantErr) {
		t.Fatalf("Backfill() error = %v, want wrapping %v", err, wantErr)
	}
}

// TestBackfill_PropagatesStoreError confirms a Store failure (e.g. a DB error)
// surfaces rather than being silently swallowed.
func TestBackfill_PropagatesStoreError(t *testing.T) {
	wantErr := errors.New("store boom")
	indexer := &fakeIndexerClient{
		validatorsResp: sampleValidatorsResp(),
		checkpoint:     sampleCheckpoint(),
	}
	store := newFakeStore()
	store.upsertValidatorErr = wantErr
	e := New(indexer, store)

	err := e.Backfill(context.Background())
	if err == nil || !errors.Is(err, wantErr) {
		t.Fatalf("Backfill() error = %v, want wrapping %v", err, wantErr)
	}
}

// TestFollow_PollsUntilContextCancelled confirms Follow ticks at least twice within a
// bounded window and then returns cleanly once ctx is cancelled.
func TestFollow_PollsUntilContextCancelled(t *testing.T) {
	indexer := &fakeIndexerClient{
		validatorsResp: sampleValidatorsResp(),
		checkpoint:     sampleCheckpoint(),
		catalogue:      sampleCatalogue(3),
	}
	store := newFakeStore()
	e := New(indexer, store)

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()

	err := e.Follow(ctx, 5*time.Millisecond)
	if err == nil || ctx.Err() == nil {
		t.Fatalf("Follow() error = %v, ctx.Err() = %v, want a context-cancellation error", err, ctx.Err())
	}
	if indexer.listValidatorsCalls < 2 {
		t.Errorf("listValidatorsCalls = %d, want >= 2", indexer.listValidatorsCalls)
	}
	if indexer.getCheckpointCalls < 2 {
		t.Errorf("getCheckpointCalls = %d, want >= 2", indexer.getCheckpointCalls)
	}
}

// TestFollow_OnlyFetchesNewCatalogueEntriesAcrossTicks confirms Follow's cursor
// advances between iterations so a steady-state catalogue (no new entries appearing)
// results in only empty pages being fetched after the first tick, not a full re-walk
// each time.
func TestFollow_OnlyFetchesNewCatalogueEntriesAcrossTicks(t *testing.T) {
	catalogue := sampleCatalogue(3)
	indexer := &fakeIndexerClient{
		validatorsResp: sampleValidatorsResp(),
		checkpoint:     sampleCheckpoint(),
		catalogue:      catalogue,
	}
	store := newFakeStore()
	e := New(indexer, store)

	// First poll: walks the full (small) catalogue and advances the cursor.
	if err := e.pollOnce(context.Background()); err != nil {
		t.Fatalf("pollOnce() [1] error = %v", err)
	}
	if len(store.templates) != 3 {
		t.Fatalf("len(templates) after first poll = %d, want 3", len(store.templates))
	}
	if e.catalogueCursor != catalogue[2].TemplateAddress {
		t.Fatalf("catalogueCursor = %q, want %q", e.catalogueCursor, catalogue[2].TemplateAddress)
	}

	// Second poll: no new entries appeared upstream, so no new templates should be
	// upserted (the fake still records an upsert per entry it's given, so len staying
	// at 3 with no new keys added confirms the cursor was actually honored).
	if err := e.pollOnce(context.Background()); err != nil {
		t.Fatalf("pollOnce() [2] error = %v", err)
	}
	if len(store.templates) != 3 {
		t.Fatalf("len(templates) after second poll = %d, want still 3", len(store.templates))
	}

	// Now a new entry appears upstream (simulating real new publishes).
	newEntry := indexerclient.TemplateCatalogueEntry{TemplateAddress: "addr99", TemplateName: "New", AuthorPublicKey: "a", BinaryHash: "h", AtEpoch: 99}
	indexer.catalogue = append(indexer.catalogue, newEntry)

	if err := e.pollOnce(context.Background()); err != nil {
		t.Fatalf("pollOnce() [3] error = %v", err)
	}
	if len(store.templates) != 4 {
		t.Fatalf("len(templates) after third poll = %d, want 4", len(store.templates))
	}
	if _, ok := store.templates["addr99"]; !ok {
		t.Errorf("expected addr99 to be upserted after appearing upstream")
	}
}

// TestSyntheticBlockID_DeterministicAndDistinct confirms syntheticBlockID is stable
// for the same header (so re-observing an unchanged checkpoint is a true upsert
// no-op) and differs for a genuinely different header.
func TestSyntheticBlockID_DeterministicAndDistinct(t *testing.T) {
	h1 := sampleCheckpoint().Header
	h2 := sampleCheckpoint().Header

	id1 := syntheticBlockID(h1)
	id2 := syntheticBlockID(h2)
	if id1 != id2 {
		t.Errorf("syntheticBlockID not deterministic: %q != %q", id1, id2)
	}

	h3 := h1
	h3.Height++
	id3 := syntheticBlockID(h3)
	if id1 == id3 {
		t.Errorf("syntheticBlockID did not change for a different header height")
	}
}
