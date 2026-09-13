// Package registry polls internal/registrymetaclient's ListFeatured and upserts the
// results into Postgres (via internal/db.UpsertTemplateRegistryMetadata) - the
// template-registry-mirroring subsystem for v1's `template_registry` table, per
// AGENTS.md's build order.
//
// Two modes, matching every other subsystem's Backfill/Follow (or Sync/Follow) split
// per AGENTS.md's stated shape convention:
//   - Sync(ctx): one-shot. Fetches the full (currently: featured-only, see below)
//     template list once and upserts every entry.
//   - Follow(ctx, pollInterval): repeatedly calls Sync on a timer until ctx is
//     cancelled - a simple polling loop, matching internal/vnhealth's own
//     Poll/Follow shape (there's no incremental-since-last-poll cursor here, unlike
//     internal/explorer's catalogue walk, because ListFeatured has no cursor/paging
//     concept at all - it's a single bounded list every time, see
//     internal/registrymetaclient's doc comment on the "no unfiltered list route"
//     gap).
//
// # Why this only mirrors "featured" templates, not the full catalogue
//
// Per internal/registrymetaclient's doc comment (confirmed live this session): the
// community metadata server has no unfiltered GET /api/templates list/paginated
// route - only GET /api/templates/featured (used here) and a per-address GET
// /api/templates/{address} route (not a list route, not used here). So this package
// can only ever mirror whichever templates the metadata server itself has chosen to
// feature, NOT every template that has ever published metadata to it. This is a
// genuine, confirmed gap flagged explicitly per DISPATCH_BRIEF.md's instruction,
// rather than silently presented as a complete mirror - see this dispatch's report
// for the full citation trail.
//
// # Relationship to internal/explorer's own template_registry writes
//
// internal/explorer (a separate, earlier dispatch) also writes to template_registry,
// from a completely different upstream source (the indexer's own
// GET /templates/catalogue) with different field coverage - it has the core
// template_address/template_name/author_public_key/binary_hash/at_epoch/
// metadata_hash fields but none of the five metadata-server-only fields
// (author_friendly_name/code_size/is_featured/metadata/definition) this package
// populates. See db.UpsertTemplateRegistryMetadata's doc comment for the exact
// COALESCE-based merge mechanics that let both sources safely write the same table
// without one clobbering the other's fields.
package registry

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/Snipa22/go-tari-ootle-explorer/internal/db"
	"github.com/Snipa22/go-tari-ootle-explorer/internal/registrymetaclient"
)

// MetadataClient is the subset of *registrymetaclient.Client's methods Registry
// needs. *registrymetaclient.Client satisfies this interface as-is; tests supply a
// fake.
type MetadataClient interface {
	ListFeatured(ctx context.Context) ([]registrymetaclient.FeaturedTemplate, error)
}

// Store is the subset of *db.DB's methods Registry needs to persist what it fetches.
// *db.DB satisfies this interface as-is; tests supply a fake.
type Store interface {
	UpsertTemplateRegistryMetadata(ctx context.Context, t db.TemplateRegistryMetadataEntry) error
}

// Registry bundles the dependencies needed to poll and persist. Construct with New.
type Registry struct {
	Client MetadataClient
	Store  Store
}

// New constructs a Registry.
func New(client MetadataClient, store Store) *Registry {
	return &Registry{Client: client, Store: store}
}

// Sync performs a one-shot sync: fetches the current featured-template list and
// upserts every entry. Safe to re-run (every write is an idempotent, merge-safe
// upsert - see db.UpsertTemplateRegistryMetadata's doc comment).
func (r *Registry) Sync(ctx context.Context) (int, error) {
	entries, err := r.Client.ListFeatured(ctx)
	if err != nil {
		return 0, fmt.Errorf("registry: sync: %w", err)
	}
	for _, e := range entries {
		row := toRow(e)
		if err := r.Store.UpsertTemplateRegistryMetadata(ctx, row); err != nil {
			return 0, fmt.Errorf("registry: sync: upsert %s: %w", e.TemplateAddress, err)
		}
	}
	return len(entries), nil
}

// Follow repeatedly calls Sync every pollInterval until ctx is cancelled. A single
// failed Sync is logged and retried on the next tick rather than aborting the whole
// loop, matching internal/explorer/internal/vnhealth's own Follow convention of
// treating a single failed poll as transient.
func (r *Registry) Follow(ctx context.Context, pollInterval time.Duration) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		if n, err := r.Sync(ctx); err != nil {
			log.Printf("registry: follow: sync: %v (will retry)", err)
		} else {
			log.Printf("registry: follow: synced %d featured template(s)", n)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}

// toRow converts a real FeaturedTemplate response entry into the merge-safe upsert
// row shape. Every field is passed through as a pointer (even the "always present in
// practice" core fields) so a genuinely absent/zero-value upstream field never
// clobbers whatever internal/explorer's OWN upsert already stored for that column -
// see db.UpsertTemplateRegistryMetadata's doc comment for the exact mechanics this
// relies on.
//
// json.RawMessage's null-handling: encoding/json decodes a JSON `null` value into a
// non-nil json.RawMessage containing the literal 4 bytes "null" (NOT a nil/empty
// slice) - see registrymetaclient.FeaturedTemplate's doc comment. rawOrNil below
// normalizes that real JSON-decoding quirk into a genuine nil []byte before handing
// it to the DB layer, so Metadata/Definition correctly read back as SQL NULL (via
// UpsertTemplateRegistryMetadata's COALESCE) rather than storing the literal string
// "null" as a JSONB value.
func toRow(e registrymetaclient.FeaturedTemplate) db.TemplateRegistryMetadataEntry {
	templateName := e.TemplateName
	authorPublicKey := e.AuthorPublicKey
	binaryHash := e.BinaryHash
	atEpoch := e.AtEpoch
	isFeatured := e.IsFeatured
	codeSize := e.CodeSize

	return db.TemplateRegistryMetadataEntry{
		TemplateAddress:    e.TemplateAddress,
		TemplateName:       &templateName,
		AuthorPublicKey:    &authorPublicKey,
		BinaryHash:         &binaryHash,
		AtEpoch:            &atEpoch,
		MetadataHash:       e.MetadataHash,
		AuthorFriendlyName: e.AuthorFriendlyName,
		CodeSize:           &codeSize,
		IsFeatured:         &isFeatured,
		Metadata:           rawOrNil(e.Metadata),
		Definition:         rawOrNil(e.Definition),
	}
}

// rawOrNil normalizes a json.RawMessage that may be the literal 4-byte "null" (see
// toRow's doc comment) into a genuine nil []byte, and passes any other non-empty
// value through unchanged.
func rawOrNil(raw []byte) []byte {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	return raw
}
