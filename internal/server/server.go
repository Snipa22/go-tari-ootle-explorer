// Package server implements the v1 read-only HTTP UI for this repo's four data
// surfaces (indexer-backed blocks/validators, validator health, burn-claim tracking,
// and the community template registry mirror) - the last piece of AGENTS.md's v1
// build order.
//
// IMPORTANT: cmd/server (the only binary that constructs a Server) is READ-ONLY over
// Postgres. It never polls tari_indexer, tari_validator_node, an L1 base node, or the
// community metadata server itself - internal/explorer, internal/vnhealth,
// internal/burnclaim, and internal/registry (run as separate cmd/explorer,
// cmd/vnhealth, cmd/burnclaim, cmd/registry processes) are the only writers. This
// package's Server only ever calls the Store interface's List*/Get* methods.
//
// Progressive disclosure (per AGENTS.md's durable UX convention, and
// DISPATCH_BRIEF.md's explicit requirement for this dispatch): the home page (/)
// stays calm and liveness-focused - recent blocks, a live validator/committee status
// SUMMARY (not a full roster dump), recent template publishes, and any currently-
// stuck burn claims (a stuck claim is itself a liveness-relevant signal, unlike
// routine pending/claimed ones, so it belongs on the calm default view even though
// the FULL burn-claims history does not). Comprehensive/noisy data - every burn claim
// regardless of status, the full validator roster, the full template catalogue - is
// collected in full (never filtered away) but lives in separate dedicated views
// (/validators, /burn-claims, /templates), reachable via nav links.
//
// Same net/http + html/template + HTMX-via-CDN-script conventions as
// go-tari-explorer's own internal/server (see that package's server.go/
// templates/layout.html/templates/blocks_list.html, fetched read-only as this
// dispatch's structural reference - not copied verbatim, since this repo has four
// data sources feeding one UI instead of one).
package server

import (
	"context"
	"embed"
	"fmt"
	"html/template"
	"log"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Snipa22/go-tari-ootle-explorer/internal/db"
)

//go:embed templates/*.html
var templateFS embed.FS

// PageSize is the number of rows returned per page/HTMX "load more" request on the
// /blocks and /templates list views.
const PageSize = 25

// HomePageSampleSize is how many rows each of the home page's "recent" panels
// (blocks, template publishes) shows - deliberately small, per this package's own
// doc comment on progressive disclosure: the home page is a calm liveness snapshot,
// not a comprehensive list.
const HomePageSampleSize = 10

// funcs is the html/template FuncMap shared by every parsed template.
var funcs = template.FuncMap{
	"sub": func(a, b int) int { return a - b },
	// rfc3339nano renders a time.Time in RFC3339Nano - used by templates_list.html's
	// "load more" button to build a ?before= cursor value handleTemplatesPartial
	// can parse back with time.Parse(time.RFC3339Nano, ...) without any precision
	// loss (first_seen_at is a TIMESTAMPTZ, sub-second precision matters for a
	// correct, non-duplicating/non-skipping cursor).
	"rfc3339nano": func(t time.Time) string { return t.Format(time.RFC3339Nano) },
}

// Store is the subset of *db.DB's read methods Server needs. *db.DB satisfies this
// interface as-is (see internal/db/reads.go); tests supply a fake, matching the
// mockable-interface convention internal/explorer/internal/vnhealth already use for
// their own dependencies.
type Store interface {
	ListOotleBlocks(ctx context.Context, beforeHeight int64, limit int) ([]db.OotleBlock, error)
	ListValidators(ctx context.Context) ([]db.ValidatorRow, error)
	GetLiveValidatorEpochSummary(ctx context.Context) (db.LiveValidatorEpochSummary, error)
	ListBurnClaims(ctx context.Context, status string) ([]db.BurnClaim, error)
	ListTemplateRegistry(ctx context.Context, before time.Time, limit int) ([]db.TemplateRegistryRow, error)
}

