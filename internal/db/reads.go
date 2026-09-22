package db

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// This file adds the read-side query methods internal/server (this dispatch) needs
// against the ootle_blocks/validators/burn_claims/template_registry tables - the
// read-side counterpart to rows.go's write-side upsert methods. Per DISPATCH_BRIEF.md:
// no read/list methods existed before this dispatch, since internal/server is the
// first actual consumer of reads against these tables (every earlier subsystem only
// ever wrote).

// ---- ootle_blocks ----

// ListOotleBlocks returns up to limit ootle_blocks rows with height < beforeHeight,
// ordered by height DESC (most recent first). This is the same "before cursor +
// DESC order" pagination shape go-tari-explorer's own blocks list uses for its HTMX
// "load more" pattern (see internal/server) - pass math.MaxInt64 as beforeHeight for
// the first page.
func (d *DB) ListOotleBlocks(ctx context.Context, beforeHeight int64, limit int) ([]OotleBlock, error) {
	rows, err := d.Pool.Query(ctx, `
		SELECT block_id, height, epoch, "timestamp", command_count
		FROM ootle_blocks
		WHERE height < $1
		ORDER BY height DESC
		LIMIT $2
	`, beforeHeight, limit)
	if err != nil {
		return nil, fmt.Errorf("db: list ootle blocks: %w", err)
	}
	defer rows.Close()

	var out []OotleBlock
	for rows.Next() {
		b, err := scanOotleBlock(rows)
		if err != nil {
			return nil, fmt.Errorf("db: list ootle blocks: scan: %w", err)
		}
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: list ootle blocks: %w", err)
	}
	return out, nil
}

// GetOotleBlock returns a single ootle_blocks row by block_id. Returns an error
// wrapping pgx.ErrNoRows (via errors.Is) if no such row exists.
func (d *DB) GetOotleBlock(ctx context.Context, blockID string) (OotleBlock, error) {
	row := d.Pool.QueryRow(ctx, `
		SELECT block_id, height, epoch, "timestamp", command_count
		FROM ootle_blocks WHERE block_id = $1
	`, blockID)
	b, err := scanOotleBlock(row)
	if err != nil {
		return OotleBlock{}, fmt.Errorf("db: get ootle block %s: %w", blockID, err)
	}
	return b, nil
}

// rowScanner is the subset of pgx.Row/pgx.Rows both interfaces share, letting the
// scanOne* helpers below serve both a single-row (QueryRow) and multi-row (Query)
// caller without duplicating the column list.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanOotleBlock(s rowScanner) (OotleBlock, error) {
	var b OotleBlock
	err := s.Scan(&b.BlockID, &b.Height, &b.Epoch, &b.Timestamp, &b.CommandCount)
	return b, err
}

// ---- validators ----

// ValidatorRow is the read-side shape for a single validators row: unlike
// db.Validator (rows.go, internal/explorer's write-side struct, which never touches
// consensus_status/last_checked_at at all), this covers every column the table
// actually has - GET /validators (DISPATCH_BRIEF.md) needs consensus_status and
// last_checked_at displayed alongside the roster fields.
//
// JSON tags below (snake_case, matching this table's own column names) exist for
// internal/server's GET /api/validators - see db.OotleBlock's own doc comment
// (rows.go) for why (same reasoning, first direct-marshal consumer of this struct).
type ValidatorRow struct {
	PublicKey              string    `json:"public_key"`
	LastSeenEpoch          uint64    `json:"last_seen_epoch"`
	ShardGroupStart        uint32    `json:"shard_group_start"`
	ShardGroupEndInclusive uint32    `json:"shard_group_end_inclusive"`
	ConsensusStatus        *string   `json:"consensus_status"`
	LastCheckedAt          time.Time `json:"last_checked_at"`
}

func scanValidatorRow(s rowScanner) (ValidatorRow, error) {
	var v ValidatorRow
	err := s.Scan(&v.PublicKey, &v.LastSeenEpoch, &v.ShardGroupStart, &v.ShardGroupEndInclusive, &v.ConsensusStatus, &v.LastCheckedAt)
	return v, err
}

