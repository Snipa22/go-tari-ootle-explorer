package registrymetaclient

import "encoding/json"

// FeaturedTemplate is the real response shape of
// GET /community-templates/api/templates/featured, live-verified this session
// against https://ootle-templates-esme.tari.com (esmeralda) - see
// testdata/featured.json for the raw captured response this package's tests decode
// against, and internal/db/migrations/0004_template_registry_metadata_server_fields.up.sql
// for the same citation applied to this repo's own schema.
//
// Field notes:
//   - AuthorFriendlyName/MetadataHash/Definition: real, nullable fields on the
//     struct - observed null on every live entry checked this session, decoded as Go
//     nil pointers rather than empty strings when absent.
//   - Metadata: a real, nullable field whose OWN internal shape is confirmed live
//     (see the TariStableCoin example in testdata/featured.json: a
//     name/version/description/tags/category/... object) but whose server-side Go/
//     Rust struct definition could not be located in tari-project/tari-cli or a
//     linked server repo - per AGENTS.md's "don't guess a struct shape" rule, this is
//     decoded as json.RawMessage (an opaque blob) rather than a guessed Go struct,
//     matching the same opaque-JSONB treatment migration 0004 gives it in Postgres.
//     Confirmed live: this is genuinely where tags/category live for a template that
//     has published metadata (DISPATCH_BRIEF.md's own open question) - NOT a
//     top-level field on FeaturedTemplate itself.
//   - Definition: also decoded as json.RawMessage for the same reason. Always null on
//     THIS route's entries in practice (confirmed: none of the three live entries
//     checked had it set) but genuinely populated when fetched via the real
//     per-template-address route this session also confirmed exists -
//     GET /community-templates/api/templates/{address} (see this package's doc
//     comment for why that route isn't otherwise used by this v1 client).
type FeaturedTemplate struct {
	TemplateAddress    string          `json:"template_address"`
	TemplateName       string          `json:"template_name"`
	AuthorPublicKey    string          `json:"author_public_key"`
	AuthorFriendlyName *string         `json:"author_friendly_name"`
	BinaryHash         string          `json:"binary_hash"`
	AtEpoch            uint64          `json:"at_epoch"`
	MetadataHash       *string         `json:"metadata_hash"`
	Definition         json.RawMessage `json:"definition"`
	CodeSize           int64           `json:"code_size"`
	IsFeatured         bool            `json:"is_featured"`
	Metadata           json.RawMessage `json:"metadata"`
}
