package indexerclient

// This file's Go structs mirror the REAL tari_indexer REST API response shapes needed
// for v1's ootle_blocks/validators/template_registry tables, per AGENTS.md's "Primary
// source" rule and DISPATCH_BRIEF.md - verified two ways:
//   1. Live, against https://ootle-indexer-a.tari.com (esmeralda, epoch 10990, checked
//      2026-09-13) - see testdata/*.json for the actual captured responses these types
//      are built from.
//   2. Against the real Rust request/response structs in
//      clients/tari_indexer_client/src/types.rs and the handler functions in
//      applications/tari_indexer/src/rest_api/handlers/{misc,validators,templates,
//      epoch_checkpoints}.rs, tari-project/tari-ootle commit d89dc92 (2026-09-11).
//
// Only fields this repo's v1 explorer actually needs are modeled - Go's json.Unmarshal
// silently ignores unrecognized fields in the response, so this is safe to keep
// intentionally partial rather than mirroring every Rust field.

// IndexerInfo is GET /info's response - this indexer node's own local configuration,
// not something two indexers necessarily agree on. Mirrors GetIndexerInfoResponse.
type IndexerInfo struct {
	Version                      string  `json:"version"`
	Network                      string  `json:"network"`
	NetworkByte                  uint8   `json:"network_byte"`
	SidechainID                  *string `json:"sidechain_id"`
	CurrentEpoch                 uint64  `json:"current_epoch"`
	TransactionRetentionEpochs   *uint64 `json:"transaction_retention_epochs"`
	IndexGossipedTransactions    bool    `json:"index_gossiped_transactions"`
	VerifySubstateProofs         bool    `json:"verify_substate_proofs"`
	SubstateCacheMaxServeLagSecs uint64  `json:"substate_cache_max_serve_lag_secs"`
	IndexesAllEvents             bool    `json:"indexes_all_events"`
}

// ShardGroup mirrors tari_ootle_common_types::ShardGroup's wire shape ({"start",
// "end_inclusive"}, both u32 Shard values) - confirmed live on both GET /validators and
// GET /epoch-checkpoints/latest. NOTE: the real field is end_inclusive, not a plain
// "end" - see internal/db/migrations/0002_template_registry_correction for the
// validators.shard_group_end -> shard_group_end_inclusive rename this caused.
type ShardGroup struct {
	Start        uint32 `json:"start"`
	EndInclusive uint32 `json:"end_inclusive"`
}

// Validator is a single entry of GET /validators' response, mirroring ValidatorInfo
// (clients/tari_indexer_client/src/types.rs).
type Validator struct {
	PublicKey         string     `json:"public_key"`
	PeerID            string     `json:"peer_id"`
	ShardGroup        ShardGroup `json:"shard_group"`
	StartEpoch        uint64     `json:"start_epoch"`
	EndEpoch          *uint64    `json:"end_epoch"`
	FeeClaimPublicKey string     `json:"fee_claim_public_key"`
	VotePower         uint64     `json:"vote_power"`
}

// ListValidatorsResponse is GET /validators' top-level response shape, mirroring
// ListValidatorsResponse (clients/tari_indexer_client/src/types.rs).
type ListValidatorsResponse struct {
	Epoch      uint64      `json:"epoch"`
	Validators []Validator `json:"validators"`
}

// TemplateCatalogueEntry is a single entry of GET /templates/catalogue (and the sole
// body of GET /templates/catalogue/{address}), mirroring TemplateCatalogueItem
// (clients/tari_indexer_client/src/types.rs).
//
// MetadataHash is a genuinely real, separate field on this same struct beyond what
// DISPATCH_BRIEF.md's snippet listed - an optional multihash pointing at off-chain CBOR
// metadata (NOT the same thing as BinaryHash, and NOT the community metadata-server
// shape AGENTS.md says not to guess - this is confirmed directly on the primary
// indexer-REST source, not inferred). See migration 0002's comment for the full
// reasoning. None of the live catalogue entries captured during this dispatch had it
// set, so it's modeled as a pointer to stay correctly optional either way.
type TemplateCatalogueEntry struct {
	TemplateAddress string  `json:"template_address"`
	TemplateName    string  `json:"template_name"`
	AuthorPublicKey string  `json:"author_public_key"`
	BinaryHash      string  `json:"binary_hash"`
	AtEpoch         uint64  `json:"at_epoch"`
	MetadataHash    *string `json:"metadata_hash,omitempty"`
}