// ListValidators returns every validators row, ordered by last_seen_epoch DESC then
// public_key ASC (most recently active first, stable tie-break) - the full roster
// view GET /validators (DISPATCH_BRIEF.md) needs. The validators table only ever
// holds ONE row per validator (see rows.go's Validator doc comment) - unlike
// ootle_blocks/burn_claims/template_registry, a real-world roster (dozens to low
// hundreds of validators) is small enough that this has no pagination cursor for v1.
func (d *DB) ListValidators(ctx context.Context) ([]ValidatorRow, error) {
	rows, err := d.Pool.Query(ctx, `
		SELECT public_key, last_seen_epoch, shard_group_start, shard_group_end_inclusive, consensus_status, last_checked_at
		FROM validators
		ORDER BY last_seen_epoch DESC, public_key ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("db: list validators: %w", err)
	}
	defer rows.Close()

	var out []ValidatorRow
	for rows.Next() {
		v, err := scanValidatorRow(rows)
		if err != nil {
			return nil, fmt.Errorf("db: list validators: scan: %w", err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: list validators: %w", err)
	}
	return out, nil
}

// GetValidator returns a single validators row by public_key. Returns an error
// wrapping pgx.ErrNoRows (via errors.Is) if no such row exists.
func (d *DB) GetValidator(ctx context.Context, publicKey string) (ValidatorRow, error) {
	row := d.Pool.QueryRow(ctx, `
		SELECT public_key, last_seen_epoch, shard_group_start, shard_group_end_inclusive, consensus_status, last_checked_at
		FROM validators WHERE public_key = $1
	`, publicKey)
	v, err := scanValidatorRow(row)
	if err != nil {
		return ValidatorRow{}, fmt.Errorf("db: get validator %s: %w", publicKey, err)
	}
	return v, nil
}

// LiveValidatorEpochSummary is the home page's calm, liveness-focused validator
// summary per DISPATCH_BRIEF.md: "count of validators seen in the current epoch, NOT
// a dump of every historical row" - the highest last_seen_epoch currently in the
// table, plus how many validators share it.
type LiveValidatorEpochSummary struct {
	Epoch uint64
	Count int
}

// GetLiveValidatorEpochSummary computes LiveValidatorEpochSummary from the
// validators table's current MAX(last_seen_epoch). Returns a zero-value summary
// (Epoch=0, Count=0), never an error, when the table is empty - an empty validators
// table (e.g. a freshly migrated DB with no explorer/vnhealth poll run yet) is a
// valid "nothing observed yet" state for the home page to render around, not a query
// failure.
func (d *DB) GetLiveValidatorEpochSummary(ctx context.Context) (LiveValidatorEpochSummary, error) {
	var maxEpoch *uint64
	if err := d.Pool.QueryRow(ctx, `SELECT MAX(last_seen_epoch) FROM validators`).Scan(&maxEpoch); err != nil {
		return LiveValidatorEpochSummary{}, fmt.Errorf("db: get live validator epoch summary: max epoch: %w", err)
	}
	if maxEpoch == nil {
		return LiveValidatorEpochSummary{}, nil
	}
	var count int
	if err := d.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM validators WHERE last_seen_epoch = $1`, *maxEpoch).Scan(&count); err != nil {
		return LiveValidatorEpochSummary{}, fmt.Errorf("db: get live validator epoch summary: count: %w", err)
	}
	return LiveValidatorEpochSummary{Epoch: *maxEpoch, Count: count}, nil
}

// ---- burn_claims ----

// ListBurnClaims returns burn_claims rows, most-recently-mined first (ORDER BY
// burn_height DESC) - the OPPOSITE order from the write-side ListBurnClaimsByStatus
// (rows.go, ASC/oldest-first, since that ordering serves internal/burnclaim's own
// oldest-overdue-first PROCESSING concern). This is a browsing/display list for
// GET /burn-claims (DISPATCH_BRIEF.md), where most-recent-first is the more useful
// default. status filters to exactly one of 'pending'/'claimed'/'stuck' when
// non-empty; an empty status returns every row regardless of status, matching
// DISPATCH_BRIEF.md's "defaulting to showing all" requirement for the ?status= query
// param.
func (d *DB) ListBurnClaims(ctx context.Context, status string) ([]BurnClaim, error) {
	var rows pgx.Rows
	var err error
	if status == "" {
		rows, err = d.Pool.Query(ctx, `
			SELECT l1_burn_tx_hash, commitment, claim_public_key, burn_height, claim_tx_id, claimed_at, status
			FROM burn_claims
			ORDER BY burn_height DESC
		`)
	} else {
		rows, err = d.Pool.Query(ctx, `
			SELECT l1_burn_tx_hash, commitment, claim_public_key, burn_height, claim_tx_id, claimed_at, status
			FROM burn_claims
			WHERE status = $1
			ORDER BY burn_height DESC
		`, status)
	}
	if err != nil {
		return nil, fmt.Errorf("db: list burn claims (status=%q): %w", status, err)
	}
	defer rows.Close()

	var out []BurnClaim
	for rows.Next() {
		b, err := scanBurnClaim(rows)
		if err != nil {
			return nil, fmt.Errorf("db: list burn claims (status=%q): scan: %w", status, err)
		}
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: list burn claims (status=%q): %w", status, err)
	}
	return out, nil
}

