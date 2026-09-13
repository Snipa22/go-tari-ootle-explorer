package burnclaim

import (
	"context"
	"testing"

	"github.com/Snipa22/go-tari-ootle-explorer/internal/db"
)

func TestStuckThreshold(t *testing.T) {
	if got := StuckThreshold(100, 1000); got != 1100 {
		t.Errorf("StuckThreshold(100, 1000) = %d, want 1100", got)
	}
	if got := StuckThreshold(780, 0); got != 780 {
		t.Errorf("StuckThreshold(780, 0) = %d, want 780", got)
	}
}

func TestIsStuck(t *testing.T) {
	tests := []struct {
		name                       string
		burnHeight, tipHeight, thr uint64
		want                       bool
	}{
		{"well within window", 1000, 1050, 1100, false},
		{"exactly at threshold - not yet stuck", 1000, 2100, 1100, false},
		{"one block past threshold - stuck", 1000, 2101, 1100, true},
		{"tip behind burn height - never stuck", 2000, 1000, 1100, false},
		{"tip equals burn height - never stuck", 1000, 1000, 1100, false},
		{"esmeralda real confirmation lag, fresh burn", 800000, 800050, 1100, false},
		{"esmeralda real confirmation lag, long overdue", 800000, 801200, 1100, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := IsStuck(tc.burnHeight, tc.tipHeight, tc.thr)
			if got != tc.want {
				t.Errorf("IsStuck(%d, %d, %d) = %v, want %v", tc.burnHeight, tc.tipHeight, tc.thr, got, tc.want)
			}
		})
	}
}

func TestKnownBaseLayerConfirmations_RealNetworkValues(t *testing.T) {
	// Confirmed against crates/consensus/src/consensus_constants.rs directly - see
	// consensus_constants.go's doc comment for the exact citation. This test pins
	// those real values so an accidental future edit to the map is caught.
	want := map[string]uint64{
		"mainnet":   780,
		"esmeralda": 100,
		"testnet":   100,
		"stagenet":  100,
		"nextnet":   100,
		"igor":      100,
		"localnet":  3,
		"devnet":    3,
	}
	for network, expected := range want {
		got, ok := KnownBaseLayerConfirmations[network]
		if !ok {
			t.Errorf("KnownBaseLayerConfirmations[%q] missing", network)
			continue
		}
		if got != expected {
			t.Errorf("KnownBaseLayerConfirmations[%q] = %d, want %d", network, got, expected)
		}
	}
}

func TestDetectStuck_MarksOnlyOverdueRows(t *testing.T) {
	store := newFakeStore()
	store.pending = []db.BurnClaim{
		{L1BurnTxHash: "fresh", Commitment: "aa", BurnHeight: 100000, Status: "pending"},
		{L1BurnTxHash: "overdue", Commitment: "bb", BurnHeight: 1000, Status: "pending"},
	}
	tr := New(nil, nil, store, 100, 1000) // threshold = 1100

	marked, err := tr.DetectStuck(context.Background(), 100050) // tip height
	if err != nil {
		t.Fatalf("DetectStuck: %v", err)
	}
	if marked != 1 {
		t.Fatalf("marked = %d, want 1", marked)
	}
	if store.stuck["fresh"] {
		t.Errorf("fresh burn (age 50) must not be marked stuck")
	}
	if !store.stuck["overdue"] {
		t.Errorf("overdue burn (age 99050, well past threshold 1100) must be marked stuck")
	}
}

func TestDetectStuck_NeverTouchesClaimedRows(t *testing.T) {
	store := newFakeStore()
	store.pending = []db.BurnClaim{
		{L1BurnTxHash: "claimed-late", Commitment: "cc", BurnHeight: 1000, Status: "pending"},
	}
	store.claimed["cc"] = true // already claimed, per fakeStore's status derivation this is no longer 'pending'
	tr := New(nil, nil, store, 100, 1000)

	marked, err := tr.DetectStuck(context.Background(), 100000)
	if err != nil {
		t.Fatalf("DetectStuck: %v", err)
	}
	if marked != 0 {
		t.Errorf("marked = %d, want 0 (an already-claimed row must never be marked stuck)", marked)
	}
}

func TestDetectStuck_EmptyPendingList(t *testing.T) {
	store := newFakeStore()
	tr := New(nil, nil, store, 100, 1000)

	marked, err := tr.DetectStuck(context.Background(), 12345)
	if err != nil {
		t.Fatalf("DetectStuck: %v", err)
	}
	if marked != 0 {
		t.Errorf("marked = %d, want 0", marked)
	}
}
