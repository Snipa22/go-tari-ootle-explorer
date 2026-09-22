// This file adds a dedicated /api/* JSON namespace alongside internal/server's
// existing HTML+HTMX views (server.go), per DISPATCH_BRIEF_JSON_API.md: a Tari user
// asked for a JSON API "similar to https://textexplore.tari.com/?json", and after
// clarifying directly with the user, the answer is a SEPARATE `/api/*` route
// namespace, NOT a `?json=1` query-param alias bolted onto the existing HTML routes
// (server.go's handleHome/handleBlocksList/etc. are untouched by this file).
//
// Each /api/* route below mirrors one existing HTML route's Store query and
// pagination/filter semantics (see the doc comment on each handler for exactly
// which one), but returns the raw db.* row types (JSON-tagged directly on the
// structs themselves - see db.OotleBlock/db.ValidatorRow/db.BurnClaim's own doc
// comments in internal/db) instead of going through server.go's HTML-only view
// adapters (blockView, validatorView, etc.) - those exist purely for template
// display methods like TimeString() and have no bearing on this JSON surface.
package server

import (
	"encoding/json"
	"log"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Snipa22/go-tari-ootle-explorer/internal/db"
)

// MaxAPILimit caps a client-supplied ?limit= on /api/blocks and /api/templates -
// per DISPATCH_BRIEF_JSON_API.md's "clamp to a sane max ... to avoid an unbounded
// query from a client-supplied limit" requirement.
const MaxAPILimit = 100

// writeJSON marshals v as the response body with the given HTTP status and the
// application/json; charset=utf-8 content type every /api/* route uses (per
// DISPATCH_BRIEF_JSON_API.md's cross-cutting requirement).
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("server: api: encode json response: %v", err)
	}
}

// writeJSONError writes a `{"error": "<message>"}` JSON body at the given status -
// every /api/* route's error path, replacing the HTML routes' http.Error (plain-text
// body) equivalent, per DISPATCH_BRIEF_JSON_API.md's "valid JSON even on error"
// requirement. Uses the same HTTP status codes the HTML handlers already use for the
// equivalent failure (500 for a Store/DB error, 400 for a malformed query param).
func writeJSONError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

// parseAPILimit parses a ?limit= query param value, defaulting to defaultLimit when
// raw is empty, and clamping any value above MaxAPILimit down to it. Returns an
// error for a non-integer or non-positive value - both are "malformed", per
// DISPATCH_BRIEF_JSON_API.md's 400-on-malformed-param requirement, rather than
// silently substituted with the default.
func parseAPILimit(raw string, defaultLimit int) (int, error) {
	if raw == "" {
		return defaultLimit, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, err
	}
	if n <= 0 {
		return 0, strconv.ErrRange
	}
	if n > MaxAPILimit {
		n = MaxAPILimit
	}
	return n, nil
}

// apiTemplateRegistryRow is the /api/templates JSON DTO for db.TemplateRegistryRow.
//
// Metadata/Definition decision (explicitly called out in DISPATCH_BRIEF_JSON_API.md
// for Alex to override in review if he disagrees): both columns are inlined as raw
// nested JSON (json.RawMessage) rather than left as db.TemplateRegistryRow's native
// []byte (which encoding/json would otherwise base64-encode into a string) or
// omitted from the response entirely. Rationale: both are legitimate, not
// oversized structured JSONB blobs sourced from the community metadata server (see
// migrations/0004_template_registry_metadata_server_fields.up.sql) - a JSON API
// consumer gets real, directly-usable nested objects/arrays this way instead of an
// opaque base64 string it would have to decode-then-parse itself, or losing the data
// entirely. Every row always carries both keys (never omitted via omitempty) so
// consumers can rely on a stable shape; an unset/never-populated blob (common for
// indexer-catalogue-only entries the community metadata server hasn't mirrored yet -
// see db.TemplateRegistryEntry's doc comment) renders as JSON null rather than a
// missing key.
type apiTemplateRegistryRow struct {
	TemplateAddress    string          `json:"template_address"`
	TemplateName       string          `json:"template_name"`
	AuthorPublicKey    string          `json:"author_public_key"`
	BinaryHash         string          `json:"binary_hash"`
	AtEpoch            uint64          `json:"at_epoch"`
	MetadataHash       *string         `json:"metadata_hash"`
	AuthorFriendlyName *string         `json:"author_friendly_name"`
	CodeSize           *int64          `json:"code_size"`
	IsFeatured         bool            `json:"is_featured"`
	Metadata           json.RawMessage `json:"metadata"`
	Definition         json.RawMessage `json:"definition"`
	FirstSeenAt        time.Time       `json:"first_seen_at"`
}

