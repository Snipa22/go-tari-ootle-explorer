// Package explorer polls internal/indexerclient and upserts the results into
// Postgres (via internal/db) - the indexer-polling subsystem for v1's
// ootle_blocks/validators/template_registry tables, per AGENTS.md's build order.
//
// Two modes, matching the same structural split as go-tari-explorer's own
// internal/indexer (see https://github.com/Snipa22/go-tari-explorer/blob/main/internal/indexer/indexer.go):
//   - Backfill(ctx): one-shot. Walks the ENTIRE template catalogue by page until
//     exhausted, and captures the current validator roster and latest epoch
//     checkpoint once each.
//   - Follow(ctx, pollInterval): repeatedly re-polls validators + the latest
//     checkpoint + any NEW catalogue entries (via an in-memory cursor - see
//     Explorer.catalogueCursor) on pollInterval, until ctx is cancelled. This is a
//     simple polling loop, not a push/subscription-based follower - consistent with
//     go-tari-explorer's own "good enough starting point" convention for this same
//     Backfill/Follow split.
//
// Explorer depends on two narrow interfaces (IndexerClient, Store) rather than the
// concrete *indexerclient.Client/*db.DB types directly, so tests can exercise this
// package's polling/pagination/upsert logic against fakes without a live indexer or a
// real Postgres connection - see explorer_test.go.
package explorer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"time"

	"github.com/Snipa22/go-tari-ootle-explorer/internal/db"
	"github.com/Snipa22/go-tari-ootle-explorer/internal/indexerclient"
)

// CatalogueBatchSize is the page size requested per ListTemplateCatalogue call - the
// real indexer handler defaults to 20 and caps at 100 (see
// applications/tari_indexer/src/rest_api/handlers/templates.rs); 100 is used here to
// minimize the number of round trips a full Backfill needs.
const CatalogueBatchSize = 100

// IndexerClient is the subset of *indexerclient.Client's methods Explorer needs.
// *indexerclient.Client satisfies this interface as-is; tests supply a fake.
type IndexerClient interface {
	ListValidators(ctx context.Context, epoch *uint64) (*indexerclient.ListValidatorsResponse, error)
	GetLatestEpochCheckpoint(ctx context.Context) (*indexerclient.EpochCheckpoint, error)
	ListTemplateCatalogue(ctx context.Context, limit uint64, after string) (*indexerclient.ListTemplateCatalogueResponse, error)
}

// Store is the subset of *db.DB's methods Explorer needs to persist what it fetches.
// *db.DB satisfies this interface as-is; tests supply a fake.
type Store interface {
	UpsertValidator(ctx context.Context, v db.Validator) error
	UpsertOotleBlock(ctx context.Context, b db.OotleBlock) error
	UpsertTemplateRegistryEntry(ctx context.Context, t db.TemplateRegistryEntry) error
}

// Explorer bundles the dependencies needed to poll and persist. Construct with New.
type Explorer struct {
	Indexer IndexerClient
	Store   Store

	// catalogueCursor is the template_address of the last catalogue entry this
	// Explorer has observed (the real API's own pagination cursor - see
	// indexerclient.Client.ListTemplateCatalogue's doc comment on why this is a
	// cursor, not an offset). Backfill walks from "" (the beginning) every time it's
	// called; Follow advances this field across iterations so each poll only fetches
	// entries new since the last one, rather than re-walking the whole catalogue on
	// every tick. Deliberately in-memory only for v1 - not persisted across process
	// restarts, so a restarted Follow re-walks the full catalogue once on its first
	// iteration (safe: every upsert is idempotent on template_address).
	catalogueCursor string
}

// New constructs an Explorer.
func New(indexer IndexerClient, store Store) *Explorer {
	return &Explorer{Indexer: indexer, Store: store}
}

// Backfill performs a one-shot full sync: walks the entire template catalogue from the
// beginning, and captures the current validator roster and latest epoch checkpoint
// once each. Safe to re-run (every write is an idempotent upsert).
func (e *Explorer) Backfill(ctx context.Context) error {
	if err := e.syncValidators(ctx); err != nil {
		return err
	}
	if err := e.syncLatestCheckpoint(ctx); err != nil {
		return err
	}
	n, lastAddr, err := e.walkTemplateCatalogue(ctx, "")
	if err != nil {
		return err
	}
	e.catalogueCursor = lastAddr
	log.Printf("explorer: backfill: synced %d template catalogue entries", n)
	return nil
}

// Follow repeatedly re-polls validators + the latest checkpoint + any new catalogue
// entries (since the last iteration's cursor - see Explorer.catalogueCursor) every
// pollInterval, until ctx is cancelled. A single poll failure is logged and retried on
// the next tick rather than aborting the whole loop, matching go-tari-explorer's own
// Follow convention of treating a single failed poll as transient.
func (e *Explorer) Follow(ctx context.Context, pollInterval time.Duration) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		if err := e.pollOnce(ctx); err != nil {
			log.Printf("explorer: follow: poll: %v (will retry)", err)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}