// Server holds the dependencies needed to serve HTTP requests.
type Server struct {
	Store Store

	homeTmpl          *template.Template
	blocksListTmpl    *template.Template
	blocksRowsTmpl    *template.Template
	validatorsTmpl    *template.Template
	burnClaimsTmpl    *template.Template
	templatesListTmpl *template.Template
	templatesRowsTmpl *template.Template
}

// New parses the embedded templates and constructs a Server. Returns an error if the
// templates fail to parse (a build-time programming error, not a runtime/request
// error).
func New(store Store) (*Server, error) {
	homeTmpl, err := template.New("layout.html").Funcs(funcs).ParseFS(templateFS, "templates/layout.html", "templates/home.html")
	if err != nil {
		return nil, fmt.Errorf("server: parse home templates: %w", err)
	}
	blocksListTmpl, err := template.New("layout.html").Funcs(funcs).ParseFS(templateFS, "templates/layout.html", "templates/blocks_list.html")
	if err != nil {
		return nil, fmt.Errorf("server: parse blocks list templates: %w", err)
	}
	blocksRowsTmpl, err := template.New("rows-only").Funcs(funcs).ParseFS(templateFS, "templates/blocks_list.html")
	if err != nil {
		return nil, fmt.Errorf("server: parse blocks rows template: %w", err)
	}
	validatorsTmpl, err := template.New("layout.html").Funcs(funcs).ParseFS(templateFS, "templates/layout.html", "templates/validators_list.html")
	if err != nil {
		return nil, fmt.Errorf("server: parse validators template: %w", err)
	}
	burnClaimsTmpl, err := template.New("layout.html").Funcs(funcs).ParseFS(templateFS, "templates/layout.html", "templates/burn_claims_list.html")
	if err != nil {
		return nil, fmt.Errorf("server: parse burn claims template: %w", err)
	}
	templatesListTmpl, err := template.New("layout.html").Funcs(funcs).ParseFS(templateFS, "templates/layout.html", "templates/templates_list.html")
	if err != nil {
		return nil, fmt.Errorf("server: parse templates list templates: %w", err)
	}
	templatesRowsTmpl, err := template.New("rows-only").Funcs(funcs).ParseFS(templateFS, "templates/templates_list.html")
	if err != nil {
		return nil, fmt.Errorf("server: parse templates rows template: %w", err)
	}

	return &Server{
		Store:             store,
		homeTmpl:          homeTmpl,
		blocksListTmpl:    blocksListTmpl,
		blocksRowsTmpl:    blocksRowsTmpl,
		validatorsTmpl:    validatorsTmpl,
		burnClaimsTmpl:    burnClaimsTmpl,
		templatesListTmpl: templatesListTmpl,
		templatesRowsTmpl: templatesRowsTmpl,
	}, nil
}

// Handler builds the top-level http.Handler with every route registered - see this
// package's doc comment for the progressive-disclosure reasoning behind the route
// split, and DISPATCH_BRIEF.md for the exact route list this mirrors.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.handleHome)
	mux.HandleFunc("GET /blocks", s.handleBlocksList)
	mux.HandleFunc("GET /blocks/partial", s.handleBlocksPartial)
	mux.HandleFunc("GET /validators", s.handleValidators)
	mux.HandleFunc("GET /burn-claims", s.handleBurnClaims)
	mux.HandleFunc("GET /templates", s.handleTemplatesList)
	mux.HandleFunc("GET /templates/partial", s.handleTemplatesPartial)
	mux.HandleFunc("GET /health", s.handleHealth)

	// Dedicated JSON API namespace - see internal/server/api.go's own doc comment
	// for why this is a separate namespace rather than a ?json=1 alias on the
	// routes above (a user-clarified design decision, per
	// DISPATCH_BRIEF_JSON_API.md). None of the HTML routes registered above are
	// touched by this.
	mux.HandleFunc("GET /api/blocks", s.handleAPIBlocks)
	mux.HandleFunc("GET /api/validators", s.handleAPIValidators)
	mux.HandleFunc("GET /api/burn-claims", s.handleAPIBurnClaims)
	mux.HandleFunc("GET /api/templates", s.handleAPITemplates)
	mux.HandleFunc("GET /api/health", s.handleAPIHealth)
	mux.HandleFunc("GET /api/tip-info", s.handleAPITipInfo)

	return mux
}