// GetBurnClaim returns a single burn_claims row by l1_burn_tx_hash. Returns an error
// wrapping pgx.ErrNoRows (via errors.Is) if no such row exists.
func (d *DB) GetBurnClaim(ctx context.Context, l1BurnTxHash string) (BurnClaim, error) {
	row := d.Pool.QueryRow(ctx, `
		SELECT l1_burn_tx_hash, commitment, claim_public_key, burn_height, claim_tx_id, claimed_at, status
		FROM burn_claims WHERE l1_burn_tx_hash = $1
	`, l1BurnTxHash)
	b, err := scanBurnClaim(row)
	if err != nil {
		return BurnClaim{}, fmt.Errorf("db: get burn claim %s: %w", l1BurnTxHash, err)
	}
	return b, nil
}

func scanBurnClaim(s rowScanner) (BurnClaim, error) {
	var b BurnClaim
	err := s.Scan(&b.L1BurnTxHash, &b.Commitment, &b.ClaimPublicKey, &b.BurnHeight, &b.ClaimTxID, &b.ClaimedAt, &b.Status)
	return b, err
}

// ---- template_registry ----

// TemplateRegistryRow is the full read-side shape for a single template_registry
// row. Unlike TemplateRegistryEntry/TemplateRegistryMetadataEntry (rows.go, each a
// PARTIAL write-side shape for one specific upstream source - see those types' doc
// comments), this covers every column the table actually has, merged from both
// sources - GET /templates (DISPATCH_BRIEF.md: "template_address, template_name,
// author, at_epoch, is_featured, code_size") needs fields from both.
type TemplateRegistryRow struct {
	TemplateAddress    string
	TemplateName       string
	AuthorPublicKey    string
	BinaryHash         string
	AtEpoch            uint64
	MetadataHash       *string
	AuthorFriendlyName *string
	CodeSize           *int64
	IsFeatured         bool
	Metadata           []byte
	Definition         []byte
	FirstSeenAt        time.Time
}

const templateRegistryColumns = `
	template_address, template_name, author_public_key, binary_hash, at_epoch,
	metadata_hash, author_friendly_name, code_size, is_featured, metadata, definition, first_seen_at
`

func scanTemplateRegistryRow(s rowScanner) (TemplateRegistryRow, error) {
	var t TemplateRegistryRow
	err := s.Scan(
		&t.TemplateAddress, &t.TemplateName, &t.AuthorPublicKey, &t.BinaryHash, &t.AtEpoch,
		&t.MetadataHash, &t.AuthorFriendlyName, &t.CodeSize, &t.IsFeatured, &t.Metadata, &t.Definition, &t.FirstSeenAt,
	)
	return t, err
}

// ListTemplateRegistry returns up to limit template_registry rows with
// first_seen_at < beforeFirstSeenAt, ordered by first_seen_at DESC (most recently
// observed first) - the same before-cursor + DESC pagination shape ListOotleBlocks
// uses, for GET /templates' HTMX "load more" list and the home page's "recent
// template publishes" panel (pass time.Now() as the sentinel for the first page).
func (d *DB) ListTemplateRegistry(ctx context.Context, beforeFirstSeenAt time.Time, limit int) ([]TemplateRegistryRow, error) {
	rows, err := d.Pool.Query(ctx, `
		SELECT `+templateRegistryColumns+`
		FROM template_registry
		WHERE first_seen_at < $1
		ORDER BY first_seen_at DESC
		LIMIT $2
	`, beforeFirstSeenAt, limit)
	if err != nil {
		return nil, fmt.Errorf("db: list template registry: %w", err)
	}
	defer rows.Close()

	var out []TemplateRegistryRow
	for rows.Next() {
		t, err := scanTemplateRegistryRow(rows)
		if err != nil {
			return nil, fmt.Errorf("db: list template registry: scan: %w", err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: list template registry: %w", err)
	}
	return out, nil
}

// GetTemplateRegistryEntry returns a single template_registry row by
// template_address. Returns an error wrapping pgx.ErrNoRows (via errors.Is) if no
// such row exists.
func (d *DB) GetTemplateRegistryEntry(ctx context.Context, templateAddress string) (TemplateRegistryRow, error) {
	row := d.Pool.QueryRow(ctx, `
		SELECT `+templateRegistryColumns+`
		FROM template_registry WHERE template_address = $1
	`, templateAddress)
	t, err := scanTemplateRegistryRow(row)
	if err != nil {
		return TemplateRegistryRow{}, fmt.Errorf("db: get template registry entry %s: %w", templateAddress, err)
	}
	return t, nil
}
