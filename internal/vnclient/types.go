package vnclient

// This file's Go structs mirror REAL tari_validator_node JSON-RPC response shapes for
// the six methods internal/vnhealth needs, per AGENTS.md's "Primary source" rule.
// Verified against a fresh `git clone --depth 1
// https://github.com/tari-project/tari-ootle.git`, commit d89dc92 (2026-09-11) - the
// SAME commit AGENTS.md's route list and internal/indexerclient's types.go were
// checked against - specifically:
//   - applications/tari_validator_node/src/json_rpc/server.rs (method-dispatch match
//     statement: confirms all six method names below are real, current routes)
//   - applications/tari_validator_node/src/json_rpc/handlers.rs (each handler's actual
//     body - not just the request/response struct declarations, since a few of these
//     handlers populate fields in a way the struct definition alone doesn't make
//     obvious, e.g. get_consensus_status's `state` is `ConsensusCurrentState::to_string()`,
//     not a serialized enum)
//   - clients/validator_node_client/src/types.rs (the request/response struct
//     definitions themselves)
//
// IMPORTANT per DISPATCH_BRIEF.md: no live tari_validator_node JSON-RPC endpoint was
// reachable in this environment to verify these shapes against a real running node -
// only against the above source. See internal/vnclient's package doc and the dispatch
// report for the explicit "not live-verified" callout.
//
// Field-shape notes worth flagging (each independently confirmed against the real
// Rust source or its exact transitive dependency, not guessed or assumed-by-pattern):
//
//   - Epoch, NodeHeight, Shard, VotePower are single-field ("newtype") tuple structs.
//     Epoch (crates/engine_types/src/epoch.rs) has no explicit #[serde(transparent)],
//     but serde's derive for a single-field tuple struct already serializes as the
//     bare inner value for a self-describing format like JSON regardless (it calls
//     Serializer::serialize_newtype_struct, which serde_json implements as "serialize
//     the inner value directly") - NodeHeight/Shard/VotePower add the attribute
//     explicitly but it's behaviourally the same for JSON either way. All four are
//     plain JSON numbers on the wire.
//   - RistrettoPublicKeyBytes (crates/template_lib_types/src/crypto/ristretto.rs) and
//     SubstateAddress (crates/common_types/src/substate_address.rs) both use an
//     explicit hex serde helper - plain lowercase hex JSON strings, no "0x" prefix.
//   - PeerAddress(PeerId) (crates/p2p/src/peer_address.rs wrapping
//     libp2p_identity::PeerId) - PeerId's own Serialize impl (libp2p-identity 0.2.14,
//     src/peer_id.rs) special-cases human-readable serializers (JSON is one) to
//     `to_base58()`, so a committee member's `address` is a base58 string on the
//     wire, NOT the raw multihash bytes it'd be for a non-human-readable format.
//   - NumPreshards (crates/common_types/src/num_preshards.rs, nested inside
//     CommitteeInfo) is a FIELDLESS enum with explicit integer discriminants (P1=1 ..
//     P256=256) but no #[serde(...)] numeric-repr override - default
//     derive(Serialize) on a fieldless enum serializes the VARIANT NAME ("P256"), not
//     the discriminant. Deliberately NOT modeled below (internal/vnhealth doesn't
//     need num_shards) specifically because it's an easy field to get wrong by
//     assuming "it's just a number" - flagged here rather than silently guessed at.
//   - FixedHash (tari_common_types, GetEpochManagerStatsResponse.current_block_hash)
//     is #[serde(transparent)] over a raw [u8; 32]. Rust's serde does NOT
//     special-case fixed-size byte arrays as byte-strings by default (that needs the
//     separate `serde_bytes` crate, not used here), so this field serializes as a
//     JSON ARRAY of 32 plain numbers, NOT a hex string like every other hash-shaped
//     field in this codebase. Deliberately NOT modeled below (v1's vnhealth doesn't
//     need it) to avoid that easy mistake.
//   - Committee<TAddr>/CommitteeMember<TAddr> (crates/common_types/src/committee.rs)
//     have some private Rust fields, but derive(Serialize) expands in the same
//     module, where private-field access is legal - they serialize exactly like
//     public fields would, using the field's own identifier as the JSON key.
//   - ConnectionDirection, LogLevel, SubstateStatus etc. are fieldless enums with NO
//     explicit discriminants and NO serde repr override either - same "variant name
//     string" rule as NumPreshards applies. Connection.Direction below is modeled as
//     a plain string for this reason (holds "Inbound"/"Outbound" verbatim), not a Go
//     enum/int.