// ListTemplateCatalogueResponse is GET /templates/catalogue's top-level response shape,
// mirroring ListTemplateCatalogueResponse (clients/tari_indexer_client/src/types.rs).
type ListTemplateCatalogueResponse struct {
	Entries []TemplateCatalogueEntry `json:"entries"`
}

// EpochCheckpointAccumulatedData mirrors the small subset of the checkpoint header's
// `accumulated_data` object this repo cares about - confirmed live (see
// testdata/epoch_checkpoint_latest.json); the exact Rust type (tari_sidechain, an
// external crate not vendored in tari-ootle) wasn't directly readable during this
// dispatch, so this shape is derived from the live JSON's field names rather than the
// Rust source - flagged here per AGENTS.md's "flag what you couldn't verify" rule.
type EpochCheckpointAccumulatedData struct {
	TotalExhaustBurn uint64 `json:"total_exhaust_burn"`
}

// EpochCheckpointHeader is the closest real analog to a "block" that
// GET /epoch-checkpoints/latest exposes: the SidechainBlockHeader of the last block
// committed in the checkpointed epoch for the checkpoint's shard group. Confirmed live
// (testdata/epoch_checkpoint_latest.json) at path
// checkpoint.proof.V1.command.commit_proof.header.
//
// IMPORTANT, flagged per AGENTS.md's "don't trust the snippet, don't guess" rule: this
// reduced wire header has NEITHER a block "id" NOR a "timestamp" field - both exist on
// the full consensus BlockHeader type (crates/storage/src/consensus_models/
// block_header.rs) but are dropped from this proof-oriented SidechainBlockHeader
// variant (id is a cached hash computed from other header fields via
// BlockHeader::calculate_block_id, which needs fields - e.g. total_leader_fee,
// extra_data - not present here either; timestamp is explicitly "informational only,
// not part of the commit hash" per BlockHeader's own doc comment, so it's dropped from
// the provable/committed proof header). internal/explorer therefore cannot populate
// ootle_blocks.block_id/timestamp with real upstream values from this endpoint - see
// internal/explorer's doc comment for how it works around this, and the dispatch
// report for why this is flagged as a follow-up schema/semantics gap rather than
// silently faked.
type EpochCheckpointHeader struct {
	// Network is the raw network BYTE (e.g. 38 for esmeralda) here, NOT the network
	// name string GET /info's "network" field uses - confirmed live: this header's
	// "network" decodes as a JSON number, unlike GetIndexerInfoResponse's "network"
	// (a string) plus separate "network_byte" (a number). Different Rust
	// serialization contexts for the same logical concept - don't assume the two
	// "network" fields across this package's types share a shape.
	Network           uint8                          `json:"network"`
	Epoch             uint64                         `json:"epoch"`
	Height            uint64                         `json:"height"`
	ShardGroup        ShardGroup                     `json:"shard_group"`
	ParentID          string                         `json:"parent_id"`
	JustifyID         string                         `json:"justify_id"`
	ProposedBy        string                         `json:"proposed_by"`
	CommandMerkleRoot string                         `json:"command_merkle_root"`
	StateMerkleRoot   string                         `json:"state_merkle_root"`
	EpochHash         string                         `json:"epoch_hash"`
	MetadataHash      string                         `json:"metadata_hash"`
	AccumulatedData   EpochCheckpointAccumulatedData `json:"accumulated_data"`
}

// EpochCheckpoint is this client's simplified view of GET /epoch-checkpoints/latest's
// deeply-nested, variant-wrapped `checkpoint` field - see epochCheckpointEnvelope for
// the raw wire shape this is extracted from.
type EpochCheckpoint struct {
	Header EpochCheckpointHeader
}

