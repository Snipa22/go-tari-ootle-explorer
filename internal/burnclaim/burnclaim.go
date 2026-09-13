// Package burnclaim tracks the L1-Minotari-burn -> L2-Ootle-claim lifecycle (see
// AGENTS.md's build order step 5 and DISPATCH_BRIEF.md) for v1's `burn_claims` table:
//
//   - L1 side (this package's GRPCClient + Tracker.Scan/scanBurnsInBlock): walks L1
//     base-node blocks via go-tari-grpc-lib looking for burn kernels
//     (TransactionKernel.burn_commitment set - the real, consensus-checked marker for
//     a burn, per claim-burn.mdx and aggregate_body_chain_validator.rs's
//     check_total_burned/validate_burn_commitment_not_in_db, both read directly
//     during this dispatch), recording each as a new 'pending' burn_claims row.
//   - L2 side (Tracker.CheckClaims): for every 'pending' row, derives the real L2
//     ClaimedOutputTombstoneAddress substate id from the burn's commitment
//     ("tombstone_" + hex(commitment) - confirmed against
//     crates/template_lib_types/src/substates/claimed_output_tombstone.rs) and asks
//     the indexer (GET /substates/{substate_id}) whether it exists yet; if so, the
//     burn has been claimed.
//   - Stuck-detection (Tracker.DetectStuck): flags a still-'pending' row 'stuck' once
//     it's older than its network's REAL base_layer_confirmations lag (see
//     consensus_constants.go) plus a configurable extra margin - NOT the instant it
//     has no claim, since claim-burn.mdx is explicit that a burn genuinely cannot be
//     claimed until validators have scanned its block, lagged by that same real
//     constant.
//
// # What is and isn't extractable from a plain L1 block scan
//
// claim_public_key (the doc's "stealth claim public key S"/"burn_public_key") is
// NOT populated by this package's L1 scanner - see
// migrations/0003_burn_claims_correction.up.sql's comment for the real source
// citation (it's a wallet-daemon-derived value via GetBurnClaimProofRequest, not a
// field on the plain TransactionOutput/TransactionKernel a base-node block scan
// sees). This is a genuine, documented v1 scope gap, not an oversight - flagged
// explicitly here and in the dispatch report rather than silently left unmentioned.
//
// claim_tx_id is never populated either: the tombstone substate this package's L2
// checker queries carries only a `value: u64` field (crates/engine_types/src/
// confidential/unclaimed.rs) - no reference back to the claiming transaction's id at
// all. claimed_at is therefore this repo's own OBSERVATION time (when CheckClaims
// first saw the tombstone exist), not a real upstream claim timestamp - same
// "flag the gap, don't fake it" pattern internal/explorer's OotleBlock.Timestamp
// caveat already established for this repo.
//
// # Live verification status (see the dispatch report for full detail)
//
// L1: LIVE-verified against a real, reachable public endpoint
// (grpc.esmeralda.tari.com:443, over TLS - plain insecure credentials do NOT work
// against this host, it is TLS-terminated) - GetTipInfo and GetBlockByHeight were
// both run against real esmeralda chain data during this dispatch. Zero real burns
// were found in the height ranges scanned (see the dispatch report for exactly which
// ranges) - this package's burn-detection logic itself is therefore verified by
// mock-based tests (l1client_test.go's bufconn fake server), not by observing a real
// burn end to end.
//
// L2: LIVE-verified against the real public indexer
// (https://ootle-indexer-a.tari.com) for BOTH the "not found" 404 path (a
// syntactically valid but never-created tombstone address) and the general 200
// response SHAPE (a different, real substate - STEALTH_TARI_RESOURCE_ADDRESS - since
// no real ClaimedOutputTombstoneAddress substate was found to exist on this network
// during this dispatch either). The ClaimedOutputTombstone variant's own decode path
// is covered by a synthetic (real-Rust-shape-derived, not live-captured) fixture -
// see internal/indexerclient's TestGetSubstate_DecodesClaimedOutputTombstone.
package burnclaim

