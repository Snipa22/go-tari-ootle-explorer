package db

import (
	"context"
	"fmt"
	"time"
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

// ValidatorHealth is the row shape internal/vnhealth upserts into the SAME
// `validators` table UpsertValidator (above) writes to, from a completely different
// upstream source (tari_validator_node's own JSON-RPC, not the indexer's REST
// /validators route - see internal/vnhealth's doc comment). Every field except
// PublicKey is a pointer because a single vnhealth poll may only have SOME of them:
// e.g. get_identity succeeding but get_epoch_manager_stats/get_consensus_status
// failing (or returning a nil committee_info because the validator isn't registered
// yet) still gives vnhealth a public_key worth recording as "recently seen", just not
// a fresh last_seen_epoch/shard_group/consensus_status to go with it.
//
// UpsertValidatorHealth's SQL (below) merges rather than overwrites: a nil field here
// never clobbers a non-nil value already stored by EITHER source (explorer's
// UpsertValidator or a previous vnhealth poll) - see that method's own doc comment for
// the exact COALESCE mechanics and why EXCLUDED can't be used naively for this.
type ValidatorHealth struct {
	PublicKey              string
	LastSeenEpoch          *uint64
	ShardGroupStart        *uint32
	ShardGroupEndInclusive *uint32
	ConsensusStatus        *string
}

// UpsertValidatorHealth inserts or updates a single validators row from
// internal/vnhealth, keyed on public_key, WITHOUT letting a nil field on this
// specific call clobber a non-nil value already stored (by internal/explorer's
// UpsertValidator, or an earlier UpsertValidatorHealth call) for that same column.
//
// The mechanics: every nullable column's UPDATE assignment is
// `COALESCE($n, validators.column)` - referencing the QUERY PARAMETER directly, NOT
// `EXCLUDED.column`. This distinction matters and is easy to get wrong: if the INSERT
// VALUES clause used `COALESCE($n, 0)` (to satisfy the column's NOT NULL constraint
// for a genuinely brand-new row - see below) and the UPDATE clause then read
// `EXCLUDED.column`, EXCLUDED would already contain that substituted 0/default, not
// the original NULL, so `COALESCE(EXCLUDED.column, validators.column)` would
// incorrectly prefer the substituted default over the existing row's real value on
// every conflict. Reading the raw parameter directly in the UPDATE clause instead
// (Postgres allows referencing the same bind parameter multiple times in one
// statement) preserves the true "nil means unknown, don't touch" semantics through
// both branches.
//
// The INSERT branch's `COALESCE($n, 0)` (or `NULL` for consensus_status, which IS a
// nullable column per migrations/0001_init.up.sql) only matters for a genuinely new
// public_key vnhealth observes before internal/explorer ever has - last_seen_epoch/
// shard_group_start/shard_group_end_inclusive are NOT NULL on this table, so a
// brand-new row with no known epoch/shard-group yet gets 0 as an explicit
// "unknown, not yet observed" placeholder rather than failing the insert; a
// subsequent poll (from either source) with real data overwrites it via the same
// COALESCE-on-update path once it's known.
func (d *DB) UpsertValidatorHealth(ctx context.Context, v ValidatorHealth) error {
	_, err := d.Pool.Exec(ctx, `
		INSERT INTO validators (public_key, last_seen_epoch, shard_group_start, shard_group_end_inclusive, consensus_status, last_checked_at)
		VALUES ($1, COALESCE($2, 0), COALESCE($3, 0), COALESCE($4, 0), $5, now())
		ON CONFLICT (public_key) DO UPDATE SET
			last_seen_epoch = COALESCE($2, validators.last_seen_epoch),
			shard_group_start = COALESCE($3, validators.shard_group_start),
			shard_group_end_inclusive = COALESCE($4, validators.shard_group_end_inclusive),
			consensus_status = COALESCE($5, validators.consensus_status),
			last_checked_at = now()
	`, v.PublicKey, v.LastSeenEpoch, v.ShardGroupStart, v.ShardGroupEndInclusive, v.ConsensusStatus)
	if err != nil {
		return fmt.Errorf("db: upsert validator health %s: %w", v.PublicKey, err)
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

// TemplateRegistryMetadataEntry is the row shape internal/registry upserts into the
// SAME `template_registry` table UpsertTemplateRegistryEntry (above) writes to, from
// a completely different upstream source (the community template-metadata server's
// GET /community-templates/api/templates/featured, not the indexer's REST
// /templates/catalogue route - see internal/registry's doc comment and
// migrations/0004_template_registry_metadata_server_fields.up.sql for the live
// citation).
//
// The metadata server's response includes the same core fields the indexer catalogue
// does (template_address/template_name/author_public_key/binary_hash/at_epoch/
// metadata_hash) PLUS five fields the indexer catalogue doesn't have at all
// (AuthorFriendlyName/CodeSize/IsFeatured/Metadata/Definition). Every field below
// (other than the TemplateAddress key) is a pointer/nilable-JSON so
// UpsertTemplateRegistryMetadata can merge rather than overwrite - see that method's
// own doc comment for why, same reasoning as db.ValidatorHealth/
// UpsertValidatorHealth.
type TemplateRegistryMetadataEntry struct {
	TemplateAddress    string
	TemplateName       *string
	AuthorPublicKey    *string
	BinaryHash         *string
	AtEpoch            *uint64
	MetadataHash       *string
	AuthorFriendlyName *string
	CodeSize           *int64
	IsFeatured         *bool
	Metadata           []byte // opaque JSONB blob, nil when unset upstream - see migration 0004's comment
	Definition         []byte // opaque JSONB blob, nil when unset upstream - see migration 0004's comment
}

// UpsertTemplateRegistryMetadata inserts or updates a single template_registry row
// from internal/registry, keyed on template_address, WITHOUT letting a nil field on
// THIS call clobber a non-nil value already stored (by internal/explorer's
// UpsertTemplateRegistryEntry, or an earlier UpsertTemplateRegistryMetadata call) for
// that same column.
//
// Same mechanics as db.UpsertValidatorHealth (see that method's doc comment for the
// full reasoning): every column's UPDATE assignment is
// `COALESCE($n, template_registry.column)`, reading the RAW QUERY PARAMETER directly,
// NOT `EXCLUDED.column` - because the INSERT branch's own `COALESCE($n, empty-default)`-style
// defaults (needed to satisfy this table's NOT NULL columns for a genuinely new
// template_address internal/registry observes before internal/explorer ever does)
// would otherwise get read back by an EXCLUDED-based UPDATE clause and incorrectly
// preferred over a real, already-stored value on every conflict.
//
// This is deliberately used for EVERY shared column (template_name/author_public_key/
// binary_hash/at_epoch/metadata_hash), not just the five metadata-server-only ones:
// DISPATCH_BRIEF.md's core concern - "don't let one source's upsert null out fields
// only the other source populates" - cuts both ways for metadata_hash in particular
// (a real, non-pointer-shared field on BOTH the indexer catalogue's
// TemplateCatalogueItem and this server's response, but observed null on every live
// metadata-server entry checked this session while some indexer-catalogue entries do
// carry a real value - see 0002_template_registry_correction's own note on that
// field). Using COALESCE uniformly means whichever source most recently saw a REAL
// (non-nil) value for a shared column wins, and neither source can blank out the
// other's value by reporting nil for a field it simply doesn't have data for this
// round.
func (d *DB) UpsertTemplateRegistryMetadata(ctx context.Context, t TemplateRegistryMetadataEntry) error {
	_, err := d.Pool.Exec(ctx, `
		INSERT INTO template_registry (
			template_address, template_name, author_public_key, binary_hash, at_epoch,
			metadata_hash, author_friendly_name, code_size, is_featured, metadata, definition
		)
		VALUES (
			$1, COALESCE($2, ''), COALESCE($3, ''), COALESCE($4, ''), COALESCE($5, 0),
			$6, $7, $8, COALESCE($9, false), $10, $11
		)
		ON CONFLICT (template_address) DO UPDATE SET
			template_name = COALESCE($2, template_registry.template_name),
			author_public_key = COALESCE($3, template_registry.author_public_key),
			binary_hash = COALESCE($4, template_registry.binary_hash),
			at_epoch = COALESCE($5, template_registry.at_epoch),
			metadata_hash = COALESCE($6, template_registry.metadata_hash),
			author_friendly_name = COALESCE($7, template_registry.author_friendly_name),
			code_size = COALESCE($8, template_registry.code_size),
			is_featured = COALESCE($9, template_registry.is_featured),
			metadata = COALESCE($10, template_registry.metadata),
			definition = COALESCE($11, template_registry.definition)
	`,
		t.TemplateAddress, t.TemplateName, t.AuthorPublicKey, t.BinaryHash, t.AtEpoch,
		t.MetadataHash, t.AuthorFriendlyName, t.CodeSize, t.IsFeatured, t.Metadata, t.Definition,
	)
	if err != nil {
		return fmt.Errorf("db: upsert template registry metadata %s: %w", t.TemplateAddress, err)
	}
	return nil
}

// BurnClaim is the row shape for the `burn_claims` table - see
// migrations/0003_burn_claims_correction.up.sql for the full real-source derivation
// of every column (in particular: why ClaimPublicKey is a pointer/always nil coming
// from internal/burnclaim's L1 scanner, and why Commitment - not just
// L1BurnTxHash - is needed at all).
type BurnClaim struct {
	L1BurnTxHash   string
	Commitment     string
	ClaimPublicKey *string
	BurnHeight     uint64
	ClaimTxID      *string
	ClaimedAt      *time.Time
	Status         string
}

// InsertPendingBurnClaim inserts a single newly-observed L1 burn as a 'pending' row,
// keyed on l1_burn_tx_hash (the burn kernel's hash - see migration 0003's comment).
// A conflict on EITHER l1_burn_tx_hash or commitment (both UNIQUE) is treated as
// "already recorded, nothing to do" rather than an error: internal/burnclaim's L1
// scanner re-scans overlapping height ranges across Follow ticks by design (see that
// package's doc comment), so re-observing the same real burn is an expected, frequent
// event, not a bug.
func (d *DB) InsertPendingBurnClaim(ctx context.Context, b BurnClaim) error {
	_, err := d.Pool.Exec(ctx, `
		INSERT INTO burn_claims (l1_burn_tx_hash, commitment, burn_height, status)
		VALUES ($1, $2, $3, 'pending')
		ON CONFLICT (l1_burn_tx_hash) DO NOTHING
	`, b.L1BurnTxHash, b.Commitment, b.BurnHeight)
	if err != nil {
		return fmt.Errorf("db: insert pending burn claim %s: %w", b.L1BurnTxHash, err)
	}
	return nil
}

// ListBurnClaimsByStatus returns every burn_claims row with the given status
// ('pending', 'claimed', or 'stuck'), ordered by burn_height so callers processing a
// large backlog see older (more overdue) burns first.
func (d *DB) ListBurnClaimsByStatus(ctx context.Context, status string) ([]BurnClaim, error) {
	rows, err := d.Pool.Query(ctx, `
		SELECT l1_burn_tx_hash, commitment, claim_public_key, burn_height, claim_tx_id, claimed_at, status
		FROM burn_claims
		WHERE status = $1
		ORDER BY burn_height ASC
	`, status)
	if err != nil {
		return nil, fmt.Errorf("db: list burn claims by status %s: %w", status, err)
	}
	defer rows.Close()

	var out []BurnClaim
	for rows.Next() {
		var b BurnClaim
		if err := rows.Scan(&b.L1BurnTxHash, &b.Commitment, &b.ClaimPublicKey, &b.BurnHeight, &b.ClaimTxID, &b.ClaimedAt, &b.Status); err != nil {
			return nil, fmt.Errorf("db: list burn claims by status %s: scan: %w", status, err)
		}
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: list burn claims by status %s: %w", status, err)
	}
	return out, nil
}

// MarkBurnClaimed transitions a burn_claims row to status='claimed', keyed on
// commitment (not l1_burn_tx_hash) since that's what internal/burnclaim's L2 checker
// looks the row up by (it queries the indexer using the commitment, has no kernel
// hash to hand at that point). claimedAt is this repo's own OBSERVATION time, not a
// real upstream claim timestamp - see migration 0003's comment on why the substate
// this is derived from carries no timestamp at all. A no-op (0 rows affected) if the
// commitment is unknown or already claimed; callers don't need to treat that as an
// error - see internal/burnclaim's own doc comment.
func (d *DB) MarkBurnClaimed(ctx context.Context, commitment string, claimedAt time.Time) error {
	_, err := d.Pool.Exec(ctx, `
		UPDATE burn_claims SET status = 'claimed', claimed_at = $2
		WHERE commitment = $1 AND status != 'claimed'
	`, commitment, claimedAt)
	if err != nil {
		return fmt.Errorf("db: mark burn claimed %s: %w", commitment, err)
	}
	return nil
}

// MarkBurnStuck transitions a burn_claims row from 'pending' to 'stuck', keyed on
// l1_burn_tx_hash. Deliberately only touches rows still 'pending' (never overwrites
// an already-'claimed' row, even one that took long enough to look stuck by the time
// it was actually claimed) - see internal/burnclaim's stuck-detection doc comment for
// why a claim can legitimately race a stuck-detection pass.
func (d *DB) MarkBurnStuck(ctx context.Context, l1BurnTxHash string) error {
	_, err := d.Pool.Exec(ctx, `
		UPDATE burn_claims SET status = 'stuck'
		WHERE l1_burn_tx_hash = $1 AND status = 'pending'
	`, l1BurnTxHash)
	if err != nil {
		return fmt.Errorf("db: mark burn stuck %s: %w", l1BurnTxHash, err)
	}
	return nil
}