// GetIdentityResponse mirrors GetIdentityResponse
// (clients/validator_node_client/src/types.rs) - the response of the `get_identity`
// method (no request params).
type GetIdentityResponse struct {
	PeerID             string   `json:"peer_id"`
	PublicKey          string   `json:"public_key"`
	PublicAddresses    []string `json:"public_addresses"`
	SupportedProtocols []string `json:"supported_protocols"`
	ProtocolVersion    string   `json:"protocol_version"`
	UserAgent          string   `json:"user_agent"`
	FeeClaimPublicKey  string   `json:"fee_claim_public_key"`
}

// GetCommitteeRequest mirrors GetCommitteeRequest
// (clients/validator_node_client/src/types.rs) - the request params of the
// `get_committee` method: the committee covering the given substate_address as of the
// given epoch.
type GetCommitteeRequest struct {
	Epoch           uint64 `json:"epoch"`
	SubstateAddress string `json:"substate_address"`
}

// CommitteeMember mirrors CommitteeMember<PeerAddress>
// (crates/common_types/src/committee.rs, instantiated with PeerAddress for this
// JSON-RPC server - see GetCommitteeResponse in handlers.rs).
type CommitteeMember struct {
	// Address is the member's PeerAddress, base58-encoded (see this file's PeerId
	// serialization note above).
	Address   string `json:"address"`
	PublicKey string `json:"public_key"`
	VotePower uint64 `json:"vote_power"`
}

// Committee mirrors Committee<PeerAddress> (crates/common_types/src/committee.rs).
type Committee struct {
	Members []CommitteeMember `json:"members"`
}

// GetCommitteeResponse mirrors GetCommitteeResponse
// (clients/validator_node_client/src/types.rs) - the response of `get_committee`.
//
// NOTE, flagged per AGENTS.md's "don't guess, flag what you can't verify" rule: this
// response is a flat MEMBER LIST for the committee covering the requested
// substate_address - it does NOT carry that committee's shard_group range anywhere
// (confirmed against both this struct and Committee<TAddr> itself, neither has a
// shard_group field). internal/vnhealth therefore does NOT use this method to
// populate validators.shard_group_start/end_inclusive (there's nothing in this
// response to populate it FROM) - see internal/vnhealth's doc comment for what it
// uses instead (GetEpochManagerStatsResponse.committee_info) and why.
type GetCommitteeResponse struct {
	Committee Committee `json:"committee"`
}

// GetAllVnsRequest mirrors GetAllVnsRequest (clients/validator_node_client/src/types.rs)
// - the request params of `get_all_vns`: the full validator-node roster as of the
// given epoch.
type GetAllVnsRequest struct {
	Epoch uint64 `json:"epoch"`
}

// BaseLayerValidatorNode mirrors BaseLayerValidatorNode
// (clients/base_node_client/src/types.rs) - as returned by `get_all_vns`. ShardKey is
// a single point in the 256-bit shard space (a SubstateAddress), NOT a range, so
// (unlike ValidatorInfo.shard_group from the indexer's REST /validators - see
// indexerclient.Validator) this alone can't populate
// validators.shard_group_start/end_inclusive either.
type BaseLayerValidatorNode struct {
	PublicKey   string  `json:"public_key"`
	ShardKey    string  `json:"shard_key"`
	SidechainID *string `json:"sidechain_id"`
}

// GetAllVnsResponse mirrors GetAllVnsResponse
// (clients/validator_node_client/src/types.rs) - the response of `get_all_vns`.
type GetAllVnsResponse struct {
	Vns []BaseLayerValidatorNode `json:"vns"`
}

