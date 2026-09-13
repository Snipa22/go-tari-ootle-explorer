package burnclaim

import (
	"context"
	"fmt"
	"testing"

	"github.com/Snipa22/go-tari-ootle-explorer/internal/db"
	"github.com/Snipa22/go-tari-ootle-explorer/internal/indexerclient"
)

func TestTombstoneSubstateID(t *testing.T) {
	got := tombstoneSubstateID("aabbcc")
	want := "tombstone_aabbcc"
	if got != want {
		t.Errorf("tombstoneSubstateID(%q) = %q, want %q", "aabbcc", got, want)
	}
}

// fakeIndexerClient is a hand-built fake for Tracker's own tests -
// internal/indexerclient's own client_test.go already exercises the real HTTP client
// against fixtures (including this dispatch's real GetSubstate fixtures), so this
// isolates CheckClaims's logic from HTTP entirely.
type fakeIndexerClient struct {
	// claimed maps a substate id to a response for substate ids considered
	// "claimed" (i.e. the tombstone exists).
	claimed map[string]*indexerclient.GetSubstateResponse
	// err lets a specific substate id return an arbitrary (non-404) error, for
	// testing the "no error) don't clobber the rest of the pass" path.
	err map[string]error
}

func (f *fakeIndexerClient) GetSubstate(_ context.Context, substateID string) (*indexerclient.GetSubstateResponse, error) {
	if err, ok := f.err[substateID]; ok {
		return nil, err
	}
	if resp, ok := f.claimed[substateID]; ok {
		return resp, nil
	}
	return nil, &indexerclient.StatusError{Op: "GetSubstate", StatusCode: 404, Body: fmt.Sprintf(`{"error":"Substate %s not found"}`, substateID)}
}

func TestCheckClaims_MarksClaimedWhenSubstateExists(t *testing.T) {
	store := newFakeStore()
	store.pending = []db.BurnClaim{
		{L1BurnTxHash: "tx1", Commitment: "aa", BurnHeight: 1, Status: "pending"},
		{L1BurnTxHash: "tx2", Commitment: "bb", BurnHeight: 2, Status: "pending"},
	}
	indexer := &fakeIndexerClient{
		claimed: map[string]*indexerclient.GetSubstateResponse{
			"tombstone_aa": {Version: 0, Verified: true, Substate: indexerclient.SubstateValue{
				ClaimedOutputTombstone: &indexerclient.ClaimedOutputTombstoneSubstate{Value: 1000},
			}},
		},
	}
	tr := New(nil, indexer, store, 100, 1000)

	checked, claimed, err := tr.CheckClaims(context.Background())
	if err != nil {
		t.Fatalf("CheckClaims: %v", err)
	}
	if checked != 2 {
		t.Errorf("checked = %d, want 2", checked)
	}
	if claimed != 1 {
		t.Errorf("claimed = %d, want 1", claimed)
	}
	if !store.claimed["aa"] {
		t.Errorf("commitment aa should be marked claimed")
	}
	if store.claimed["bb"] {
		t.Errorf("commitment bb should NOT be marked claimed (still 404)")
	}
}

func TestCheckClaims_404IsNotAnError(t *testing.T) {
	store := newFakeStore()
	store.pending = []db.BurnClaim{{L1BurnTxHash: "tx1", Commitment: "aa", BurnHeight: 1, Status: "pending"}}
	indexer := &fakeIndexerClient{}
	tr := New(nil, indexer, store, 100, 1000)

	checked, claimed, err := tr.CheckClaims(context.Background())
	if err != nil {
		t.Fatalf("CheckClaims: %v (a 404 must not surface as a pass-level error)", err)
	}
	if checked != 1 || claimed != 0 {
		t.Errorf("checked=%d claimed=%d, want 1,0", checked, claimed)
	}
}

func TestCheckClaims_OtherErrorsDontStopThePass(t *testing.T) {
	store := newFakeStore()
	store.pending = []db.BurnClaim{
		{L1BurnTxHash: "tx1", Commitment: "aa", BurnHeight: 1, Status: "pending"},
		{L1BurnTxHash: "tx2", Commitment: "bb", BurnHeight: 2, Status: "pending"},
	}
	indexer := &fakeIndexerClient{
		err: map[string]error{"tombstone_aa": fmt.Errorf("boom: network exploded")},
		claimed: map[string]*indexerclient.GetSubstateResponse{
			"tombstone_bb": {Version: 0, Verified: true},
		},
	}
	tr := New(nil, indexer, store, 100, 1000)

	checked, claimed, err := tr.CheckClaims(context.Background())
	if err != nil {
		t.Fatalf("CheckClaims: %v", err)
	}
	if checked != 2 {
		t.Errorf("checked = %d, want 2", checked)
	}
	if claimed != 1 {
		t.Errorf("claimed = %d, want 1 (the second row should still be processed despite the first erroring)", claimed)
	}
	if !store.claimed["bb"] {
		t.Errorf("commitment bb should be marked claimed despite aa's error")
	}
}

func TestCheckClaims_EmptyPendingList(t *testing.T) {
	store := newFakeStore()
	tr := New(nil, &fakeIndexerClient{}, store, 100, 1000)

	checked, claimed, err := tr.CheckClaims(context.Background())
	if err != nil {
		t.Fatalf("CheckClaims: %v", err)
	}
	if checked != 0 || claimed != 0 {
		t.Errorf("checked=%d claimed=%d, want 0,0", checked, claimed)
	}
}
