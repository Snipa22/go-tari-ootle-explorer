package registry

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Snipa22/go-tari-ootle-explorer/internal/db"
	"github.com/Snipa22/go-tari-ootle-explorer/internal/registrymetaclient"
)

// fakeMetadataClient is an in-memory MetadataClient double - no real network
// involved, consistent with this package's CI-safe unit coverage requirement (the
// real live-fetch verification lives in registry_live_test.go instead).
type fakeMetadataClient struct {
	entries []registrymetaclient.FeaturedTemplate
	err     error

	listFeaturedCalls int
}

func (f *fakeMetadataClient) ListFeatured(ctx context.Context) ([]registrymetaclient.FeaturedTemplate, error) {
	f.listFeaturedCalls++
	if f.err != nil {
		return nil, f.err
	}
	return f.entries, nil
}

// fakeStore is an in-memory Store double, recording every upsert for assertions.
type fakeStore struct {
	upserts          []db.TemplateRegistryMetadataEntry
	upsertErrFor     map[string]error // template_address -> error to return for that upsert
	defaultUpsertErr error
}

func newFakeStore() *fakeStore {
	return &fakeStore{upsertErrFor: make(map[string]error)}
}

func (f *fakeStore) UpsertTemplateRegistryMetadata(ctx context.Context, t db.TemplateRegistryMetadataEntry) error {
	if err, ok := f.upsertErrFor[t.TemplateAddress]; ok {
		return err
	}
	if f.defaultUpsertErr != nil {
		return f.defaultUpsertErr
	}
	f.upserts = append(f.upserts, t)
	return nil
}

func (f *fakeStore) find(templateAddress string) (db.TemplateRegistryMetadataEntry, bool) {
	for _, u := range f.upserts {
		if u.TemplateAddress == templateAddress {
			return u, true
		}
	}
	return db.TemplateRegistryMetadataEntry{}, false
}

func strPtr(s string) *string { return &s }

// TestSync_UpsertsEveryFeaturedEntry covers the "everything succeeds" path against a
// realistic, live-shaped fixture: three entries, one with a real non-null metadata
// blob and the others with every optional field null - confirms every entry is
// upserted and the toRow conversion (in particular the json.RawMessage
// null-normalization - see toRow's doc comment) behaves correctly end to end.
func TestSync_UpsertsEveryFeaturedEntry(t *testing.T) {
	client := &fakeMetadataClient{
		entries: []registrymetaclient.FeaturedTemplate{
			{
				TemplateAddress: "aa",
				TemplateName:    "Account",
				AuthorPublicKey: "00",
				BinaryHash:      "bh-aa",
				AtEpoch:         0,
				CodeSize:        510199,
				IsFeatured:      true,
				Metadata:        []byte("null"),
				Definition:      []byte("null"),
			},
			{
				TemplateAddress:    "bb",
				TemplateName:       "TariStableCoin",
				AuthorPublicKey:    "3c",
				AuthorFriendlyName: strPtr("Some Author"),
				BinaryHash:         "bh-bb",
				AtEpoch:            9013,
				CodeSize:           157535,
				IsFeatured:         true,
				Metadata:           []byte(`{"category":"token","tags":["token","fungible"]}`),
				Definition:         []byte("null"),
			},
		},
	}
	store := newFakeStore()
	r := New(client, store)

	n, err := r.Sync(context.Background())
	if err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	if n != 2 {
		t.Errorf("Sync() = %d, want 2", n)
	}
	if len(store.upserts) != 2 {
		t.Fatalf("len(upserts) = %d, want 2", len(store.upserts))
	}

	account, ok := store.find("aa")
	if !ok {
		t.Fatalf("expected an upsert for template_address=aa")
	}
	if account.TemplateName == nil || *account.TemplateName != "Account" {
		t.Errorf("account.TemplateName = %v, want Account", account.TemplateName)
	}
	if account.CodeSize == nil || *account.CodeSize != 510199 {
		t.Errorf("account.CodeSize = %v, want 510199", account.CodeSize)
	}
	if account.IsFeatured == nil || !*account.IsFeatured {
		t.Errorf("account.IsFeatured = %v, want true", account.IsFeatured)
	}
	// The literal JSON "null" must be normalized to a genuine nil []byte, not stored
	// as the 4-byte string "null" - see toRow's doc comment.
	if account.Metadata != nil {
		t.Errorf("account.Metadata = %q, want nil (JSON null normalized)", account.Metadata)
	}
	if account.Definition != nil {
		t.Errorf("account.Definition = %q, want nil (JSON null normalized)", account.Definition)
	}
	if account.AuthorFriendlyName != nil {
		t.Errorf("account.AuthorFriendlyName = %v, want nil", account.AuthorFriendlyName)
	}

	stableCoin, ok := store.find("bb")
	if !ok {
		t.Fatalf("expected an upsert for template_address=bb")
	}
	if stableCoin.AuthorFriendlyName == nil || *stableCoin.AuthorFriendlyName != "Some Author" {
		t.Errorf("stableCoin.AuthorFriendlyName = %v, want \"Some Author\"", stableCoin.AuthorFriendlyName)
	}
	if stableCoin.AtEpoch == nil || *stableCoin.AtEpoch != 9013 {
		t.Errorf("stableCoin.AtEpoch = %v, want 9013", stableCoin.AtEpoch)
	}
	if string(stableCoin.Metadata) != `{"category":"token","tags":["token","fungible"]}` {
		t.Errorf("stableCoin.Metadata = %q, want the real non-null JSON blob", stableCoin.Metadata)
	}
	if stableCoin.Definition != nil {
		t.Errorf("stableCoin.Definition = %q, want nil (JSON null normalized)", stableCoin.Definition)
	}
}

