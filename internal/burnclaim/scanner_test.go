package burnclaim

import (
	"context"
	"testing"
	"time"

	"github.com/Snipa22/go-tari-grpc-lib/v3/tari_generated"
	"github.com/Snipa22/go-tari-ootle-explorer/internal/db"
)

func TestBurnsInBlock_FindsBurnKernel(t *testing.T) {
	blk := &tari_generated.Block{
		Header: &tari_generated.BlockHeader{Height: 42},
		Body: &tari_generated.AggregateBody{
			Kernels: []*tari_generated.TransactionKernel{
				{Hash: []byte{0xaa, 0xbb}, BurnCommitment: []byte{0x01, 0x02, 0x03}},
				{Hash: []byte{0xcc, 0xdd}}, // ordinary kernel, no burn
			},
		},
	}

	got := burnsInBlock(blk)
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1", len(got))
	}
	if got[0].L1BurnTxHash != "aabb" {
		t.Errorf("L1BurnTxHash = %q, want aabb", got[0].L1BurnTxHash)
	}
	if got[0].Commitment != "010203" {
		t.Errorf("Commitment = %q, want 010203", got[0].Commitment)
	}
	if got[0].BurnHeight != 42 {
		t.Errorf("BurnHeight = %d, want 42", got[0].BurnHeight)
	}
	if got[0].Status != "pending" {
		t.Errorf("Status = %q, want pending", got[0].Status)
	}
}

func TestBurnsInBlock_NoBurns(t *testing.T) {
	blk := &tari_generated.Block{
		Header: &tari_generated.BlockHeader{Height: 1},
		Body: &tari_generated.AggregateBody{
			Kernels: []*tari_generated.TransactionKernel{
				{Hash: []byte{0x01}},
				{Hash: []byte{0x02}},
			},
		},
	}
	if got := burnsInBlock(blk); len(got) != 0 {
		t.Fatalf("len(got) = %d, want 0", len(got))
	}
}

func TestBurnsInBlock_NilSafety(t *testing.T) {
	if got := burnsInBlock(nil); got != nil {
		t.Fatalf("burnsInBlock(nil) = %v, want nil", got)
	}
	if got := burnsInBlock(&tari_generated.Block{}); got != nil {
		t.Fatalf("burnsInBlock(empty) = %v, want nil", got)
	}
}

func TestBurnsInBlock_SkipsEmptyKernelHash(t *testing.T) {
	blk := &tari_generated.Block{
		Header: &tari_generated.BlockHeader{Height: 5},
		Body: &tari_generated.AggregateBody{
			Kernels: []*tari_generated.TransactionKernel{
				{Hash: nil, BurnCommitment: []byte{0x01}},
			},
		},
	}
	if got := burnsInBlock(blk); len(got) != 0 {
		t.Fatalf("len(got) = %d, want 0 (empty kernel hash must never become a primary key)", len(got))
	}
}

// fakeL1Client is a hand-built (not bufconn-real) fake for Tracker's own tests -
// l1client_test.go already exercises the real GRPC wire path against GRPCClient, so
// Tracker's tests can use a plain interface fake to isolate scan/pagination logic.
type fakeL1Client struct {
	tip    *tari_generated.TipInfoResponse
	blocks map[uint64]*tari_generated.Block
	calls  [][]uint64 // records every GetBlockByHeight call's height list, for pagination assertions
}

func (f *fakeL1Client) GetTipInfo(_ context.Context) (*tari_generated.TipInfoResponse, error) {
	return f.tip, nil
}

func (f *fakeL1Client) GetBlockByHeight(_ context.Context, heights []uint64) ([]*tari_generated.Block, error) {
	f.calls = append(f.calls, heights)
	var out []*tari_generated.Block
	for _, h := range heights {
		if blk, ok := f.blocks[h]; ok {
			out = append(out, blk)
		}
	}
	return out, nil
}

// fakeStore is a hand-built fake db.Store for Tracker's own tests.
type fakeStore struct {
	pending []db.BurnClaim
	claimed map[string]bool // by commitment
	stuck   map[string]bool // by l1_burn_tx_hash
}

func newFakeStore() *fakeStore {
	return &fakeStore{claimed: map[string]bool{}, stuck: map[string]bool{}}
}