// ---- view adapters (presentation-only, keep db package free of display concerns) ----

type blockView struct {
	db.OotleBlock
}

// TimeString formats Timestamp (unix seconds - see db.OotleBlock's own doc comment
// on why this is this repo's OWN observation time, not a real block-creation
// timestamp) as a human-readable UTC string.
func (b blockView) TimeString() string {
	return time.Unix(b.Timestamp, 0).UTC().Format("2006-01-02 15:04:05 UTC")
}

func toBlockViews(blocks []db.OotleBlock) []blockView {
	out := make([]blockView, len(blocks))
	for i, b := range blocks {
		out[i] = blockView{b}
	}
	return out
}

type validatorView struct {
	db.ValidatorRow
}

func (v validatorView) ConsensusStatusDisplay() string {
	if v.ConsensusStatus == nil || *v.ConsensusStatus == "" {
		return "unknown"
	}
	return *v.ConsensusStatus
}

func (v validatorView) LastCheckedAtDisplay() string {
	return v.LastCheckedAt.UTC().Format("2006-01-02 15:04:05 UTC")
}

func (v validatorView) ShardRangeDisplay() string {
	return fmt.Sprintf("[%d, %d]", v.ShardGroupStart, v.ShardGroupEndInclusive)
}

func toValidatorViews(validators []db.ValidatorRow) []validatorView {
	out := make([]validatorView, len(validators))
	for i, v := range validators {
		out[i] = validatorView{v}
	}
	return out
}

type burnClaimView struct {
	db.BurnClaim
}

func (b burnClaimView) ClaimPublicKeyDisplay() string {
	if b.ClaimPublicKey == nil || *b.ClaimPublicKey == "" {
		return "-"
	}
	return *b.ClaimPublicKey
}

func (b burnClaimView) ClaimTxIDDisplay() string {
	if b.ClaimTxID == nil || *b.ClaimTxID == "" {
		return "-"
	}
	return *b.ClaimTxID
}

func (b burnClaimView) ClaimedAtDisplay() string {
	if b.ClaimedAt == nil {
		return "-"
	}
	return b.ClaimedAt.UTC().Format("2006-01-02 15:04:05 UTC")
}

func toBurnClaimViews(claims []db.BurnClaim) []burnClaimView {
	out := make([]burnClaimView, len(claims))
	for i, c := range claims {
		out[i] = burnClaimView{c}
	}
	return out
}

type templateRegistryView struct {
	db.TemplateRegistryRow
}

// AuthorDisplay prefers the metadata server's human-friendly author name when known,
// falling back to the raw author_public_key (always present, from the indexer
// catalogue side) otherwise.
func (t templateRegistryView) AuthorDisplay() string {
	if t.AuthorFriendlyName != nil && *t.AuthorFriendlyName != "" {
		return *t.AuthorFriendlyName
	}
	return t.AuthorPublicKey
}

func (t templateRegistryView) CodeSizeDisplay() string {
	if t.CodeSize == nil {
		return "-"
	}
	return fmt.Sprintf("%d bytes", *t.CodeSize)
}

func (t templateRegistryView) FirstSeenAtDisplay() string {
	return t.FirstSeenAt.UTC().Format("2006-01-02 15:04:05 UTC")
}

func toTemplateRegistryViews(entries []db.TemplateRegistryRow) []templateRegistryView {
	out := make([]templateRegistryView, len(entries))
	for i, e := range entries {
		out[i] = templateRegistryView{e}
	}
	return out
}

// ---- handlers ----