// TestSync_ClientErrorPropagates confirms a MetadataClient failure surfaces via
// Sync's returned error, without a partial/silent upsert.
func TestSync_ClientErrorPropagates(t *testing.T) {
	wantErr := errors.New("dial tcp: connection refused")
	client := &fakeMetadataClient{err: wantErr}
	store := newFakeStore()
	r := New(client, store)

	n, err := r.Sync(context.Background())
	if err == nil || !errors.Is(err, wantErr) {
		t.Fatalf("Sync() error = %v, want wrapping %v", err, wantErr)
	}
	if n != 0 {
		t.Errorf("Sync() = %d, want 0", n)
	}
	if len(store.upserts) != 0 {
		t.Errorf("len(upserts) = %d, want 0", len(store.upserts))
	}
}

// TestSync_StoreErrorPropagates confirms a Store failure (e.g. a real DB error)
// surfaces via Sync's returned error rather than being silently swallowed, and stops
// processing further entries (matching internal/explorer's own "fail loud on a
// write error" convention).
func TestSync_StoreErrorPropagates(t *testing.T) {
	wantErr := errors.New("db boom")
	client := &fakeMetadataClient{
		entries: []registrymetaclient.FeaturedTemplate{
			{TemplateAddress: "aa", TemplateName: "Account", AuthorPublicKey: "00", BinaryHash: "bh"},
		},
	}
	store := newFakeStore()
	store.defaultUpsertErr = wantErr
	r := New(client, store)

	_, err := r.Sync(context.Background())
	if err == nil || !errors.Is(err, wantErr) {
		t.Fatalf("Sync() error = %v, want wrapping %v", err, wantErr)
	}
}

// TestSync_EmptyList_NoOpNoError confirms an empty featured list (a valid response -
// no templates currently featured) is a clean no-op, not an error.
func TestSync_EmptyList_NoOpNoError(t *testing.T) {
	client := &fakeMetadataClient{entries: nil}
	store := newFakeStore()
	r := New(client, store)

	n, err := r.Sync(context.Background())
	if err != nil {
		t.Fatalf("Sync() error = %v, want nil", err)
	}
	if n != 0 {
		t.Errorf("Sync() = %d, want 0", n)
	}
	if len(store.upserts) != 0 {
		t.Errorf("len(upserts) = %d, want 0", len(store.upserts))
	}
}

// TestFollow_PollsUntilContextCancelled confirms Follow ticks at least twice within a
// bounded window and then returns cleanly once ctx is cancelled - same shape as
// internal/explorer/internal/vnhealth's own TestFollow_PollsUntilContextCancelled.
func TestFollow_PollsUntilContextCancelled(t *testing.T) {
	client := &fakeMetadataClient{entries: []registrymetaclient.FeaturedTemplate{
		{TemplateAddress: "aa", TemplateName: "Account", AuthorPublicKey: "00", BinaryHash: "bh"},
	}}
	store := newFakeStore()
	r := New(client, store)

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()

	err := r.Follow(ctx, 5*time.Millisecond)
	if err == nil || ctx.Err() == nil {
		t.Fatalf("Follow() error = %v, ctx.Err() = %v, want a context-cancellation error", err, ctx.Err())
	}
	if client.listFeaturedCalls < 2 {
		t.Errorf("listFeaturedCalls = %d, want >= 2", client.listFeaturedCalls)
	}
}

// TestFollow_SyncErrorDoesNotAbortLoop confirms a single failed Sync (e.g. a
// transient metadata-server outage) is logged and retried on the next tick, rather
// than aborting Follow entirely - matching internal/explorer/internal/vnhealth's own
// Follow convention.
func TestFollow_SyncErrorDoesNotAbortLoop(t *testing.T) {
	client := &fakeMetadataClient{err: errors.New("boom")}
	store := newFakeStore()
	r := New(client, store)

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()

	err := r.Follow(ctx, 5*time.Millisecond)
	if err == nil || ctx.Err() == nil {
		t.Fatalf("Follow() error = %v, ctx.Err() = %v, want a context-cancellation error", err, ctx.Err())
	}
	if client.listFeaturedCalls < 2 {
		t.Errorf("listFeaturedCalls = %d, want >= 2 (a failed poll must not stop Follow from retrying)", client.listFeaturedCalls)
	}
}