import (
	"context"
	"encoding/hex"
	"fmt"
	"log"
	"time"

	"github.com/Snipa22/go-tari-grpc-lib/v3/tari_generated"
	"github.com/Snipa22/go-tari-ootle-explorer/internal/db"
	"github.com/Snipa22/go-tari-ootle-explorer/internal/indexerclient"
)

// ScanBatchSize is the number of block heights requested per GetBlockByHeight call -
// matches go-tari-explorer/internal/nodeclient's own GetBlockByHeight batching
// rationale (bound the response size/round-trip count without needing a huge
// MaxCallRecvMsgSize for a single giant request).
const ScanBatchSize = 100

// L1Client is the subset of *GRPCClient's methods Tracker needs. *GRPCClient
// satisfies this interface as-is; tests supply a fake.
type L1Client interface {
	GetTipInfo(ctx context.Context) (*tari_generated.TipInfoResponse, error)
	GetBlockByHeight(ctx context.Context, heights []uint64) ([]*tari_generated.Block, error)
}

// IndexerClient is the subset of *indexerclient.Client's methods Tracker needs.
// *indexerclient.Client satisfies this interface as-is; tests supply a fake.
type IndexerClient interface {
	GetSubstate(ctx context.Context, substateID string) (*indexerclient.GetSubstateResponse, error)
}

// Store is the subset of *db.DB's methods Tracker needs to persist what it
// scans/checks. *db.DB satisfies this interface as-is; tests supply a fake.
type Store interface {
	InsertPendingBurnClaim(ctx context.Context, b db.BurnClaim) error
	ListBurnClaimsByStatus(ctx context.Context, status string) ([]db.BurnClaim, error)
	MarkBurnClaimed(ctx context.Context, commitment string, claimedAt time.Time) error
	MarkBurnStuck(ctx context.Context, l1BurnTxHash string) error
}

// Tracker bundles the dependencies and thresholds needed to scan L1 for burns, check
// L2 for claims, and detect stuck burns. Construct with New.
type Tracker struct {
	L1      L1Client
	Indexer IndexerClient
	Store   Store

	// ConfirmationLag is the network's REAL base_layer_confirmations consensus
	// constant (see consensus_constants.go's KnownBaseLayerConfirmations) - the
	// minimum number of L1 blocks a burn must age before it's even claimable.
	ConfirmationLag uint64
	// ExtraStuckBlocks is the configurable margin ON TOP of ConfirmationLag a
	// pending burn is allowed before DetectStuck flags it - see StuckThreshold.
	ExtraStuckBlocks uint64

	// lastScannedHeight is Follow's in-memory cursor: the highest L1 height Scan
	// has already covered. Deliberately not persisted across process restarts,
	// same "acceptable v1 tradeoff, every write is an idempotent upsert" rationale
	// as internal/explorer's own catalogueCursor - a restarted Follow simply
	// resumes from the current tip (see Follow's doc comment) rather than
	// replaying history it may have already covered, or missing a gap.
	lastScannedHeight uint64
}

// New constructs a Tracker.
func New(l1 L1Client, indexer IndexerClient, store Store, confirmationLag, extraStuckBlocks uint64) *Tracker {
	return &Tracker{
		L1:               l1,
		Indexer:          indexer,
		Store:            store,
		ConfirmationLag:  confirmationLag,
		ExtraStuckBlocks: extraStuckBlocks,
	}
}

