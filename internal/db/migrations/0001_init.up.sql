-- 0001_init: minimal-viable v1 schema, covering exactly the four tables step 2 of
-- AGENTS.md's build order calls for. Don't add more tables or speculative columns here
-- - extend incrementally as internal/explorer, internal/vnhealth, internal/burnclaim,
-- and internal/registry actually need to store more.
--
-- Column choices for ootle_blocks and validators are checked against the real upstream
-- shapes (tari-project/tari-ootle commit d89dc92, 2026-09-11) per AGENTS.md's "Primary
-- source" rule, not guessed from the route list in AGENTS.md or from web search:
--   - ootle_blocks mirrors consensus BlockHeader's id/height/epoch/timestamp fields
--     (crates/storage/src/consensus_models/block_header.rs) plus a command_count
--     column standing in for "transaction count": a block's body is a BTreeSet<Command>
--     (crates/storage/src/consensus_models/command.rs) where most variants wrap a
--     TransactionAtom, so "number of commands" is the closest real per-block count to
--     a traditional chain's transaction count. Note this table isn't populated by any
--     REST route directly (there's no plain "/blocks" endpoint on tari_indexer - see the
--     confirmed route list in AGENTS.md); internal/explorer (a later dispatch) is
--     expected to derive rows from /epoch-checkpoints and/or consensus data as it's
--     built, not from a route that doesn't exist.
--   - validators mirrors tari_indexer's GET /validators ValidatorInfo shape
--     (public_key, shard_group; applications/tari_indexer/src/rest_api/handlers/validators.rs)
--     plus tari_validator_node's JSON-RPC get_consensus_status response's `state` string
--     field (applications/tari_validator_node/src/json_rpc/handlers.rs) for
--     consensus_status.
--   - burn_claims and template_registry column lists are exactly as specified in the v1
--     scope brief (l1_burn_tx_hash/claim_public_key/burn_height/claim_tx_id/claimed_at/
--     status; template_address/name/tags/category/metadata_hash/published_at) - the
--     latter's tags/category fields match the real TemplateMetadata struct's
--     `tags: Vec<String>` / `category: Option<String>`
--     (crates/template_metadata/src/metadata.rs).

CREATE TABLE IF NOT EXISTS ootle_blocks (
    block_id      TEXT PRIMARY KEY,          -- hex-encoded consensus BlockId (BlockHeader.id)
    height        BIGINT NOT NULL,           -- BlockHeader.height (NodeHeight)
    epoch         BIGINT NOT NULL,           -- BlockHeader.epoch
    "timestamp"   BIGINT NOT NULL,           -- unix seconds, BlockHeader.timestamp
    command_count INTEGER NOT NULL DEFAULT 0 -- number of Commands in the block body (tx-count proxy, see above)
);

CREATE INDEX IF NOT EXISTS idx_ootle_blocks_epoch ON ootle_blocks (epoch);
CREATE INDEX IF NOT EXISTS idx_ootle_blocks_height ON ootle_blocks (height);

CREATE TABLE IF NOT EXISTS validators (
    public_key        TEXT PRIMARY KEY,      -- hex-encoded Ristretto public key (ValidatorInfo.public_key)
    last_seen_epoch   BIGINT NOT NULL,        -- highest epoch this validator was seen in the roster
    shard_group_start INTEGER NOT NULL,       -- ValidatorInfo.shard_group start shard (committee shard range)
    shard_group_end   INTEGER NOT NULL,       -- ValidatorInfo.shard_group end shard (inclusive)
    consensus_status  TEXT,                   -- GetConsensusStatusResponse.state ("Running", "Syncing", etc); NULL if never checked
    last_checked_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_validators_last_seen_epoch ON validators (last_seen_epoch);

CREATE TABLE IF NOT EXISTS burn_claims (
    l1_burn_tx_hash   TEXT PRIMARY KEY,       -- L1 burnt-UTXO transaction hash
    claim_public_key  TEXT NOT NULL,          -- public key the burn commits to claiming against on L2
    burn_height       BIGINT NOT NULL,        -- L1 block height the burn was mined at
    claim_tx_id       TEXT,                   -- L2 claim transaction id, NULL until claimed
    claimed_at        TIMESTAMPTZ,            -- NULL until claimed
    status            TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'claimed', 'stuck'))
);

CREATE INDEX IF NOT EXISTS idx_burn_claims_status ON burn_claims (status);

CREATE TABLE IF NOT EXISTS template_registry (
    template_address TEXT PRIMARY KEY,        -- on-chain PublishedTemplateAddress
    name             TEXT NOT NULL,           -- TemplateMetadata.name
    tags             JSONB NOT NULL DEFAULT '[]'::jsonb, -- TemplateMetadata.tags (Vec<String>), stored as a JSON array
    category         TEXT,                    -- TemplateMetadata.category (Option<String>)
    metadata_hash    TEXT NOT NULL,            -- hex-encoded MetadataHash
    published_at     TIMESTAMPTZ NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_template_registry_category ON template_registry (category);