// toAPITemplateRegistryRow converts a db.TemplateRegistryRow into its JSON DTO - see
// apiTemplateRegistryRow's own doc comment for the Metadata/Definition decision this
// implements. An empty (nil or zero-length) blob is left as a nil json.RawMessage,
// which json.Marshal renders as `null`, rather than being coerced into `{}`/`[]` -
// this repo doesn't know which shape (object vs array) the metadata server would
// have used, so null-for-genuinely-absent is the only honest representation.
func toAPITemplateRegistryRow(t db.TemplateRegistryRow) apiTemplateRegistryRow {
	out := apiTemplateRegistryRow{
		TemplateAddress:    t.TemplateAddress,
		TemplateName:       t.TemplateName,
		AuthorPublicKey:    t.AuthorPublicKey,
		BinaryHash:         t.BinaryHash,
		AtEpoch:            t.AtEpoch,
		MetadataHash:       t.MetadataHash,
		AuthorFriendlyName: t.AuthorFriendlyName,
		CodeSize:           t.CodeSize,
		IsFeatured:         t.IsFeatured,
		FirstSeenAt:        t.FirstSeenAt,
	}
	if len(t.Metadata) > 0 {
		out.Metadata = json.RawMessage(t.Metadata)
	}
	if len(t.Definition) > 0 {
		out.Definition = json.RawMessage(t.Definition)
	}
	return out
}

// ---- /api/* handlers ----

// handleAPIBlocks serves GET /api/blocks: a JSON array of db.OotleBlock rows, same
// before-cursor + limit pagination shape as handleBlocksList/handleBlocksPartial
// (GET /blocks and /blocks/partial) collapsed into a single endpoint - a JSON caller
// doesn't need the HTML routes' "full page vs HTMX rows-only partial" split.
// ?before=<height> defaults to math.MaxInt64 (first page) when omitted; ?limit=<n>
// defaults to PageSize (25), clamped to MaxAPILimit (100).
func (s *Server) handleAPIBlocks(w http.ResponseWriter, r *http.Request) {
	before := int64(math.MaxInt64)
	if raw := r.URL.Query().Get("before"); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid before parameter")
			return
		}
		before = parsed
	}
	limit, err := parseAPILimit(r.URL.Query().Get("limit"), PageSize)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid limit parameter")
		return
	}

	blocks, err := s.Store.ListOotleBlocks(r.Context(), before, limit)
	if err != nil {
		log.Printf("server: api: list blocks: %v", err)
		writeJSONError(w, http.StatusInternalServerError, "failed to load blocks")
		return
	}
	if blocks == nil {
		blocks = []db.OotleBlock{}
	}
	writeJSON(w, http.StatusOK, blocks)
}

// handleAPIValidators serves GET /api/validators: a JSON array of every
// db.ValidatorRow - the full roster, unpaginated, matching handleValidators'
// (GET /validators) own no-pagination behavior.
func (s *Server) handleAPIValidators(w http.ResponseWriter, r *http.Request) {
	validators, err := s.Store.ListValidators(r.Context())
	if err != nil {
		log.Printf("server: api: list validators: %v", err)
		writeJSONError(w, http.StatusInternalServerError, "failed to load validators")
		return
	}
	if validators == nil {
		validators = []db.ValidatorRow{}
	}
	writeJSON(w, http.StatusOK, validators)
}

// handleAPIBurnClaims serves GET /api/burn-claims: a JSON array of db.BurnClaim,
// honoring the same ?status=pending|claimed|stuck filter semantics as
// handleBurnClaims (GET /burn-claims) - empty/omitted returns every status.
func (s *Server) handleAPIBurnClaims(w http.ResponseWriter, r *http.Request) {
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	claims, err := s.Store.ListBurnClaims(r.Context(), status)
	if err != nil {
		log.Printf("server: api: list burn claims (status=%q): %v", status, err)
		writeJSONError(w, http.StatusInternalServerError, "failed to load burn claims")
		return
	}
	if claims == nil {
		claims = []db.BurnClaim{}
	}
	writeJSON(w, http.StatusOK, claims)
}

// handleAPITemplates serves GET /api/templates: a JSON array of
// apiTemplateRegistryRow (see that type's doc comment for the Metadata/Definition
// design decision), same before-cursor + limit pagination shape as
// handleTemplatesList/handleTemplatesPartial (GET /templates and
// /templates/partial) collapsed into one endpoint, same as handleAPIBlocks does for
// blocks. ?before=<RFC3339Nano timestamp> defaults to time.Now() (first page) when
// omitted, matching ListTemplateRegistry's own cursor semantics; ?limit=<n> defaults
// to PageSize (25), clamped to MaxAPILimit (100).
func (s *Server) handleAPITemplates(w http.ResponseWriter, r *http.Request) {
	before := time.Now()
	if raw := r.URL.Query().Get("before"); raw != "" {
		parsed, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid before parameter")
			return
		}
		before = parsed
	}
	limit, err := parseAPILimit(r.URL.Query().Get("limit"), PageSize)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid limit parameter")
		return
	}

	entries, err := s.Store.ListTemplateRegistry(r.Context(), before, limit)
	if err != nil {
		log.Printf("server: api: list template registry: %v", err)
		writeJSONError(w, http.StatusInternalServerError, "failed to load templates")
		return
	}
	out := make([]apiTemplateRegistryRow, len(entries))
	for i, e := range entries {
		out[i] = toAPITemplateRegistryRow(e)
	}
	writeJSON(w, http.StatusOK, out)
}