// GetConsensusStatusResponse mirrors GetConsensusStatusResponse
// (clients/validator_node_client/src/types.rs) - the response of
// `get_consensus_status` (no request params). State is confirmed (handlers.rs) to be
// `ConsensusCurrentState::to_string()` - free-form text (e.g. "Running", "Syncing",
// "Idle", "Initialising", "CheckSync" per ConsensusCurrentState's variants in
// tari_consensus::hotstuff::ConsensusCurrentState), not a fixed enum tag - matches
// migrations/0001_init.up.sql's validators.consensus_status column doc comment
// ("GetConsensusStatusResponse.state ('Running', 'Syncing', etc)").
//
// StateVersions (Option<IndexMap<Shard, StateVersion>>) is deliberately NOT modeled -
// internal/vnhealth doesn't need per-shard state-tree versions for v1.
type GetConsensusStatusResponse struct {
	Epoch  uint64 `json:"epoch"`
	Height uint64 `json:"height"`
	State  string `json:"state"`
}

// CommitteeInfo mirrors the small subset of CommitteeInfo
// (crates/common_types/src/committee.rs) internal/vnhealth needs: this validator's
// OWN current shard_group and the epoch it was computed for. num_shards (a
// NumPreshards, serializes as a variant-name STRING per this file's top comment),
// num_shard_group_members, num_committees, and total_power are deliberately NOT
// modeled - not needed for v1's validators table.
type CommitteeInfo struct {
	ShardGroup ShardGroup `json:"shard_group"`
	Epoch      uint64     `json:"epoch"`
}

// ShardGroup mirrors ShardGroup (crates/common_types/src/shard_group.rs) - private
// Start/EndInclusive fields, default serde field names ("start"/"end_inclusive").
// Deliberately a SEPARATE type from indexerclient.ShardGroup even though they share
// today's wire shape: they come from genuinely different Rust source files
// (crates/common_types/src/shard_group.rs here vs the indexer client's own type), and
// coupling this package's types to indexerclient's is not worth the today-only
// convenience if one of the two ever drifts.
type ShardGroup struct {
	Start        uint32 `json:"start"`
	EndInclusive uint32 `json:"end_inclusive"`
}

// GetEpochManagerStatsResponse mirrors GetEpochManagerStatsResponse
// (clients/validator_node_client/src/types.rs) - the response of
// `get_epoch_manager_stats` (no request params).
//
// CurrentBlockHash (a FixedHash - see this file's top comment on why it's a JSON
// array of numbers, not hex) is deliberately NOT modeled.
type GetEpochManagerStatsResponse struct {
	CurrentEpoch              uint64         `json:"current_epoch"`
	CurrentBlockHeight        uint64         `json:"current_block_height"`
	IsValid                   bool           `json:"is_valid"`
	IsInitialScanningComplete bool           `json:"is_initial_scanning_complete"`
	StartEpoch                *uint64        `json:"start_epoch"`
	CommitteeInfo             *CommitteeInfo `json:"committee_info"`
}

// GetCommsStatsResponse mirrors GetCommsStatsResponse
// (clients/validator_node_client/src/types.rs) - the response of `get_comms_stats` (no
// request params). ConnectionStatus is confirmed (handlers.rs) to be the literal
// string "Online" or "Offline" (not an arbitrary free-form string), computed from
// whether the node has any connected peers at all.
type GetCommsStatsResponse struct {
	ConnectionStatus string `json:"connection_status"`
}

// Connection mirrors the subset of Connection
// (clients/validator_node_client/src/types.rs) internal/vnhealth needs: enough to
// count and describe active P2P connections. Age/PingLatency (Duration-shaped -
// {"secs":N,"nanos":N} on the wire per the real ts() annotation) are deliberately not
// modeled - not needed for v1.
type Connection struct {
	ConnectionID string `json:"connection_id"`
	PeerID       string `json:"peer_id"`
	Address      string `json:"address"`
	// Direction holds the real enum variant name verbatim ("Inbound" or "Outbound") -
	// see this file's top comment on why fieldless-enum fields are modeled as plain
	// strings, not a Go enum/int, absent a confirmed numeric/lowercase serde override.
	Direction string  `json:"direction"`
	UserAgent *string `json:"user_agent"`
}

// GetConnectionsResponse mirrors GetConnectionsResponse
// (clients/validator_node_client/src/types.rs) - the response of `get_connections` (no
// request params).
type GetConnectionsResponse struct {
	Connections []Connection `json:"connections"`
}
