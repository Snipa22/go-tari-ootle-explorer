-- 0002_template_registry_correction: fixes template_registry to match the REAL
-- GET /templates/catalogue (and /templates/catalogue/{address}) response shape.
--
-- Live-verified against https://ootle-indexer-a.tari.com (esmeralda, epoch 10990,
-- checked 2026-09-13) and against the real TemplateCatalogueItem struct
-- (clients/tari_indexer_client/src/types.rs, tari-project/tari-ootle commit d89dc92,
-- 2026-09-11):
--   template_address, template_name (NOT `name`), author_public_key, binary_hash (NOT
--   `metadata_hash` - see below for why 0001's column of that name was simply wrong),
--   at_epoch, and an OPTIONAL metadata_hash (a separate, real field on the same
--   struct - see note below).
--
-- 0001_init's tags/category/published_at columns were assumed from a different,
-- separate service entirely: the community template-metadata server (tari-cli's
-- `metadata-server-url` config, what `tari metadata publish` pushes to) - NOT
-- something GET /templates/catalogue returns. That server's real request/response
-- shape is still unconfirmed against real source (see DISPATCH_BRIEF.md) - don't
-- guess it here either; those columns belong in a LATER migration once
-- internal/registry is actually built and that shape is confirmed, per AGENTS.md's
-- "don't ship speculative columns" rule. So this migration drops them rather than
-- reinterpreting them.
--
-- NOTE ON metadata_hash: the real TemplateCatalogueItem struct (types.rs) has a
-- SECOND, genuinely real field beyond what DISPATCH_BRIEF.md's snippet listed: an
-- OPTIONAL `metadata_hash` ("Optional multihash of off-chain CBOR metadata") -
-- distinct from `binary_hash` (hash of the compiled WASM binary) and distinct from
-- 0001's removed `metadata_hash` column (which had a different, wrong meaning - it
-- was guessed as TemplateMetadata's own content hash, not this pointer field). This
-- is included here as nullable because it's confirmed directly from the same
-- primary source as every other column in this table, not inferred from the
-- metadata-server assumption AGENTS.md warns against. None of the live catalogue
-- entries checked during this dispatch happened to have it set (early/system
-- templates), so it is genuinely optional in practice, not just in the Rust type.
--
-- template_registry has never held real data (internal/registry, the only writer of
-- tags/category, doesn't exist yet), so this is a full drop+recreate rather than a
-- column-by-column ALTER migration - see DISPATCH_BRIEF.md.

DROP TABLE IF EXISTS template_registry;

CREATE TABLE template_registry (
    template_address  TEXT PRIMARY KEY,        -- TemplateCatalogueItem.template_address
    template_name     TEXT NOT NULL,           -- TemplateCatalogueItem.template_name
    author_public_key TEXT NOT NULL,           -- TemplateCatalogueItem.author_public_key
    binary_hash       TEXT NOT NULL,           -- TemplateCatalogueItem.binary_hash (hash of the compiled WASM binary)
    at_epoch          BIGINT NOT NULL,          -- TemplateCatalogueItem.at_epoch
    metadata_hash     TEXT,                     -- TemplateCatalogueItem.metadata_hash (optional multihash pointer to off-chain CBOR metadata); NULL when unset upstream
    first_seen_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 0001's shard_group_end rename: the real field on ValidatorInfo.shard_group is
-- `end_inclusive` (confirmed live via GET /validators, same indexer/epoch as above),
-- not a plain "end". No code in this repo references the column yet (internal/explorer
-- is introduced in this same dispatch, internal/vnhealth is a later one), so renaming
-- for clarity is cheap and doesn't touch already-shipped code - see DISPATCH_BRIEF.md's
-- "rename if cheap, comment-only otherwise" guidance.
ALTER TABLE validators RENAME COLUMN shard_group_end TO shard_group_end_inclusive;
COMMENT ON COLUMN validators.shard_group_end_inclusive IS
    'ValidatorInfo.shard_group.end_inclusive (inclusive upper bound of this validator''s committee shard range). Confirmed live against GET /validators, esmeralda epoch 10990, 2026-09-13 - the real field is end_inclusive, not a plain "end"; do not assume exclusive-end range semantics.';