// handleHome serves GET /: the calm, liveness-focused default view - see this
// package's doc comment. Every panel degrades to an inline error message on its own
// query failure rather than failing the whole page - one data source being
// temporarily unavailable/erroring isn't reason enough to 500 a page whose other
// three panels loaded fine.
func (s *Server) handleHome(w http.ResponseWriter, r *http.Request) {
	data := struct {
		Blocks               []blockView
		BlocksError          string
		ValidatorSummary     db.LiveValidatorEpochSummary
		ValidatorError       string
		Templates            []templateRegistryView
		TemplatesError       string
		StuckBurnClaims      []burnClaimView
		StuckBurnClaimsError string
	}{}

	blocks, err := s.Store.ListOotleBlocks(r.Context(), math.MaxInt64, HomePageSampleSize)
	if err != nil {
		log.Printf("server: home: list ootle blocks: %v", err)
		data.BlocksError = "unable to load recent blocks"
	} else {
		data.Blocks = toBlockViews(blocks)
	}

	summary, err := s.Store.GetLiveValidatorEpochSummary(r.Context())
	if err != nil {
		log.Printf("server: home: live validator epoch summary: %v", err)
		data.ValidatorError = "unable to load validator summary"
	} else {
		data.ValidatorSummary = summary
	}

	templates, err := s.Store.ListTemplateRegistry(r.Context(), time.Now(), HomePageSampleSize)
	if err != nil {
		log.Printf("server: home: list template registry: %v", err)
		data.TemplatesError = "unable to load recent templates"
	} else {
		data.Templates = toTemplateRegistryViews(templates)
	}

	stuck, err := s.Store.ListBurnClaims(r.Context(), "stuck")
	if err != nil {
		log.Printf("server: home: list stuck burn claims: %v", err)
		data.StuckBurnClaimsError = "unable to load stuck burn claims"
	} else {
		data.StuckBurnClaims = toBurnClaimViews(stuck)
	}

	if err := s.homeTmpl.Execute(w, data); err != nil {
		log.Printf("server: render home: %v", err)
	}
}

// handleBlocksList serves GET /blocks: the full, paginated ootle_blocks list (first
// page), per DISPATCH_BRIEF.md's "GET /blocks — paginated ootle_blocks list (HTMX
// 'load more')" requirement.
func (s *Server) handleBlocksList(w http.ResponseWriter, r *http.Request) {
	blocks, err := s.Store.ListOotleBlocks(r.Context(), math.MaxInt64, PageSize)
	if err != nil {
		http.Error(w, "failed to load blocks", http.StatusInternalServerError)
		log.Printf("server: list blocks: %v", err)
		return
	}
	data := struct{ Blocks []blockView }{Blocks: toBlockViews(blocks)}
	if err := s.blocksListTmpl.Execute(w, data); err != nil {
		log.Printf("server: render blocks list: %v", err)
	}
}

// handleBlocksPartial serves the HTMX "load more" request for /blocks: the next page
// of rows strictly below ?before=<height>, rendered without the surrounding page
// layout.
func (s *Server) handleBlocksPartial(w http.ResponseWriter, r *http.Request) {
	before, err := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
	if err != nil {
		http.Error(w, "invalid before parameter", http.StatusBadRequest)
		return
	}
	blocks, err := s.Store.ListOotleBlocks(r.Context(), before, PageSize)
	if err != nil {
		http.Error(w, "failed to load blocks", http.StatusInternalServerError)
		log.Printf("server: list blocks partial: %v", err)
		return
	}
	if err := s.blocksRowsTmpl.ExecuteTemplate(w, "rows", toBlockViews(blocks)); err != nil {
		log.Printf("server: render blocks partial: %v", err)
	}
}

// handleValidators serves GET /validators: the full validator roster, per
// DISPATCH_BRIEF.md's "full validator list/table (public_key, last_seen_epoch, shard
// range, consensus_status, last_checked_at)" requirement. Unpaginated - see
// db.ListValidators' own doc comment on why a real-world roster is small enough for
// v1's purposes.
func (s *Server) handleValidators(w http.ResponseWriter, r *http.Request) {
	validators, err := s.Store.ListValidators(r.Context())
	if err != nil {
		http.Error(w, "failed to load validators", http.StatusInternalServerError)
		log.Printf("server: list validators: %v", err)
		return
	}
	data := struct{ Validators []validatorView }{Validators: toValidatorViews(validators)}
	if err := s.validatorsTmpl.Execute(w, data); err != nil {
		log.Printf("server: render validators: %v", err)
	}
}

