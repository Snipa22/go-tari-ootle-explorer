-- 0003_burn_claims_correction: fixes burn_claims to match what internal/burnclaim (this
-- dispatch) actually needs and can actually populate, per real primary-source
-- verification (tari-project/tari-ootle commit d89dc92, 2026-09-11; tari-project/tari
-- commit af19c0d, 2026-09-11) - see DISPATCH_BRIEF.md and the dispatch report for the
-- full citations. burn_claims has never held real data (internal/burnclaim, its only
-- writer, is introduced in this same dispatch), so this is a full drop+recreate, same
-- convention as 0002_template_registry_correction used for template_registry.
--
-- Two real corrections to 0001_init's guessed shape:
--
--   1. claim_public_key cannot be populated from a plain L1 base-node block scan.
--      Confirmed by reading the real wallet-side claim flow
--      (integration_tests/tests/steps/wallet.rs's get_burn_claim_proof call, and
--      applications/tari_walletd/src/services/auto_claim_burn_service.rs): the
--      stealth claim public key S (what the claim-burn.mdx doc's "Anatomy of a burn"
--      section calls burn_public_key) is derived from a nonce R and the claimant's
--      OWN account key P via a wallet-daemon RPC (GetBurnClaimProofRequest) - it is
--      NOT a field carried on the plain L1 TransactionOutput/TransactionKernel a
--      base-node GRPC block scan sees (go-tari-grpc-lib's OutputFeatures/
--      TransactionKernel messages have no such field). internal/burnclaim's L1
--      scanner therefore always leaves this column NULL - DROP NOT NULL.
--   2. The L2 side needs the RAW burn commitment C, not just an L1-identifying hash,
--      to derive the L2 ClaimedOutputTombstoneAddress substate id ("tombstone_" +
--      hex(C) - confirmed against
--      crates/template_lib_types/src/substates/claimed_output_tombstone.rs's
--      ClaimedOutputTombstoneAddress::from_commitment, which maps the 32-byte
--      commitment directly into the 32-byte ObjectKey with no further hashing) and
--      check via GET /substates/{substate_id} whether that burn has been claimed.
--      0001_init had no column for this at all - ADD COLUMN commitment.
--
-- l1_burn_tx_hash is populated from the burn transaction's KERNEL hash
-- (TransactionKernel.hash, tari_protos/transaction.proto) - the closest real L1
-- per-burn identifier that exists, since Mimblewimble has no plain "transaction hash"
-- the way account-based chains do (same convention go-tari-explorer's own L1 kernels
-- table uses kernel.Hash as its identifying column). Confirmed real per
-- aggregate_body_chain_validator.rs's check_total_burned: a burn kernel's
-- burn_commitment is REQUIRED to equal its matching burned output's commitment
-- (consensus rule, both directions - "Burned kernel does not match burned output" /
-- "Burned output has no matching burned kernel"), so scanning kernels alone (rather
-- than also cross-referencing outputs) is sufficient to get both l1_burn_tx_hash
-- (kernel.hash) and commitment (kernel.burn_commitment) for every real burn.

DROP TABLE IF EXISTS burn_claims;

CREATE TABLE burn_claims (
    l1_burn_tx_hash   TEXT PRIMARY KEY,       -- hex TransactionKernel.hash of the burn kernel (see note above)
    commitment        TEXT NOT NULL UNIQUE,   -- hex burn commitment C (TransactionKernel.burn_commitment); preimage of the L2 tombstone_<hex> substate id
    claim_public_key  TEXT,                   -- nullable: not extractable from a plain L1 block scan, see note above
    burn_height       BIGINT NOT NULL,        -- L1 block height the burn kernel was mined at
    claim_tx_id       TEXT,                   -- L2 claim transaction id; the tombstone substate lookup this repo uses doesn't expose one, so this stays NULL even once status='claimed' (see internal/burnclaim's doc comment)
    claimed_at        TIMESTAMPTZ,            -- NULL until claimed; set to OBSERVATION time when the L2 checker first sees the tombstone exist, not a real upstream claim timestamp (ClaimedOutputTombstone carries no timestamp field at all - crates/engine_types/src/confidential/unclaimed.rs)
    status            TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'claimed', 'stuck'))
);

CREATE INDEX IF NOT EXISTS idx_burn_claims_status ON burn_claims (status);
