-- 0004_template_registry_metadata_server_fields: extends template_registry with the
-- real fields the community template-metadata server (internal/registry, this
-- dispatch) adds on top of what internal/explorer already populates from the
-- indexer's own GET /templates/catalogue (see 0002_template_registry_correction).
--
-- Live-verified this session against the real, reachable metadata server:
--
--   curl -sS https://ootle-templates-esme.tari.com/community-templates/api/templates/featured
--
-- (esmeralda), which returned a JSON array of objects shaped like:
--
--   {"template_address":"0bd8d5844741b0a76fa113f6bcd63121150961ae13c61bc4b18d92a63b98d303",
--    "template_name":"TariStableCoin",
--    "author_public_key":"3c6eebdcb2939ebad646a9d464874005aa979f1e575b72cd2a690289f10e5e50",
--    "author_friendly_name":null,
--    "binary_hash":"2baac65d72f75cf7e0de433c7cab1d14d162f3c7249795c0d9add0dde59c9ce2",
--    "at_epoch":9013,
--    "metadata_hash":null,
--    "definition":null,
--    "code_size":157535,
--    "is_featured":true,
--    "metadata":{"name":"tari-stablecoin-minimal","version":"0.1.0", ... "tags":["token","fungible","defi","stablecoin"],"category":"token", ...}}
--
-- This is a strict superset of template_registry's current columns
-- (template_address/template_name/author_public_key/binary_hash/at_epoch/
-- metadata_hash/first_seen_at) - this migration only ADDS the five genuinely new
-- fields, per DISPATCH_BRIEF.md:
--
--   - author_friendly_name TEXT: nullable, observed null on every live entry checked
--     this session, but a real optional field on the response struct.
--   - code_size BIGINT: the compiled WASM binary's size in bytes - always present as
--     a plain integer on every live entry checked.
--   - is_featured BOOLEAN NOT NULL DEFAULT false: always present (true/false) on
--     every live entry from this specific route (GET .../featured - unsurprising,
--     that route's whole purpose is listing featured templates), so NOT NULL with a
--     false default is safe for any row this repo's OTHER writer
--     (internal/explorer, which never sets this column at all - see below) creates.
--   - metadata JSONB: nullable. Per DISPATCH_BRIEF.md, this is where tags/category
--     genuinely live (confirmed live: the "TariStableCoin" entry above has a
--     non-null metadata object containing "tags":[...] and "category":"token"), but
--     the real server-side response struct for this field could not be found in
--     tari-project/tari-cli or a linked server repo (see the dispatch report) - per
--     AGENTS.md's "don't guess a struct shape" rule, this is stored as an OPAQUE
--     JSONB blob column, not decomposed into structured tags/category columns.
--   - definition JSONB: nullable. Observed null on every /featured entry, but a real,
--     populated value when fetched via the per-template-address route (confirmed
--     live: GET .../api/templates/<addr> for the same TariStableCoin address returns
--     a non-null definition containing the CBOR-decoded-to-JSON TemplateDef ABI:
--     {"V1":{"abi_version":0,"functions":[...]}}) - also stored as opaque JSONB per
--     the same reasoning as metadata, since decomposing the full ABI shape isn't
--     needed for v1's mirror.
--
-- internal/explorer's existing UpsertTemplateRegistryEntry (from 0002's fields) is
-- deliberately left untouched by this migration and its own SQL never references
-- these five new columns at all - so it can never null them out on conflict,
-- regardless of upsert ordering between internal/explorer and internal/registry (see
-- internal/db/rows.go's UpsertTemplateRegistryMetadata for the new, merge-safe writer
-- these columns actually get written through).

ALTER TABLE template_registry
    ADD COLUMN author_friendly_name TEXT,
    ADD COLUMN code_size BIGINT,
    ADD COLUMN is_featured BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN metadata JSONB,
    ADD COLUMN definition JSONB;

COMMENT ON COLUMN template_registry.author_friendly_name IS
    'Community metadata server''s optional human-friendly author name; NULL on every live entry checked 2026-09-13, but a real nullable field on the response struct.';
COMMENT ON COLUMN template_registry.code_size IS
    'Community metadata server''s compiled WASM binary size in bytes (always present on live /featured entries).';
COMMENT ON COLUMN template_registry.is_featured IS
    'Community metadata server''s own featured flag; NOT NULL DEFAULT false so rows created by internal/explorer (which never sets this column) are unambiguously "not known to be featured", not NULL/unknown.';
COMMENT ON COLUMN template_registry.metadata IS
    'Opaque JSONB blob mirroring the community metadata server''s "metadata" field verbatim - this is where tags/category actually live per a live-confirmed non-null example (TariStableCoin, esmeralda), but the real server-side struct definition could not be located in tari-project/tari-cli, so it is intentionally NOT decomposed into structured columns - see this migration file''s header comment.';
COMMENT ON COLUMN template_registry.definition IS
    'Opaque JSONB blob mirroring the community metadata server''s "definition" field verbatim (the CBOR-decoded TemplateDef ABI, when published) - null on /featured list entries, populated when fetched via the per-address route; not decomposed into structured columns for the same reason as metadata above.';
