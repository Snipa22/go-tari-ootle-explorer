package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Snipa22/go-tari-ootle-explorer/internal/db"
)

// fakeStore is an in-memory Store double - no real Postgres involved, per
// DISPATCH_BRIEF.md's requirement that internal/server's HTTP handler tests use a
// mockable Store interface (mirroring internal/explorer/internal/vnhealth's own
// fake-dependency test convention), reserving real-Postgres coverage for
// internal/db's own read-method tests (see internal/db/reads_test.go).
type fakeStore struct {
	blocks    []db.OotleBlock
	blocksErr error

	validators    []db.ValidatorRow
	validatorsErr error

	summary    db.LiveValidatorEpochSummary
	summaryErr error

	burnClaims    []db.BurnClaim
	burnClaimsErr error
	// lastStatusArg records the status string ListBurnClaims was last called
	// with, so tests can assert the ?status= query param is threaded through
	// correctly.
	lastStatusArg string

	templates    []db.TemplateRegistryRow
	templatesErr error
	// lastBeforeArg records the beforeHeight/before cursor ListOotleBlocks/
	// ListTemplateRegistry was last called with.
	lastBeforeHeightArg int64
	lastBeforeTimeArg   time.Time
}

func (f *fakeStore) ListOotleBlocks(_ context.Context, beforeHeight int64, _ int) ([]db.OotleBlock, error) {
	f.lastBeforeHeightArg = beforeHeight
	if f.blocksErr != nil {
		return nil, f.blocksErr
	}
	return f.blocks, nil
}

func (f *fakeStore) ListValidators(_ context.Context) ([]db.ValidatorRow, error) {
	if f.validatorsErr != nil {
		return nil, f.validatorsErr
	}
	return f.validators, nil
}

func (f *fakeStore) GetLiveValidatorEpochSummary(_ context.Context) (db.LiveValidatorEpochSummary, error) {
	if f.summaryErr != nil {
		return db.LiveValidatorEpochSummary{}, f.summaryErr
	}
	return f.summary, nil
}

func (f *fakeStore) ListBurnClaims(_ context.Context, status string) ([]db.BurnClaim, error) {
	f.lastStatusArg = status
	if f.burnClaimsErr != nil {
		return nil, f.burnClaimsErr
	}
	return f.burnClaims, nil
}

func (f *fakeStore) ListTemplateRegistry(_ context.Context, before time.Time, _ int) ([]db.TemplateRegistryRow, error) {
	f.lastBeforeTimeArg = before
	if f.templatesErr != nil {
		return nil, f.templatesErr
	}
	return f.templates, nil
}

func newTestServer(t *testing.T, store *fakeStore) *Server {
	t.Helper()
	srv, err := New(store)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return srv
}

func doRequest(t *testing.T, h http.Handler, method, target string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestHandleHome_HappyPath covers GET / rendering every panel successfully -
// including the "stuck burn claims surfaced on the calm default view" behavior
// DISPATCH_BRIEF.md explicitly calls out as the one comprehensive-data exception to
// progressive disclosure.
func TestHandleHome_HappyPath(t *testing.T) {
	store := &fakeStore{
		blocks:     []db.OotleBlock{{BlockID: "block-1", Height: 42, Epoch: 5, Timestamp: time.Now().Unix()}},
		summary:    db.LiveValidatorEpochSummary{Epoch: 5, Count: 3},
		templates:  []db.TemplateRegistryRow{{TemplateAddress: "addr-1", TemplateName: "Tmpl", AuthorPublicKey: "author-1", AtEpoch: 5}},
		burnClaims: []db.BurnClaim{{L1BurnTxHash: "kernel-1", Commitment: "commit-1", BurnHeight: 100, Status: "stuck"}},
	}
	srv := newTestServer(t, store)

	rec := doRequest(t, srv.Handler(), "GET", "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "block-1") {
		t.Errorf("body missing recent block, got: %s", body)
	}
	if !strings.Contains(body, "5") { // epoch
		t.Errorf("body missing validator summary epoch, got: %s", body)
	}
	if !strings.Contains(body, "Tmpl") {
		t.Errorf("body missing recent template, got: %s", body)
	}
	if !strings.Contains(body, "kernel-1") || !strings.Contains(body, "stuck burn claim") {
		t.Errorf("body missing stuck burn claim banner, got: %s", body)
	}
}

// TestHandleHome_DegradesPerPanelOnError covers home page's per-panel error
// isolation: one Store method failing must not 500 the whole page, and must not
// prevent the other, successfully-loaded panels from rendering.
func TestHandleHome_DegradesPerPanelOnError(t *testing.T) {
	store := &fakeStore{
		blocksErr:  errors.New("boom"),
		summary:    db.LiveValidatorEpochSummary{Epoch: 9, Count: 1},
		templates:  []db.TemplateRegistryRow{{TemplateAddress: "addr-1", TemplateName: "Tmpl", AtEpoch: 9}},
		burnClaims: nil, // no stuck claims - the calm, no-banner default state
	}
	srv := newTestServer(t, store)

	rec := doRequest(t, srv.Handler(), "GET", "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (a single panel's error must not 500 the page); body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "unable to load recent blocks") {
		t.Errorf("body missing blocks-panel error message, got: %s", body)
	}
	if !strings.Contains(body, "Tmpl") {
		t.Errorf("body missing recent template (should still render despite blocks error), got: %s", body)
	}
	if strings.Contains(strings.ToLower(body), "stuck burn claim(s)") {
		t.Errorf("body should not show a stuck-claims banner when there are none, got: %s", body)
	}
}

// TestHandleBlocksList covers GET /blocks: first page uses math.MaxInt64 as the
// before-cursor sentinel and renders every returned row.
func TestHandleBlocksList(t *testing.T) {
	store := &fakeStore{blocks: []db.OotleBlock{
		{BlockID: "block-2", Height: 20, Epoch: 1, Timestamp: time.Now().Unix()},
		{BlockID: "block-1", Height: 10, Epoch: 1, Timestamp: time.Now().Unix()},
	}}
	srv := newTestServer(t, store)

	rec := doRequest(t, srv.Handler(), "GET", "/blocks")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "block-2") || !strings.Contains(body, "block-1") {
		t.Errorf("body missing expected block rows, got: %s", body)
	}
	// The "load more" button should target the LAST row's height (10), matching
	// blocks_list.html's {{$last := index . (sub (len .) 1)}} cursor logic.
	if !strings.Contains(body, "/blocks/partial?before=10") {
		t.Errorf("body missing correct load-more cursor (before=10), got: %s", body)
	}
}

