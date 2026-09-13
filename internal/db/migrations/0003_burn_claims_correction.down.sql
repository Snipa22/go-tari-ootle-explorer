-- 0003_burn_claims_correction down: restore burn_claims to 0001_init's original
-- (guessed, incorrect) shape. burn_claims has never held real data so this is a clean
-- drop+recreate, same as the up migration.

DROP TABLE IF EXISTS burn_claims;

CREATE TABLE IF NOT EXISTS burn_claims (
    l1_burn_tx_hash   TEXT PRIMARY KEY,
    claim_public_key  TEXT NOT NULL,
    burn_height       BIGINT NOT NULL,
    claim_tx_id       TEXT,
    claimed_at        TIMESTAMPTZ,
    status            TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'claimed', 'stuck'))
);

CREATE INDEX IF NOT EXISTS idx_burn_claims_status ON burn_claims (status);