// handleBurnClaims serves GET /burn-claims: the full burn_claims list, filterable by
// ?status=pending|claimed|stuck, defaulting to showing all - per DISPATCH_BRIEF.md.
// An unrecognized status value is passed straight through to the DB query (which
// will simply match zero rows, given burn_claims' CHECK constraint already limits
// real rows to the three known values) rather than rejected with a 400 - a stray/
// misspelled query param degrading to an empty (but still 200) list is friendlier
// than a hard error for a read-only browsing view.
func (s *Server) handleBurnClaims(w http.ResponseWriter, r *http.Request) {
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	claims, err := s.Store.ListBurnClaims(r.Context(), status)
	if err != nil {
		http.Error(w, "failed to load burn claims", http.StatusInternalServerError)
		log.Printf("server: list burn claims (status=%q): %v", status, err)
		return
	}
	data := struct {
		Claims       []burnClaimView
		StatusFilter string
	}{Claims: toBurnClaimViews(claims), StatusFilter: status}
	if err := s.burnClaimsTmpl.Execute(w, data); err != nil {
		log.Printf("server: render burn claims: %v", err)
	}
}

// handleTemplatesList serves GET /templates: the full, paginated template_registry
// catalogue (first page), per DISPATCH_BRIEF.md's "full template_registry list/table
// (template_address, template_name, author, at_epoch, is_featured, code_size)"
// requirement.
func (s *Server) handleTemplatesList(w http.ResponseWriter, r *http.Request) {
	entries, err := s.Store.ListTemplateRegistry(r.Context(), time.Now(), PageSize)
	if err != nil {
		http.Error(w, "failed to load templates", http.StatusInternalServerError)
		log.Printf("server: list template registry: %v", err)
		return
	}
	data := struct{ Templates []templateRegistryView }{Templates: toTemplateRegistryViews(entries)}
	if err := s.templatesListTmpl.Execute(w, data); err != nil {
		log.Printf("server: render templates list: %v", err)
	}
}

// handleTemplatesPartial serves the HTMX "load more" request for /templates: the
// next page of rows strictly before ?before=<RFC3339 first_seen_at>, rendered
// without the surrounding page layout.
func (s *Server) handleTemplatesPartial(w http.ResponseWriter, r *http.Request) {
	before, err := time.Parse(time.RFC3339Nano, r.URL.Query().Get("before"))
	if err != nil {
		http.Error(w, "invalid before parameter", http.StatusBadRequest)
		return
	}
	entries, err := s.Store.ListTemplateRegistry(r.Context(), before, PageSize)
	if err != nil {
		http.Error(w, "failed to load templates", http.StatusInternalServerError)
		log.Printf("server: list template registry partial: %v", err)
		return
	}
	if err := s.templatesRowsTmpl.ExecuteTemplate(w, "rows", toTemplateRegistryViews(entries)); err != nil {
		log.Printf("server: render templates partial: %v", err)
	}
}

// handleHealth serves GET /health: a plain liveness endpoint. Per DISPATCH_BRIEF.md
// this is deliberately NOT a poller-health check - cmd/server never runs any of the
// pollers itself (see this package's doc comment), so "healthy" here means only
// "this HTTP process is up and its Postgres connection is queryable", surfaced via a
// single cheap query (GetLiveValidatorEpochSummary) rather than a real per-subsystem
// last-poll timestamp (which would need each cmd/* poller to itself record a
// heartbeat row somewhere - out of scope for this dispatch, see the dispatch
// report). Always returns 200 - a degraded Postgres connection is reported in the
// body, not via a non-200 status, since this is a liveness probe for the HTTP
// process itself, not a readiness gate for the data behind it.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)

	if _, err := s.Store.GetLiveValidatorEpochSummary(r.Context()); err != nil {
		fmt.Fprintf(w, "ok (server process) - database: degraded: %v\n", err)
		return
	}
	fmt.Fprintf(w, "ok (server process) - database: reachable\n")
}