// TestHandleBlocksList_Error covers GET /blocks' 500 path - unlike the home page,
// /blocks IS the whole page's content, so a Store failure here is a real 500, not a
// degraded panel.
func TestHandleBlocksList_Error(t *testing.T) {
	store := &fakeStore{blocksErr: errors.New("boom")}
	srv := newTestServer(t, store)

	rec := doRequest(t, srv.Handler(), "GET", "/blocks")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rec.Code, rec.Body.String())
	}
}

// TestHandleBlocksPartial covers the HTMX "load more" endpoint: the before cursor is
// parsed from ?before= and threaded through to the Store call, and rows-only (no
// layout) are rendered.
func TestHandleBlocksPartial(t *testing.T) {
	store := &fakeStore{blocks: []db.OotleBlock{{BlockID: "block-1", Height: 5, Epoch: 1, Timestamp: time.Now().Unix()}}}
	srv := newTestServer(t, store)

	rec := doRequest(t, srv.Handler(), "GET", "/blocks/partial?before=10")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if store.lastBeforeHeightArg != 10 {
		t.Errorf("lastBeforeHeightArg = %d, want 10", store.lastBeforeHeightArg)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "block-1") {
		t.Errorf("body missing expected row, got: %s", body)
	}
	if strings.Contains(body, "<html") || strings.Contains(body, "<nav") {
		t.Errorf("partial response should not include the surrounding layout, got: %s", body)
	}
}

// TestHandleBlocksPartial_InvalidBefore covers the 400 path for a malformed/missing
// ?before= param.
func TestHandleBlocksPartial_InvalidBefore(t *testing.T) {
	srv := newTestServer(t, &fakeStore{})
	rec := doRequest(t, srv.Handler(), "GET", "/blocks/partial?before=not-a-number")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

// TestHandleValidators covers GET /validators: the full roster renders, including
// consensus_status/last_checked_at fields db.Validator's write-side struct doesn't
// even carry.
func TestHandleValidators(t *testing.T) {
	status := "Running"
	store := &fakeStore{validators: []db.ValidatorRow{
		{PublicKey: "pubkey-a", LastSeenEpoch: 10, ShardGroupStart: 1, ShardGroupEndInclusive: 128, ConsensusStatus: &status, LastCheckedAt: time.Now()},
	}}
	srv := newTestServer(t, store)

	rec := doRequest(t, srv.Handler(), "GET", "/validators")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "pubkey-a") || !strings.Contains(body, "Running") {
		t.Errorf("body missing expected validator row, got: %s", body)
	}
}

// TestHandleValidators_Error covers GET /validators' 500 path.
func TestHandleValidators_Error(t *testing.T) {
	srv := newTestServer(t, &fakeStore{validatorsErr: errors.New("boom")})
	rec := doRequest(t, srv.Handler(), "GET", "/validators")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rec.Code, rec.Body.String())
	}
}

