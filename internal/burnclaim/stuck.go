package burnclaim

import (
	"context"
	"fmt"
	"log"
)

// DetectStuck checks every 'pending' burn_claims row's age against tipHeight and
// this Tracker's configured StuckThreshold(ConfirmationLag, ExtraStuckBlocks),
// marking any that exceed it 'stuck'. Returns the number of rows newly marked.
//
// Deliberately re-lists 'pending' rows independently of CheckClaims (rather than
// sharing one list across both) - a fresh list here means a burn CheckClaims just
// marked 'claimed' in the same pass is correctly excluded from stuck-detection,
// avoiding a race where a burn that took just long enough to be flagged stuck is
// then, in the same tick, ALSO discovered to have just been claimed - MarkBurnStuck
// only ever transitions 'pending' -> 'stuck' (never touches an already-'claimed'
// row - see db.MarkBurnStuck's own doc comment), so this ordering (CheckClaims
// first, DetectStuck second, both against v1's Scan/Follow call sites) means a claim
// that lands in the same tick as it would've gone stuck is preferred over marking it
// stuck.
func (t *Tracker) DetectStuck(ctx context.Context, tipHeight uint64) (int, error) {
	pending, err := t.Store.ListBurnClaimsByStatus(ctx, "pending")
	if err != nil {
		return 0, fmt.Errorf("burnclaim: detect stuck: list pending: %w", err)
	}

	threshold := StuckThreshold(t.ConfirmationLag, t.ExtraStuckBlocks)
	marked := 0
	for _, b := range pending {
		if !IsStuck(b.BurnHeight, tipHeight, threshold) {
			continue
		}
		if err := t.Store.MarkBurnStuck(ctx, b.L1BurnTxHash); err != nil {
			log.Printf("burnclaim: detect stuck: mark stuck %s: %v", b.L1BurnTxHash, err)
			continue
		}
		marked++
	}
	return marked, nil
}