// apiValidatorEpochSummary is the /api/tip-info JSON DTO for
// db.LiveValidatorEpochSummary - that struct has no json tags of its own (it's an
// internal/db-only computed value, not a table row), so this gives it the stable
// snake_case shape DISPATCH_BRIEF_TIPINFO_ADDENDUM.md specifies
// (`{"epoch":...,"count":...}`) without adding JSON concerns to internal/db.
type apiValidatorEpochSummary struct {
	Epoch uint64 `json:"epoch"`
	Count int    `json:"count"`
}

// apiTipInfo is the /api/tip-info response body. Each field's sibling
// "<field>_error" is only present (via omitempty) when that specific sub-query
// failed - see handleAPITipInfo's own doc comment for the independent-degradation
// reasoning.
type apiTipInfo struct {
	LatestBlock                *db.OotleBlock           `json:"latest_block"`
	LatestBlockError           string                   `json:"latest_block_error,omitempty"`
	ValidatorEpochSummary      apiValidatorEpochSummary `json:"validator_epoch_summary"`
	ValidatorEpochSummaryError string                   `json:"validator_epoch_summary_error,omitempty"`
	StuckBurnClaimsCount       int                      `json:"stuck_burn_claims_count"`
	StuckBurnClaimsCountError  string                   `json:"stuck_burn_claims_count_error,omitempty"`
}

// handleAPITipInfo serves GET /api/tip-info: a compact "chain tip / liveness
// status" snapshot, per DISPATCH_BRIEF_TIPINFO_ADDENDUM.md's analogy to
// textexplore.tari.com's `?json` response's `tipInfo` subsection (an L1 base-node
// chain-tip object: best block height/hash, accumulated difficulty, sync state).
// This repo is L2/Ootle and has no base-node equivalent in its data model, so this
// endpoint instead assembles the closest real analog this repo's Store actually
// has - the same three data points handleHome's default view already surfaces as
// its own "liveness" panels: the most recently observed ootle_blocks row, the live
// validator epoch summary, and the current stuck-burn-claim count.
//
// Each of the three sub-queries degrades independently on its own error, same
// "one data source erroring isn't reason to fail the whole response" principle
// handleHome documents for its own panels: on a sub-query error, the corresponding
// field falls back to its zero value (nil/zero-value/0) and a sibling "<field>_error"
// string is set instead. A top-level 500 is only returned if ALL three sub-queries
// fail - a genuinely fully-degraded response, rather than a single flaky query
// taking down the whole snapshot.
func (s *Server) handleAPITipInfo(w http.ResponseWriter, r *http.Request) {
	var out apiTipInfo
	failures := 0

	blocks, err := s.Store.ListOotleBlocks(r.Context(), math.MaxInt64, 1)
	if err != nil {
		log.Printf("server: api: tip-info: list ootle blocks: %v", err)
		out.LatestBlockError = "unable to load recent blocks"
		failures++
	} else if len(blocks) > 0 {
		out.LatestBlock = &blocks[0]
	}

	summary, err := s.Store.GetLiveValidatorEpochSummary(r.Context())
	if err != nil {
		log.Printf("server: api: tip-info: live validator epoch summary: %v", err)
		out.ValidatorEpochSummaryError = "unable to load validator summary"
		failures++
	} else {
		out.ValidatorEpochSummary = apiValidatorEpochSummary{Epoch: summary.Epoch, Count: summary.Count}
	}

	stuck, err := s.Store.ListBurnClaims(r.Context(), "stuck")
	if err != nil {
		log.Printf("server: api: tip-info: list stuck burn claims: %v", err)
		out.StuckBurnClaimsCountError = "unable to load stuck burn claims"
		failures++
	} else {
		out.StuckBurnClaimsCount = len(stuck)
	}

	if failures == 3 {
		writeJSONError(w, http.StatusInternalServerError, "failed to load tip info")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleAPIHealth serves GET /api/health: same liveness semantics as handleHealth
// (GET /health, see that handler's own doc comment for the full reasoning -
// deliberately NOT a poller-health check, just "is this HTTP process up and is its
// Postgres connection queryable") but as a JSON body instead of plain text. Always
// returns 200 - a degraded database is reported in the body, not via a non-200
// status, unchanged from handleHealth's own behavior.
func (s *Server) handleAPIHealth(w http.ResponseWriter, r *http.Request) {
	if _, err := s.Store.GetLiveValidatorEpochSummary(r.Context()); err != nil {
		writeJSON(w, http.StatusOK, map[string]string{
			"status":   "ok",
			"database": "degraded",
			"error":    err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"status":   "ok",
		"database": "reachable",
	})
}
