package burnclaim

// KnownBaseLayerConfirmations maps a tari-ootle network name to its REAL
// base_layer_confirmations consensus constant - the number of L1 blocks validators
// deliberately lag behind the L1 tip before a burn mined in a given block becomes
// claimable at all (see this package's doc comment and claim-burn.mdx's "Claims are
// delayed by the confirmation depth" aside for why).
//
// Confirmed by reading crates/consensus/src/consensus_constants.rs directly
// (tari-project/tari-ootle, commit d89dc92, 2026-09-11) - NOT guessed or taken from
// AGENTS.md's own summary of these numbers, per that file's "don't infer... from this
// file's route list" rule (which extends to consensus constants too, not just REST
// routes):
//
//	ConsensusConstants::MAINNET.base_layer_confirmations   = 780
//	ConsensusConstants::ESMERALDA.base_layer_confirmations = 100
//	ConsensusConstants::TESTNET.base_layer_confirmations   = 100  (shared by StageNet/NextNet/Igor - see From<Network> for ConsensusConstants)
//	ConsensusConstants::devnet(_).base_layer_confirmations = 3   (shared by LocalNet - see From<Network> for ConsensusConstants)
//
// This is intentionally NOT consulted with a fallback default for an unknown network
// name - AGENTS.md's standing rule against hardcoded/guessed infra assumptions
// applies just as much to a consensus constant as to an endpoint URL: a network this
// map doesn't recognize must have its confirmation lag supplied explicitly (see
// cmd/burnclaim's -confirmation-lag flag), not silently assigned some other
// network's real value.
var KnownBaseLayerConfirmations = map[string]uint64{
	"mainnet":   780,
	"esmeralda": 100,
	"testnet":   100,
	"stagenet":  100,
	"nextnet":   100,
	"igor":      100,
	"localnet":  3,
	"devnet":    3,
}

// DefaultExtraStuckBlocks is the default number of L1 blocks a burn may sit
// unclaimed PAST its network's real base_layer_confirmations lag before
// DetectStuck flags it 'stuck' - see StuckThreshold. Per DISPATCH_BRIEF.md: "a
// sane configurable default like 1000 extra blocks" on top of the real per-network
// confirmation lag, not a single universal number that ignores it. Callers should
// treat this as a starting point, not a tuned production value - v1 has no
// operational data yet on how long legitimate claims actually take beyond the
// confirmation window.
const DefaultExtraStuckBlocks = 1000

// StuckThreshold returns the total number of L1 blocks (since burn_height) a burn
// may remain unclaimed before it is considered stuck: the network's real
// confirmation lag PLUS a configurable extra margin, per DISPATCH_BRIEF.md's
// explicit instruction not to flag every fresh, still-within-confirmation-window
// burn as stuck.
func StuckThreshold(confirmationLag, extraBlocks uint64) uint64 {
	return confirmationLag + extraBlocks
}

// IsStuck reports whether a pending burn mined at burnHeight should be considered
// stuck, given the current L1 tip height and a threshold (see StuckThreshold) - true
// exactly when the burn is older than the threshold AND still has no claim (the
// caller is expected to only call this for rows already known to be 'pending', i.e.
// not yet claimed).
func IsStuck(burnHeight, tipHeight, threshold uint64) bool {
	if tipHeight <= burnHeight {
		return false // not even mined yet from this node's view (shouldn't happen for an already-recorded burn, but never treat it as "old" if so)
	}
	return tipHeight-burnHeight > threshold
}