func (s *fakeStore) InsertPendingBurnClaim(_ context.Context, b db.BurnClaim) error {
	for _, existing := range s.pending {
		if existing.L1BurnTxHash == b.L1BurnTxHash {
			return nil // ON CONFLICT DO NOTHING, same semantics as the real store
		}
	}
	s.pending = append(s.pending, b)
	return nil
}

func (s *fakeStore) ListBurnClaimsByStatus(_ context.Context, status string) ([]db.BurnClaim, error) {
	var out []db.BurnClaim
	for _, b := range s.pending {
		cur := "pending"
		if s.claimed[b.Commitment] {
			cur = "claimed"
		} else if s.stuck[b.L1BurnTxHash] {
			cur = "stuck"
		}
		if cur == status {
			out = append(out, b)
		}
	}
	return out, nil
}

func (s *fakeStore) MarkBurnClaimed(_ context.Context, commitment string, _ time.Time) error {
	s.claimed[commitment] = true
	return nil
}

func (s *fakeStore) MarkBurnStuck(_ context.Context, l1BurnTxHash string) error {
	s.stuck[l1BurnTxHash] = true
	return nil
}

func TestScanRange_InsertsBurnsAndPaginates(t *testing.T) {
	blocks := map[uint64]*tari_generated.Block{}
	for h := uint64(1); h <= 250; h++ {
		blocks[h] = &tari_generated.Block{
			Header: &tari_generated.BlockHeader{Height: h},
			Body:   &tari_generated.AggregateBody{},
		}
	}
	// Plant a real burn kernel at height 150.
	blocks[150].Body.Kernels = []*tari_generated.TransactionKernel{
		{Hash: []byte{0x11}, BurnCommitment: []byte{0x22}},
	}

	l1 := &fakeL1Client{blocks: blocks}
	store := newFakeStore()
	tr := New(l1, nil, store, 100, 1000)

	found, err := tr.scanRange(context.Background(), 1, 250)
	if err != nil {
		t.Fatalf("scanRange: %v", err)
	}
	if found != 1 {
		t.Fatalf("found = %d, want 1", found)
	}
	if len(store.pending) != 1 {
		t.Fatalf("len(store.pending) = %d, want 1", len(store.pending))
	}
	if store.pending[0].BurnHeight != 150 {
		t.Errorf("BurnHeight = %d, want 150", store.pending[0].BurnHeight)
	}

	// 250 heights at ScanBatchSize=100 should be 3 calls: [1..100], [101..200], [201..250].
	if len(l1.calls) != 3 {
		t.Fatalf("len(l1.calls) = %d, want 3", len(l1.calls))
	}
	if len(l1.calls[0]) != 100 || len(l1.calls[1]) != 100 || len(l1.calls[2]) != 50 {
		t.Errorf("call sizes = %d,%d,%d, want 100,100,50", len(l1.calls[0]), len(l1.calls[1]), len(l1.calls[2]))
	}
}

func TestScanRange_Idempotent(t *testing.T) {
	blocks := map[uint64]*tari_generated.Block{
		5: {
			Header: &tari_generated.BlockHeader{Height: 5},
			Body: &tari_generated.AggregateBody{
				Kernels: []*tari_generated.TransactionKernel{{Hash: []byte{0x01}, BurnCommitment: []byte{0x02}}},
			},
		},
	}
	l1 := &fakeL1Client{blocks: blocks}
	store := newFakeStore()
	tr := New(l1, nil, store, 100, 1000)

	if _, err := tr.scanRange(context.Background(), 5, 5); err != nil {
		t.Fatalf("first scanRange: %v", err)
	}
	if _, err := tr.scanRange(context.Background(), 5, 5); err != nil {
		t.Fatalf("second scanRange (re-scan): %v", err)
	}
	if len(store.pending) != 1 {
		t.Fatalf("len(store.pending) = %d, want 1 (re-scanning the same burn must not duplicate it)", len(store.pending))
	}
}

func TestScanRange_EmptyRange(t *testing.T) {
	l1 := &fakeL1Client{blocks: map[uint64]*tari_generated.Block{}}
	store := newFakeStore()
	tr := New(l1, nil, store, 100, 1000)

	found, err := tr.scanRange(context.Background(), 10, 5) // from > to
	if err != nil {
		t.Fatalf("scanRange: %v", err)
	}
	if found != 0 {
		t.Fatalf("found = %d, want 0", found)
	}
	if len(l1.calls) != 0 {
		t.Fatalf("expected no GetBlockByHeight calls for an empty range, got %d", len(l1.calls))
	}
}
