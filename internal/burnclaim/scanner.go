package burnclaim

import (
	"context"
	"fmt"

	"github.com/Snipa22/go-tari-grpc-lib/v3/tari_generated"
	"github.com/Snipa22/go-tari-ootle-explorer/internal/db"
)

// scanRange walks L1 blocks [fromHeight, toHeight] (inclusive) in ScanBatchSize-sized
// GetBlockByHeight calls, inserting every burn kernel found as a new 'pending'
// burn_claims row. Returns the number of burns found (including ones already
// recorded from an earlier overlapping scan - InsertPendingBurnClaim's ON CONFLICT DO
// NOTHING makes re-insertion a safe no-op, but this count still reflects every burn
// KERNEL seen in this call's range, not just newly-inserted ones - see this
// package's doc comment on why re-scanning overlapping ranges is expected).
func (t *Tracker) scanRange(ctx context.Context, fromHeight, toHeight uint64) (int, error) {
	if fromHeight > toHeight {
		return 0, nil
	}

	found := 0
	for start := fromHeight; start <= toHeight; start += ScanBatchSize {
		end := start + ScanBatchSize - 1
		if end > toHeight {
			end = toHeight
		}
		heights := make([]uint64, 0, end-start+1)
		for h := start; h <= end; h++ {
			heights = append(heights, h)
		}

		blocks, err := t.L1.GetBlockByHeight(ctx, heights)
		if err != nil {
			return found, fmt.Errorf("burnclaim: scan range: get blocks [%d,%d]: %w", start, end, err)
		}

		for _, blk := range blocks {
			burns := burnsInBlock(blk)
			for _, b := range burns {
				if err := t.Store.InsertPendingBurnClaim(ctx, b); err != nil {
					return found, fmt.Errorf("burnclaim: scan range: insert pending burn %s: %w", b.L1BurnTxHash, err)
				}
				found++
			}
		}
	}
	return found, nil
}

// burnsInBlock extracts every burn from a single L1 block, scanning KERNELS ONLY
// (not outputs) for a non-empty BurnCommitment - the real, consensus-checked marker
// for a burn (TransactionKernel.is_burned()/get_burn_commitment() in the Rust source;
// TransactionKernel.burn_commitment on the wire, tari_protos/transaction.proto).
//
// This is deliberately NOT cross-referenced against OutputType::BURN outputs in the
// same block, even though every real burn has both: L1's own consensus validation
// (base_layer/core/src/validation/aggregate_body/aggregate_body_chain_validator.rs's
// check_total_burned, read directly during this dispatch) REQUIRES a burn kernel's
// burn_commitment to exactly equal its matching burned output's commitment, in BOTH
// directions ("Burned kernel does not match burned output" / "Burned output has no
// matching burned kernel" are both hard validation errors) - so scanning kernels
// alone already gives us everything a real, validly-mined burn's matching output
// would tell us (specifically: the same commitment), with no missed or
// double-counted cases and half the field-matching logic.
func burnsInBlock(blk *tari_generated.Block) []db.BurnClaim {
	if blk == nil || blk.Header == nil || blk.Body == nil {
		return nil
	}
	height := blk.Header.Height

	var out []db.BurnClaim
	for _, k := range blk.Body.Kernels {
		if k == nil || len(k.BurnCommitment) == 0 {
			continue
		}
		if len(k.Hash) == 0 {
			// Defensive: every real kernel GetBlocks returns carries a non-empty
			// hash (it's a stored field, not computed on demand) - this should
			// never actually happen, but l1_burn_tx_hash is this table's PRIMARY
			// KEY, so silently keying a row on an empty string would be far worse
			// than dropping one malformed-looking kernel and moving on.
			continue
		}
		out = append(out, db.BurnClaim{
			L1BurnTxHash: kernelHashHex(k.Hash),
			Commitment:   commitmentHex(k.BurnCommitment),
			BurnHeight:   height,
			Status:       "pending",
		})
	}
	return out
}
