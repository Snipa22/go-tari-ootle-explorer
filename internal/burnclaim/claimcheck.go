package burnclaim

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/Snipa22/go-tari-ootle-explorer/internal/indexerclient"
)

// tombstoneAddressPrefix is the real address_prefixes::CLAIMED_OUTPUT_TOMBSTONE
// constant (crates/template_lib_types/src/address_prefixes.rs) - confirmed directly
// against that file, not guessed from ClaimedOutputTombstoneAddress's Display impl
// alone.
const tombstoneAddressPrefix = "tombstone"

// tombstoneSubstateID builds the real L2 substate id for a burn's
// ClaimedOutputTombstoneAddress, given the burn's commitment as a hex string (same
// hex encoding db.BurnClaim.Commitment is stored in - see burnsInBlock).
//
// Confirmed against
// crates/template_lib_types/src/substates/claimed_output_tombstone.rs:
// ClaimedOutputTombstoneAddress::from_commitment maps the 32-byte commitment
// DIRECTLY into the 32-byte ObjectKey (no re-hashing), and its Display impl renders
// as "tombstone_" + the object key's lowercase hex - so the substate id is simply
// "tombstone_" + the same lowercase hex this package already stores as
// db.BurnClaim.Commitment.
func tombstoneSubstateID(commitmentHex string) string {
	return tombstoneAddressPrefix + "_" + commitmentHex
}

// CheckClaims checks every 'pending' burn_claims row against the indexer's substate
// lookup for that burn's tombstone address, marking any that now exist 'claimed'.
// Returns the number of pending rows checked and how many were newly marked claimed.
//
// A 404 (StatusError with StatusCode 404 - the real, confirmed-live "this substate
// doesn't exist" response, see indexerclient.Client.GetSubstate's doc comment) is the
// EXPECTED, non-error outcome for a burn that hasn't been claimed yet - it does not
// count as a failure and does not stop the pass; every other error (network failure,
// a non-404 status, a decode error) is logged and skipped for that one row without
// aborting the rest of the pass, matching internal/vnhealth's own
// "one row's failure doesn't block the others" convention.
func (t *Tracker) CheckClaims(ctx context.Context) (checked int, claimed int, err error) {
	pending, err := t.Store.ListBurnClaimsByStatus(ctx, "pending")
	if err != nil {
		return 0, 0, fmt.Errorf("burnclaim: check claims: list pending: %w", err)
	}

	now := time.Now()
	for _, b := range pending {
		checked++
		substateID := tombstoneSubstateID(b.Commitment)

		_, err := t.Indexer.GetSubstate(ctx, substateID)
		if err != nil {
			var statusErr *indexerclient.StatusError
			if errors.As(err, &statusErr) && statusErr.StatusCode == http.StatusNotFound {
				continue // expected: not claimed yet
			}
			// Any other failure (network, non-404 status, decode) - log and move
			// on to the next row rather than aborting the whole pass.
			log.Printf("burnclaim: check claims: %s (commitment %s): %v", b.L1BurnTxHash, b.Commitment, err)
			continue
		}

		// The substate exists - regardless of its decoded contents (a tombstone
		// that exists at all, at this address, can only be a
		// ClaimedOutputTombstone - the address is a preimage-committed derivation
		// specific to that substate KIND, see tombstoneSubstateID's doc comment -
		// so a 200 here is itself sufficient proof of a claim, independent of
		// whether this client's SubstateValue.ClaimedOutputTombstone decode
		// succeeded).
		if err := t.Store.MarkBurnClaimed(ctx, b.Commitment, now); err != nil {
			log.Printf("burnclaim: check claims: mark claimed %s: %v", b.Commitment, err)
			continue
		}
		claimed++
	}

	return checked, claimed, nil
}