// TestHandleBurnClaims_StatusFilter covers GET /burn-claims?status=... threading the
// query param through to the Store call, and defaulting to "" (all) when absent.
func TestHandleBurnClaims_StatusFilter(t *testing.T) {
	store := &fakeStore{burnClaims: []db.BurnClaim{{L1BurnTxHash: "kernel-1", Commitment: "commit-1", BurnHeight: 100, Status: "stuck"}}}
	srv := newTestServer(t, store)

	rec := doRequest(t, srv.Handler(), "GET", "/burn-claims?status=stuck")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if store.lastStatusArg != "stuck" {
		t.Errorf("lastStatusArg = %q, want \"stuck\"", store.lastStatusArg)
	}
	if !strings.Contains(rec.Body.String(), "kernel-1") {
		t.Errorf("body missing expected claim row, got: %s", rec.Body.String())
	}

	rec2 := doRequest(t, srv.Handler(), "GET", "/burn-claims")
	if rec2.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec2.Code)
	}
	if store.lastStatusArg != "" {
		t.Errorf("lastStatusArg = %q, want \"\" (default: show all)", store.lastStatusArg)
	}
}

// TestHandleBurnClaims_Error covers GET /burn-claims' 500 path.
func TestHandleBurnClaims_Error(t *testing.T) {
	srv := newTestServer(t, &fakeStore{burnClaimsErr: errors.New("boom")})
	rec := doRequest(t, srv.Handler(), "GET", "/burn-claims")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rec.Code, rec.Body.String())
	}
}

// TestHandleTemplatesList covers GET /templates rendering rows and the load-more
// cursor (an RFC3339Nano-encoded first_seen_at, per templates_list.html's
// {{rfc3339nano $last.FirstSeenAt}}).
func TestHandleTemplatesList(t *testing.T) {
	seenAt := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	store := &fakeStore{templates: []db.TemplateRegistryRow{
		{TemplateAddress: "addr-1", TemplateName: "Tmpl", AuthorPublicKey: "author-1", AtEpoch: 5, FirstSeenAt: seenAt},
	}}
	srv := newTestServer(t, store)

	rec := doRequest(t, srv.Handler(), "GET", "/templates")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Tmpl") || !strings.Contains(body, "addr-1") {
		t.Errorf("body missing expected template row, got: %s", body)
	}
	wantCursor := "/templates/partial?before=" + seenAt.Format(time.RFC3339Nano)
	if !strings.Contains(body, wantCursor) {
		t.Errorf("body missing correct load-more cursor %q, got: %s", wantCursor, body)
	}
}

// TestHandleTemplatesList_Error covers GET /templates' 500 path.
func TestHandleTemplatesList_Error(t *testing.T) {
	srv := newTestServer(t, &fakeStore{templatesErr: errors.New("boom")})
	rec := doRequest(t, srv.Handler(), "GET", "/templates")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rec.Code, rec.Body.String())
	}
}

// TestHandleTemplatesPartial covers the HTMX "load more" endpoint for /templates:
// the RFC3339Nano ?before= cursor round-trips correctly back into a time.Time.
func TestHandleTemplatesPartial(t *testing.T) {
	before := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	store := &fakeStore{templates: []db.TemplateRegistryRow{{TemplateAddress: "addr-0", TemplateName: "Older"}}}
	srv := newTestServer(t, store)

	rec := doRequest(t, srv.Handler(), "GET", "/templates/partial?before="+before.Format(time.RFC3339Nano))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if !store.lastBeforeTimeArg.Equal(before) {
		t.Errorf("lastBeforeTimeArg = %v, want %v", store.lastBeforeTimeArg, before)
	}
	if !strings.Contains(rec.Body.String(), "Older") {
		t.Errorf("body missing expected row, got: %s", rec.Body.String())
	}
}

// TestHandleTemplatesPartial_InvalidBefore covers the 400 path for a malformed/
// missing ?before= param.
func TestHandleTemplatesPartial_InvalidBefore(t *testing.T) {
	srv := newTestServer(t, &fakeStore{})
	rec := doRequest(t, srv.Handler(), "GET", "/templates/partial?before=not-a-time")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

// TestHandleHealth covers GET /health: always 200, reporting the database as
// reachable when the underlying (cheap) query succeeds.
func TestHandleHealth(t *testing.T) {
	srv := newTestServer(t, &fakeStore{summary: db.LiveValidatorEpochSummary{}})
	rec := doRequest(t, srv.Handler(), "GET", "/health")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "reachable") {
		t.Errorf("body = %q, want it to report the database as reachable", rec.Body.String())
	}
}

// TestHandleHealth_DegradedDatabase covers /health's "always 200, report degraded
// database in the body" behavior per this handler's own doc comment - a Postgres
// outage is not reason to fail this liveness probe for the HTTP process itself.
func TestHandleHealth_DegradedDatabase(t *testing.T) {
	srv := newTestServer(t, &fakeStore{summaryErr: errors.New("connection refused")})
	rec := doRequest(t, srv.Handler(), "GET", "/health")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 even when the database is degraded; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "degraded") {
		t.Errorf("body = %q, want it to report the database as degraded", rec.Body.String())
	}
}