// Scan performs one one-shot pass: walks L1 blocks [fromHeight, toHeight] (inclusive)
// for burns, inserting each as a new 'pending' row (idempotent - see
// db.InsertPendingBurnClaim's doc comment), then runs a single CheckClaims and
// DetectStuck pass over whatever is 'pending' afterward (including rows from earlier
// runs, not just this call's own range). Returns the number of NEW burns found in
// this call's range.
func (t *Tracker) Scan(ctx context.Context, fromHeight, toHeight uint64) (int, error) {
	found, err := t.scanRange(ctx, fromHeight, toHeight)
	if err != nil {
		return found, err
	}

	checked, claimed, err := t.CheckClaims(ctx)
	if err != nil {
		log.Printf("burnclaim: scan: check claims: %v", err)
	} else if checked > 0 {
		log.Printf("burnclaim: scan: checked %d pending burn(s), %d newly claimed", checked, claimed)
	}

	tip, err := t.L1.GetTipInfo(ctx)
	if err != nil {
		log.Printf("burnclaim: scan: get tip info for stuck-detection: %v", err)
	} else {
		tipHeight := tip.GetMetadata().GetBestBlockHeight()
		marked, err := t.DetectStuck(ctx, tipHeight)
		if err != nil {
			log.Printf("burnclaim: scan: detect stuck: %v", err)
		} else if marked > 0 {
			log.Printf("burnclaim: scan: marked %d burn(s) stuck (tip height %d)", marked, tipHeight)
		}
	}

	return found, nil
}

// Follow repeatedly scans new L1 blocks + checks pending claims + detects stuck
// burns, every pollInterval, until ctx is cancelled.
//
// On its FIRST iteration only, if no starting height was ever established (a fresh
// Tracker, or one constructed with startHeight 0), Follow begins scanning from
// tip-ScanBatchSize+1 rather than from genesis or an arbitrary default - i.e. it
// starts "following" from roughly now, not backfilling all of history. A real
// historical backfill needs an explicit Scan(ctx, fromHeight, toHeight) call with a
// deliberately chosen range instead (see cmd/burnclaim's -mode=scan) - Follow is the
// ongoing-health-tracking mode, not a substitute for one.
func (t *Tracker) Follow(ctx context.Context, pollInterval time.Duration, startHeight uint64) error {
	t.lastScannedHeight = startHeight

	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		if err := t.followTick(ctx); err != nil {
			log.Printf("burnclaim: follow: tick: %v (will retry)", err)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}

// followTick runs a single Follow iteration: determine the current tip, scan
// everything new since lastScannedHeight (or the last ScanBatchSize blocks if this is
// the very first tick and no startHeight was configured), then check claims and
// detect stuck burns against that same tip.
func (t *Tracker) followTick(ctx context.Context) error {
	tip, err := t.L1.GetTipInfo(ctx)
	if err != nil {
		return fmt.Errorf("burnclaim: follow: get tip info: %w", err)
	}
	tipHeight := tip.GetMetadata().GetBestBlockHeight()

	from := t.lastScannedHeight + 1
	if t.lastScannedHeight == 0 {
		from = 1
		if tipHeight > ScanBatchSize {
			from = tipHeight - ScanBatchSize + 1
		}
	}
	if from <= tipHeight {
		found, err := t.scanRange(ctx, from, tipHeight)
		if err != nil {
			return fmt.Errorf("burnclaim: follow: scan range [%d,%d]: %w", from, tipHeight, err)
		}
		if found > 0 {
			log.Printf("burnclaim: follow: found %d new burn(s) in L1 height range [%d,%d]", found, from, tipHeight)
		}
	}
	t.lastScannedHeight = tipHeight

	checked, claimed, err := t.CheckClaims(ctx)
	if err != nil {
		log.Printf("burnclaim: follow: check claims: %v", err)
	} else if checked > 0 {
		log.Printf("burnclaim: follow: checked %d pending burn(s), %d newly claimed", checked, claimed)
	}

	marked, err := t.DetectStuck(ctx, tipHeight)
	if err != nil {
		log.Printf("burnclaim: follow: detect stuck: %v", err)
	} else if marked > 0 {
		log.Printf("burnclaim: follow: marked %d burn(s) stuck (tip height %d)", marked, tipHeight)
	}

	return nil
}

// commitmentHex/kernelHashHex are tiny naming-only wrappers around hex.EncodeToString
// so call sites read as what they mean, not just "some hex string".
func commitmentHex(b []byte) string { return hex.EncodeToString(b) }
func kernelHashHex(b []byte) string { return hex.EncodeToString(b) }