// pollOnce runs a single Follow iteration's work.
func (e *Explorer) pollOnce(ctx context.Context) error {
	if err := e.syncValidators(ctx); err != nil {
		return err
	}
	if err := e.syncLatestCheckpoint(ctx); err != nil {
		return err
	}
	n, lastAddr, err := e.walkTemplateCatalogue(ctx, e.catalogueCursor)
	if err != nil {
		return err
	}
	if lastAddr != "" {
		e.catalogueCursor = lastAddr
	}
	if n > 0 {
		log.Printf("explorer: follow: synced %d new template catalogue entries", n)
	}
	return nil
}

// syncValidators fetches the current validator roster and upserts every entry.
func (e *Explorer) syncValidators(ctx context.Context) error {
	resp, err := e.Indexer.ListValidators(ctx, nil)
	if err != nil {
		return fmt.Errorf("explorer: sync validators: %w", err)
	}
	for _, v := range resp.Validators {
		row := db.Validator{
			PublicKey:              v.PublicKey,
			LastSeenEpoch:          resp.Epoch,
			ShardGroupStart:        v.ShardGroup.Start,
			ShardGroupEndInclusive: v.ShardGroup.EndInclusive,
		}
		if err := e.Store.UpsertValidator(ctx, row); err != nil {
			return fmt.Errorf("explorer: sync validators: %w", err)
		}
	}
	return nil
}

// syncLatestCheckpoint fetches the latest epoch checkpoint and upserts it as a single
// ootle_blocks row - see db.OotleBlock's doc comment for the important caveats on
// BlockID/Timestamp/CommandCount this derivation carries.
func (e *Explorer) syncLatestCheckpoint(ctx context.Context) error {
	cp, err := e.Indexer.GetLatestEpochCheckpoint(ctx)
	if err != nil {
		return fmt.Errorf("explorer: sync latest checkpoint: %w", err)
	}
	row := db.OotleBlock{
		BlockID:      syntheticBlockID(cp.Header),
		Height:       cp.Header.Height,
		Epoch:        cp.Header.Epoch,
		Timestamp:    time.Now().Unix(),
		CommandCount: 0,
	}
	if err := e.Store.UpsertOotleBlock(ctx, row); err != nil {
		return fmt.Errorf("explorer: sync latest checkpoint: %w", err)
	}
	return nil
}

// walkTemplateCatalogue walks GET /templates/catalogue page by page, starting after
// startAfter (empty string means "from the beginning"), upserting every entry it
// finds, until a short page (fewer than CatalogueBatchSize entries) signals the
// catalogue is exhausted. Returns the number of entries upserted and the
// template_address of the last entry seen (empty if none were seen at all).
func (e *Explorer) walkTemplateCatalogue(ctx context.Context, startAfter string) (count int, lastAddr string, err error) {
	after := startAfter
	for {
		resp, err := e.Indexer.ListTemplateCatalogue(ctx, CatalogueBatchSize, after)
		if err != nil {
			return count, lastAddr, fmt.Errorf("explorer: walk template catalogue: %w", err)
		}
		if len(resp.Entries) == 0 {
			break
		}
		for _, entry := range resp.Entries {
			row := db.TemplateRegistryEntry{
				TemplateAddress: entry.TemplateAddress,
				TemplateName:    entry.TemplateName,
				AuthorPublicKey: entry.AuthorPublicKey,
				BinaryHash:      entry.BinaryHash,
				AtEpoch:         entry.AtEpoch,
				MetadataHash:    entry.MetadataHash,
			}
			if err := e.Store.UpsertTemplateRegistryEntry(ctx, row); err != nil {
				return count, lastAddr, fmt.Errorf("explorer: walk template catalogue: %w", err)
			}
			count++
			lastAddr = entry.TemplateAddress
		}
		after = lastAddr
		if uint64(len(resp.Entries)) < CatalogueBatchSize {
			// A short page means we've reached the end - the real handler never
			// returns fewer than the requested limit unless it ran out of rows (see
			// list_template_catalogue in templates.rs: it's a plain LIMIT query, not
			// a filter that could return a short page with more rows still behind
			// it).
			break
		}
	}
	return count, lastAddr, nil
}

// syntheticBlockID computes this repo's own stable identifier for an ootle_blocks row
// derived from an epoch checkpoint header - see db.OotleBlock's doc comment for why
// this is necessary (the real upstream header has no id field) and why SHA-256 over
// the header's own committed fields is good enough for v1: it's deterministic (the
// same checkpoint always yields the same id, so re-observing it is a true upsert no-op
// rather than a spurious duplicate row) and collision-safe in practice (a collision
// would require two genuinely different checkpoints - different epoch, height, shard
// group, or Merkle roots - to hash identically).
func syntheticBlockID(h indexerclient.EpochCheckpointHeader) string {
	sum := sha256.New()
	fmt.Fprintf(sum, "%d|%d|%d|%d|%d|%s|%s|%s|%s|%s|%s",
		h.Network, h.Epoch, h.Height, h.ShardGroup.Start, h.ShardGroup.EndInclusive,
		h.ParentID, h.JustifyID, h.CommandMerkleRoot, h.StateMerkleRoot, h.EpochHash, h.ProposedBy,
	)
	return hex.EncodeToString(sum.Sum(nil))
}