// epochCheckpointEnvelope mirrors the real wire shape of GET /epoch-checkpoints/latest's
// top-level response: {"checkpoint": {"proof": {"V1": {"commit_proof": {"header": {...
// Confirmed live (testdata/epoch_checkpoint_latest.json). The envelope also carries a
// `shard_tree_summary` (a large per-shard Merkle-root map) and the proof's
// `inclusion_proof`/`proof_elements` (QC signatures etc.) - none of which
// ootle_blocks needs, so they're deliberately not modeled here; Go's JSON decoder
// ignores them.
type epochCheckpointEnvelope struct {
	Checkpoint struct {
		Proof struct {
			V1 struct {
				CommitProof struct {
					Header EpochCheckpointHeader `json:"header"`
				} `json:"commit_proof"`
			} `json:"V1"`
		} `json:"proof"`
	} `json:"checkpoint"`
}

// ClaimedOutputTombstoneSubstate mirrors the real ClaimedOutputTombstone substate
// value (crates/engine_types/src/confidential/unclaimed.rs) - the ONLY field it
// carries is the burned amount, confirmed both against that Rust struct (a single
// `#[n(0)] pub value: u64`) and live: no created-at/claimed-at/tx-id field exists on
// this substate at all, so internal/burnclaim's "claimed_at" is necessarily this
// repo's own observation-time stand-in (documented on db.BurnClaim), not a real
// upstream timestamp - same caveat pattern as EpochCheckpointHeader's
// synthetic-block-id/timestamp above.
type ClaimedOutputTombstoneSubstate struct {
	Value uint64 `json:"value"`
}

// SubstateValue mirrors the wire shape of GetSubstateResponse's `substate` field: a
// Rust enum (engine_types::substate::SubstateValue) serialized with serde's default
// EXTERNALLY TAGGED representation - i.e. {"<VariantName>": <inner value>} - confirmed
// live against https://ootle-indexer-a.tari.com/substates/resource_<64 hex 01s>
// (esmeralda, checked 2026-09-13), which returned
// {"version":0,"substate":{"Resource":{...}},"verified":true}. Only the
// ClaimedOutputTombstone variant is modeled here (the one internal/burnclaim's L2
// claim-checker needs); every other variant (Component, Resource, Vault,
// NonFungible, TransactionReceipt, Template, ValidatorFeePool, Utxo,
// ConfidentialOutput - crates/engine_types/src/substate.rs) is left unmodeled and
// silently ignored by json.Unmarshal, consistent with this package's
// "only model what's needed" convention.
type SubstateValue struct {
	ClaimedOutputTombstone *ClaimedOutputTombstoneSubstate `json:"ClaimedOutputTombstone,omitempty"`
}

// GetSubstateResponse is GET /substates/{substate_id}'s 200 response shape, mirroring
// the real GetSubstateResponse (clients/tari_indexer_client/src/types.rs) - confirmed
// live (see SubstateValue's doc comment above) and against the handler
// (applications/tari_indexer/src/rest_api/handlers/substates.rs). A substate that
// does not exist (e.g. an unclaimed burn's tombstone) is a 404 with the real
// indexer's {"error": "..."} shape, NOT a 200 with some "not found" variant - see
// Client.GetSubstate's doc comment for how that's surfaced to callers as a
// *StatusError rather than folded into this struct.
type GetSubstateResponse struct {
	Version  uint32        `json:"version"`
	Substate SubstateValue `json:"substate"`
	// Verified is true when the indexer checked this substate's value against the
	// shard group committee via a merkle proof - false when proofs are disabled or
	// no committee member could supply one yet. internal/burnclaim's L2 checker
	// does not currently gate on this (v1 accepts the indexer's answer either way,
	// same trust level Backfill/Poll already extend to every other indexer route in
	// this repo) - flagged here as a real, deliberate simplification rather than an
	// oversight, since a mis-verified false claim would be a genuine
	// security-relevant gap in a production burn tracker.
	Verified bool `json:"verified"`
}
