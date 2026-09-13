package db

import (
	"context"
	"fmt"
)

// This file adds the row shapes and upsert methods internal/explorer needs against the
// ootle_blocks/validators/template_registry tables (see migrations/0001_init.up.sql and
// migrations/0002_template_registry_correction.up.sql for the schema these mirror).
// Read-side query methods aren't added yet - v1's internal/server (a later dispatch per
// AGENTS.md's build order) is the first actual consumer of reads against these tables,
// so per this repo's "don't front-load speculative code" convention, only what
// internal/explorer needs to WRITE is added here.

// Validator is the row shape for the `validators` table: this repo's own per-epoch
// snapshot of a single validator's roster entry, keyed on public_key (see
// migrations/0001_init.up.sql - the table only ever holds the MOST RECENTLY seen
// roster entry per validator, not a full history).
//
// ConsensusStatus is deliberately NOT set by internal/explorer (always left as its
// zero value, nil, here) - it comes from tari_validator_node's JSON-RPC
// get_consensus_status, which internal/vnhealth (a separate, later dispatch per
// AGENTS.md) is responsible for. UpsertValidator's SQL reflects this: it never writes
// to consensus_status on either INSERT or UPDATE, so it can never clobber a value
// vnhealth already stored, regardless of which of the two subsystems happens to run
// first or more recently against a given validator's row.
type Validator struct {
	PublicKey              string
	LastSeenEpoch          uint64
	ShardGroupStart        uint32
	ShardGroupEndInclusive uint32 // see migrations/0002_template_registry_correction.up.sql for the shard_group_end -> shard_group_end_inclusive rename
}

// UpsertValidator inserts or updates a single validator roster row, keyed on
// public_key. Never touches consensus_status - see Validator's doc comment.
func (d *DB) UpsertValidator(ctx context.Context, v Validator) error {
	_, err := d.Pool.Exec(ctx, `
		INSERT INTO validators (public_key, last_seen_epoch, shard_group_start, shard_group_end_inclusive, last_checked_at)
		VALUES ($1, $2, $3, $4, now())
		ON CONFLICT (public_key) DO UPDATE SET
			last_seen_epoch = EXCLUDED.last_seen_epoch,
			shard_group_start = EXCLUDED.shard_group_start,
			shard_group_end_inclusive = EXCLUDED.shard_group_end_inclusive,
			last_checked_at = now()
	`, v.PublicKey, v.LastSeenEpoch, v.ShardGroupStart, v.ShardGroupEndInclusive)
	if err != nil {
		return fmt.Errorf("db: upsert validator %s: %w", v.PublicKey, err)
	}
	return nil
}

// OotleBlock is the row shape for the `ootle_blocks` table, populated by
// internal/explorer from GET /epoch-checkpoints/latest's commit-proof header - the
// closest real analog to a "block" tari_indexer's REST API exposes (there is no plain
// /blocks route - see migrations/0001_init.up.sql's own doc comment, which anticipated
// this).
//
// IMPORTANT CAVEAT, flagged per AGENTS.md's "don't guess, flag what you can't verify"
// rule (see internal/indexerclient.EpochCheckpointHeader's doc comment for the full
// primary-source verification behind this): the real checkpoint header has NEITHER a
// block id NOR a timestamp field, so:
//   - BlockID is a SYNTHETIC identifier this repo computes itself (see
//     syntheticBlockID in internal/explorer) - a SHA-256 hex digest over the header's
//     own committed fields. It is NOT the real upstream consensus BlockId (computing
//     that exactly would require replicating BlockHeader::calculate_block_id's hash
//     schema, which needs fields - total_leader_fee, extra_data, protocol_version -
//     this reduced wire header doesn't carry at all) but IS stable and collision-safe
//     enough to key this table's rows on for v1.
//   - Timestamp is this repo's own capture-time (time.Now().Unix() when
//     internal/explorer fetched the checkpoint), NOT a real block-creation timestamp -
//     there simply isn't one in this response. Callers should treat this column as
//     "when this explorer last observed the latest checkpoint", not "when this block
//     was produced". This is flagged in the dispatch report as a follow-up
//     schema/semantics gap (e.g. renaming the column to observed_at in a later
//     migration) rather than silently treated as a real block timestamp.
//   - CommandCount is always 0 here: the checkpoint's own committed command is a
//     single internal EndOfEpochCommand (crates/storage/src/consensus_models/
//     epoch_checkpoint.rs), not the shard group's actual per-block transaction
//     commands, so there is no meaningful "command count" to report from this
//     endpoint either. This is consistent with 0 rather than actively misleading, but
//     is not the same "number of Commands in the block body" semantic the column's
//     doc comment in 0001_init.up.sql originally described for a real consensus
//     block.
type OotleBlock struct {
	BlockID      string
	Height       uint64
	Epoch        uint64
	Timestamp    int64
	CommandCount int32
}

// UpsertOotleBlock inserts or updates a single ootle_blocks row, keyed on block_id.
func (d *DB) UpsertOotleBlock(ctx context.Context, b OotleBlock) error {
	_, err := d.Pool.Exec(ctx, `
		INSERT INTO ootle_blocks (block_id, height, epoch, "timestamp", command_count)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (block_id) DO UPDATE SET
			height = EXCLUDED.height,
			epoch = EXCLUDED.epoch,
			"timestamp" = EXCLUDED."timestamp",
			command_count = EXCLUDED.command_count
	`, b.BlockID, b.Height, b.Epoch, b.Timestamp, b.CommandCount)
	if err != nil {
		return fmt.Errorf("db: upsert ootle block %s: %w", b.BlockID, err)
	}
	return nil
}

// TemplateRegistryEntry is the row shape for the `template_registry` table, mirroring
// GET /templates/catalogue's real TemplateCatalogueItem shape - see
// migrations/0002_template_registry_correction.up.sql for the full derivation.
type TemplateRegistryEntry struct {
	TemplateAddress string
	TemplateName    string
	AuthorPublicKey string
	BinaryHash      string
	AtEpoch         uint64
	MetadataHash    *string // nil when unset upstream (see migration 0002's comment)
}

// UpsertTemplateRegistryEntry inserts or updates a single template_registry row, keyed
// on template_address. first_seen_at is only ever set on initial INSERT (via its
// column DEFAULT now()) - a re-observed template (e.g. re-walked during a later
// Backfill) keeps its original first_seen_at rather than having it bumped forward,
// which is why it's absent from the UPDATE SET clause below.
func (d *DB) UpsertTemplateRegistryEntry(ctx context.Context, t TemplateRegistryEntry) error {
	_, err := d.Pool.Exec(ctx, `
		INSERT INTO template_registry (template_address, template_name, author_public_key, binary_hash, at_epoch, metadata_hash)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (template_address) DO UPDATE SET
			template_name = EXCLUDED.template_name,
			author_public_key = EXCLUDED.author_public_key,
			binary_hash = EXCLUDED.binary_hash,
			at_epoch = EXCLUDED.at_epoch,
			metadata_hash = EXCLUDED.metadata_hash
	`, t.TemplateAddress, t.TemplateName, t.AuthorPublicKey, t.BinaryHash, t.AtEpoch, t.MetadataHash)
	if err != nil {
		return fmt.Errorf("db: upsert template registry entry %s: %w", t.TemplateAddress, err)
	}
	return nil
}
