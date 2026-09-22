package server

import (
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Snipa22/go-tari-ootle-explorer/internal/db"
	"gopkg.in/yaml.v3"
)

// decodeJSON is a small test helper that fails the test if rec's body isn't valid
// JSON decodable into v - every /api/* response, success or error, must satisfy
// this per DISPATCH_BRIEF_JSON_API.md's "valid JSON even on error" requirement.
func decodeJSON(t *testing.T, body []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(body, v); err != nil {
		t.Fatalf("response body is not valid JSON: %v; body=%s", err, body)
	}
}

func assertJSONContentType(t *testing.T, rec interface{ Header() http.Header }) {
	t.Helper()
	got := rec.Header().Get("Content-Type")
	if got != "application/json; charset=utf-8" {
		t.Errorf("Content-Type = %q, want %q", got, "application/json; charset=utf-8")
	}
}

// ---- GET /api/blocks ----

func TestHandleAPIBlocks_HappyPath(t *testing.T) {
	store := &fakeStore{blocks: []db.OotleBlock{
		{BlockID: "block-2", Height: 20, Epoch: 1, Timestamp: 100, CommandCount: 0},
		{BlockID: "block-1", Height: 10, Epoch: 1, Timestamp: 90, CommandCount: 0},
	}}
	srv := newTestServer(t, store)

	rec := doRequest(t, srv.Handler(), "GET", "/api/blocks")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	assertJSONContentType(t, rec)

	var got []db.OotleBlock
	decodeJSON(t, rec.Body.Bytes(), &got)
	if len(got) != 2 || got[0].BlockID != "block-2" || got[1].BlockID != "block-1" {
		t.Errorf("got %+v, want 2 blocks in order [block-2, block-1]", got)
	}
	// Default first page uses math.MaxInt64 as the before-cursor sentinel, same as
	// handleBlocksList.
	if store.lastBeforeHeightArg != math.MaxInt64 {
		t.Errorf("lastBeforeHeightArg = %d, want math.MaxInt64", store.lastBeforeHeightArg)
	}
	if store.lastLimitArg != PageSize {
		t.Errorf("lastLimitArg = %d, want PageSize (%d)", store.lastLimitArg, PageSize)
	}
	// snake_case JSON tags.
	if !strings.Contains(rec.Body.String(), `"block_id"`) {
		t.Errorf("body missing snake_case block_id key, got: %s", rec.Body.String())
	}
}

func TestHandleAPIBlocks_EmptyResultIsJSONArrayNotNull(t *testing.T) {
	srv := newTestServer(t, &fakeStore{})
	rec := doRequest(t, srv.Handler(), "GET", "/api/blocks")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got := strings.TrimSpace(rec.Body.String()); got != "[]" {
		t.Errorf("body = %q, want empty JSON array \"[]\" (not null)", got)
	}
}

func TestHandleAPIBlocks_BeforeAndLimitParams(t *testing.T) {
	store := &fakeStore{blocks: []db.OotleBlock{{BlockID: "block-1", Height: 5}}}
	srv := newTestServer(t, store)

	rec := doRequest(t, srv.Handler(), "GET", "/api/blocks?before=42&limit=5")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if store.lastBeforeHeightArg != 42 {
		t.Errorf("lastBeforeHeightArg = %d, want 42", store.lastBeforeHeightArg)
	}
	if store.lastLimitArg != 5 {
		t.Errorf("lastLimitArg = %d, want 5", store.lastLimitArg)
	}
}

func TestHandleAPIBlocks_LimitClampedToMax(t *testing.T) {
	store := &fakeStore{}
	srv := newTestServer(t, store)

	rec := doRequest(t, srv.Handler(), "GET", "/api/blocks?limit=99999")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if store.lastLimitArg != MaxAPILimit {
		t.Errorf("lastLimitArg = %d, want clamped MaxAPILimit (%d)", store.lastLimitArg, MaxAPILimit)
	}
}

func TestHandleAPIBlocks_InvalidBefore(t *testing.T) {
	srv := newTestServer(t, &fakeStore{})
	rec := doRequest(t, srv.Handler(), "GET", "/api/blocks?before=not-a-number")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	assertJSONContentType(t, rec)
	var errBody map[string]string
	decodeJSON(t, rec.Body.Bytes(), &errBody)
	if errBody["error"] == "" {
		t.Errorf("body missing non-empty \"error\" field, got: %s", rec.Body.String())
	}
}

func TestHandleAPIBlocks_InvalidLimit(t *testing.T) {
	srv := newTestServer(t, &fakeStore{})
	for _, limit := range []string{"not-a-number", "0", "-5"} {
		rec := doRequest(t, srv.Handler(), "GET", "/api/blocks?limit="+limit)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("limit=%q: status = %d, want 400; body=%s", limit, rec.Code, rec.Body.String())
		}
	}
}

func TestHandleAPIBlocks_DBError(t *testing.T) {
	srv := newTestServer(t, &fakeStore{blocksErr: errors.New("boom")})
	rec := doRequest(t, srv.Handler(), "GET", "/api/blocks")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rec.Code, rec.Body.String())
	}
	assertJSONContentType(t, rec)
	var errBody map[string]string
	decodeJSON(t, rec.Body.Bytes(), &errBody)
	if errBody["error"] == "" {
		t.Errorf("body missing non-empty \"error\" field, got: %s", rec.Body.String())
	}
}

// ---- GET /api/validators ----

func TestHandleAPIValidators_HappyPath(t *testing.T) {
	status := "Running"
	store := &fakeStore{validators: []db.ValidatorRow{
		{PublicKey: "pubkey-a", LastSeenEpoch: 10, ShardGroupStart: 1, ShardGroupEndInclusive: 128, ConsensusStatus: &status, LastCheckedAt: time.Now()},
	}}
	srv := newTestServer(t, store)

	rec := doRequest(t, srv.Handler(), "GET", "/api/validators")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	assertJSONContentType(t, rec)

	var got []db.ValidatorRow
	decodeJSON(t, rec.Body.Bytes(), &got)
	if len(got) != 1 || got[0].PublicKey != "pubkey-a" || got[0].ConsensusStatus == nil || *got[0].ConsensusStatus != "Running" {
		t.Errorf("got %+v, want 1 validator pubkey-a with ConsensusStatus=Running", got)
	}
	if !strings.Contains(rec.Body.String(), `"public_key"`) {
		t.Errorf("body missing snake_case public_key key, got: %s", rec.Body.String())
	}
}

func TestHandleAPIValidators_EmptyResultIsJSONArrayNotNull(t *testing.T) {
	srv := newTestServer(t, &fakeStore{})
	rec := doRequest(t, srv.Handler(), "GET", "/api/validators")
	if got := strings.TrimSpace(rec.Body.String()); got != "[]" {
		t.Errorf("body = %q, want empty JSON array \"[]\" (not null)", got)
	}
}

func TestHandleAPIValidators_DBError(t *testing.T) {
	srv := newTestServer(t, &fakeStore{validatorsErr: errors.New("boom")})
	rec := doRequest(t, srv.Handler(), "GET", "/api/validators")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rec.Code, rec.Body.String())
	}
	assertJSONContentType(t, rec)
}

// ---- GET /api/burn-claims ----

func TestHandleAPIBurnClaims_StatusFilter(t *testing.T) {
	store := &fakeStore{burnClaims: []db.BurnClaim{{L1BurnTxHash: "kernel-1", Commitment: "commit-1", BurnHeight: 100, Status: "stuck"}}}
	srv := newTestServer(t, store)

	rec := doRequest(t, srv.Handler(), "GET", "/api/burn-claims?status=stuck")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if store.lastStatusArg != "stuck" {
		t.Errorf("lastStatusArg = %q, want \"stuck\"", store.lastStatusArg)
	}
	var got []db.BurnClaim
	decodeJSON(t, rec.Body.Bytes(), &got)
	if len(got) != 1 || got[0].L1BurnTxHash != "kernel-1" {
		t.Errorf("got %+v, want 1 claim kernel-1", got)
	}

	rec2 := doRequest(t, srv.Handler(), "GET", "/api/burn-claims")
	if rec2.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec2.Code)
	}
	if store.lastStatusArg != "" {
		t.Errorf("lastStatusArg = %q, want \"\" (default: show all)", store.lastStatusArg)
	}
}

func TestHandleAPIBurnClaims_EmptyResultIsJSONArrayNotNull(t *testing.T) {
	srv := newTestServer(t, &fakeStore{})
	rec := doRequest(t, srv.Handler(), "GET", "/api/burn-claims")
	if got := strings.TrimSpace(rec.Body.String()); got != "[]" {
		t.Errorf("body = %q, want empty JSON array \"[]\" (not null)", got)
	}
}

func TestHandleAPIBurnClaims_DBError(t *testing.T) {
	srv := newTestServer(t, &fakeStore{burnClaimsErr: errors.New("boom")})
	rec := doRequest(t, srv.Handler(), "GET", "/api/burn-claims")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rec.Code, rec.Body.String())
	}
	assertJSONContentType(t, rec)
}

// ---- GET /api/templates ----

func TestHandleAPITemplates_HappyPath_MetadataInlinedAsRawJSON(t *testing.T) {
	seenAt := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	codeSize := int64(1024)
	store := &fakeStore{templates: []db.TemplateRegistryRow{
		{
			TemplateAddress: "addr-1", TemplateName: "Tmpl", AuthorPublicKey: "author-1", AtEpoch: 5,
			CodeSize: &codeSize, FirstSeenAt: seenAt,
			Metadata:   []byte(`{"foo":"bar"}`),
			Definition: []byte(`{"functions":[]}`),
		},
		{
			// No metadata/definition observed yet - must render as JSON null, not
			// an empty/base64 string.
			TemplateAddress: "addr-2", TemplateName: "Tmpl2", AuthorPublicKey: "author-2", AtEpoch: 5,
			FirstSeenAt: seenAt,
		},
	}}
	srv := newTestServer(t, store)

	rec := doRequest(t, srv.Handler(), "GET", "/api/templates")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	assertJSONContentType(t, rec)

	var got []map[string]any
	decodeJSON(t, rec.Body.Bytes(), &got)
	if len(got) != 2 {
		t.Fatalf("got %d entries, want 2", len(got))
	}
	// First entry: metadata/definition should decode as real nested JSON objects,
	// not base64 strings.
	meta, ok := got[0]["metadata"].(map[string]any)
	if !ok || meta["foo"] != "bar" {
		t.Errorf("got[0][\"metadata\"] = %#v, want nested object {\"foo\":\"bar\"}", got[0]["metadata"])
	}
	def, ok := got[0]["definition"].(map[string]any)
	if !ok {
		t.Errorf("got[0][\"definition\"] = %#v, want nested object", got[0]["definition"])
	}
	_ = def
	// Second entry: unset metadata/definition should be JSON null, present as a key.
	if v, present := got[1]["metadata"]; !present || v != nil {
		t.Errorf("got[1][\"metadata\"] = %#v (present=%v), want null", v, present)
	}
	if v, present := got[1]["definition"]; !present || v != nil {
		t.Errorf("got[1][\"definition\"] = %#v (present=%v), want null", v, present)
	}
	if got[0]["template_address"] != "addr-1" {
		t.Errorf("got[0][\"template_address\"] = %v, want addr-1", got[0]["template_address"])
	}
}

func TestHandleAPITemplates_EmptyResultIsJSONArrayNotNull(t *testing.T) {
	srv := newTestServer(t, &fakeStore{})
	rec := doRequest(t, srv.Handler(), "GET", "/api/templates")
	if got := strings.TrimSpace(rec.Body.String()); got != "[]" {
		t.Errorf("body = %q, want empty JSON array \"[]\" (not null)", got)
	}
}

func TestHandleAPITemplates_BeforeAndLimitParams(t *testing.T) {
	before := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	store := &fakeStore{}
	srv := newTestServer(t, store)

	rec := doRequest(t, srv.Handler(), "GET", "/api/templates?before="+before.Format(time.RFC3339Nano)+"&limit=7")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if !store.lastBeforeTimeArg.Equal(before) {
		t.Errorf("lastBeforeTimeArg = %v, want %v", store.lastBeforeTimeArg, before)
	}
	if store.lastTemplatesLimitArg != 7 {
		t.Errorf("lastTemplatesLimitArg = %d, want 7", store.lastTemplatesLimitArg)
	}
}

func TestHandleAPITemplates_InvalidBefore(t *testing.T) {
	srv := newTestServer(t, &fakeStore{})
	rec := doRequest(t, srv.Handler(), "GET", "/api/templates?before=not-a-time")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	assertJSONContentType(t, rec)
}

func TestHandleAPITemplates_InvalidLimit(t *testing.T) {
	srv := newTestServer(t, &fakeStore{})
	rec := doRequest(t, srv.Handler(), "GET", "/api/templates?limit=0")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleAPITemplates_DBError(t *testing.T) {
	srv := newTestServer(t, &fakeStore{templatesErr: errors.New("boom")})
	rec := doRequest(t, srv.Handler(), "GET", "/api/templates")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rec.Code, rec.Body.String())
	}
	assertJSONContentType(t, rec)
}

// ---- GET /api/tip-info ----

func TestHandleAPITipInfo_HappyPath(t *testing.T) {
	store := &fakeStore{
		blocks:  []db.OotleBlock{{BlockID: "block-2", Height: 20, Epoch: 5, Timestamp: 100, CommandCount: 3}},
		summary: db.LiveValidatorEpochSummary{Epoch: 5, Count: 7},
		burnClaims: []db.BurnClaim{
			{L1BurnTxHash: "kernel-1", Commitment: "commit-1", BurnHeight: 100, Status: "stuck"},
			{L1BurnTxHash: "kernel-2", Commitment: "commit-2", BurnHeight: 101, Status: "stuck"},
		},
	}
	srv := newTestServer(t, store)

	rec := doRequest(t, srv.Handler(), "GET", "/api/tip-info")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	assertJSONContentType(t, rec)

	var got map[string]any
	decodeJSON(t, rec.Body.Bytes(), &got)

	latestBlock, ok := got["latest_block"].(map[string]any)
	if !ok || latestBlock["block_id"] != "block-2" {
		t.Errorf("got[\"latest_block\"] = %#v, want block-2", got["latest_block"])
	}
	summary, ok := got["validator_epoch_summary"].(map[string]any)
	if !ok || summary["epoch"] != float64(5) || summary["count"] != float64(7) {
		t.Errorf("got[\"validator_epoch_summary\"] = %#v, want {epoch:5 count:7}", got["validator_epoch_summary"])
	}
	if got["stuck_burn_claims_count"] != float64(2) {
		t.Errorf("got[\"stuck_burn_claims_count\"] = %v, want 2", got["stuck_burn_claims_count"])
	}
	// The stuck-claims filter must have been threaded through, not "all statuses".
	if store.lastStatusArg != "stuck" {
		t.Errorf("lastStatusArg = %q, want \"stuck\"", store.lastStatusArg)
	}
	// A compact count, not the full claim rows.
	if _, present := got["stuck_burn_claims"]; present {
		t.Errorf("body unexpectedly includes full stuck_burn_claims rows: %s", rec.Body.String())
	}
	// No error fields on the happy path.
	for _, key := range []string{"latest_block_error", "validator_epoch_summary_error", "stuck_burn_claims_count_error"} {
		if _, present := got[key]; present {
			t.Errorf("body unexpectedly includes %q on the happy path: %s", key, rec.Body.String())
		}
	}
}

func TestHandleAPITipInfo_EmptyBlocksTableIsNull(t *testing.T) {
	store := &fakeStore{summary: db.LiveValidatorEpochSummary{}}
	srv := newTestServer(t, store)

	rec := doRequest(t, srv.Handler(), "GET", "/api/tip-info")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	var got map[string]any
	decodeJSON(t, rec.Body.Bytes(), &got)
	if v, present := got["latest_block"]; !present || v != nil {
		t.Errorf("got[\"latest_block\"] = %#v (present=%v), want null", v, present)
	}
	if got["stuck_burn_claims_count"] != float64(0) {
		t.Errorf("got[\"stuck_burn_claims_count\"] = %v, want 0", got["stuck_burn_claims_count"])
	}
}

func TestHandleAPITipInfo_PartialDegradation(t *testing.T) {
	store := &fakeStore{
		blocksErr: errors.New("boom"),
		summary:   db.LiveValidatorEpochSummary{Epoch: 9, Count: 2},
		burnClaims: []db.BurnClaim{
			{L1BurnTxHash: "kernel-1", Commitment: "commit-1", BurnHeight: 100, Status: "stuck"},
		},
	}
	srv := newTestServer(t, store)

	rec := doRequest(t, srv.Handler(), "GET", "/api/tip-info")
	// One sub-query failing must still be a 200 with an inline error, not a 500.
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	assertJSONContentType(t, rec)

	var got map[string]any
	decodeJSON(t, rec.Body.Bytes(), &got)
	if v, present := got["latest_block"]; !present || v != nil {
		t.Errorf("got[\"latest_block\"] = %#v (present=%v), want null on error", v, present)
	}
	if got["latest_block_error"] == "" || got["latest_block_error"] == nil {
		t.Errorf("body missing non-empty \"latest_block_error\" field, got: %s", rec.Body.String())
	}
	summary, ok := got["validator_epoch_summary"].(map[string]any)
	if !ok || summary["epoch"] != float64(9) || summary["count"] != float64(2) {
		t.Errorf("got[\"validator_epoch_summary\"] = %#v, want {epoch:9 count:2} (unaffected by the blocks error)", got["validator_epoch_summary"])
	}
	if got["stuck_burn_claims_count"] != float64(1) {
		t.Errorf("got[\"stuck_burn_claims_count\"] = %v, want 1 (unaffected by the blocks error)", got["stuck_burn_claims_count"])
	}
	if _, present := got["validator_epoch_summary_error"]; present {
		t.Errorf("body unexpectedly includes validator_epoch_summary_error: %s", rec.Body.String())
	}
}

func TestHandleAPITipInfo_AllSubQueriesFail(t *testing.T) {
	store := &fakeStore{
		blocksErr:     errors.New("boom"),
		summaryErr:    errors.New("boom"),
		burnClaimsErr: errors.New("boom"),
	}
	srv := newTestServer(t, store)

	rec := doRequest(t, srv.Handler(), "GET", "/api/tip-info")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 when all three sub-queries fail; body=%s", rec.Code, rec.Body.String())
	}
	assertJSONContentType(t, rec)
	var errBody map[string]string
	decodeJSON(t, rec.Body.Bytes(), &errBody)
	if errBody["error"] == "" {
		t.Errorf("body missing non-empty \"error\" field, got: %s", rec.Body.String())
	}
}

// ---- GET /api/health ----

func TestHandleAPIHealth_Reachable(t *testing.T) {
	srv := newTestServer(t, &fakeStore{summary: db.LiveValidatorEpochSummary{}})
	rec := doRequest(t, srv.Handler(), "GET", "/api/health")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	assertJSONContentType(t, rec)

	var body map[string]string
	decodeJSON(t, rec.Body.Bytes(), &body)
	if body["status"] != "ok" || body["database"] != "reachable" {
		t.Errorf("body = %+v, want status=ok database=reachable", body)
	}
}

// ---- GET /api/spec ----

func TestHandleAPISpec_YAML(t *testing.T) {
	srv := newTestServer(t, &fakeStore{})
	rec := doRequest(t, srv.Handler(), "GET", "/api/spec")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "application/yaml; charset=utf-8" {
		t.Errorf("Content-Type = %q, want %q", got, "application/yaml; charset=utf-8")
	}
	body := rec.Body.Bytes()
	if len(body) == 0 {
		t.Fatal("body is empty, want non-empty YAML spec")
	}
	var doc map[string]any
	if err := yaml.Unmarshal(body, &doc); err != nil {
		t.Fatalf("body is not valid YAML: %v; body=%s", err, body)
	}
	if doc["openapi"] == nil {
		t.Errorf("parsed YAML missing top-level \"openapi\" key, got: %+v", doc)
	}
	if _, ok := doc["paths"].(map[string]any); !ok {
		t.Errorf("parsed YAML missing top-level \"paths\" map, got: %+v", doc["paths"])
	}
}

func TestHandleAPISpec_JSONFormat(t *testing.T) {
	srv := newTestServer(t, &fakeStore{})
	rec := doRequest(t, srv.Handler(), "GET", "/api/spec?format=json")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	assertJSONContentType(t, rec)
	var doc map[string]any
	decodeJSON(t, rec.Body.Bytes(), &doc)
	if doc["openapi"] == nil {
		t.Errorf("parsed JSON missing top-level \"openapi\" key, got: %+v", doc)
	}
	paths, ok := doc["paths"].(map[string]any)
	if !ok {
		t.Fatalf("parsed JSON missing top-level \"paths\" map, got: %+v", doc["paths"])
	}
	for _, route := range []string{"/api/blocks", "/api/validators", "/api/burn-claims", "/api/templates", "/api/tip-info", "/api/health"} {
		if _, present := paths[route]; !present {
			t.Errorf("parsed JSON spec missing path %q", route)
		}
	}
}

// ---- GET /api/docs ----

func TestHandleAPIDocs(t *testing.T) {
	srv := newTestServer(t, &fakeStore{})
	rec := doRequest(t, srv.Handler(), "GET", "/api/docs")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q, want %q", got, "text/html; charset=utf-8")
	}
	body := rec.Body.String()
	if !strings.Contains(body, "swagger-ui-bundle.js") {
		t.Errorf("body missing Swagger UI CDN script reference, got: %s", body)
	}
	if !strings.Contains(body, "/api/spec") {
		t.Errorf("body missing reference to /api/spec as the Swagger UI spec URL, got: %s", body)
	}
}

func TestHandleAPIHealth_DegradedDatabase(t *testing.T) {
	srv := newTestServer(t, &fakeStore{summaryErr: errors.New("connection refused")})
	rec := doRequest(t, srv.Handler(), "GET", "/api/health")
	// Always 200 - a degraded DB is reported in the body, not via a non-200
	// status, matching handleHealth's own behavior.
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 even when the database is degraded; body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]string
	decodeJSON(t, rec.Body.Bytes(), &body)
	if body["status"] != "ok" || body["database"] != "degraded" {
		t.Errorf("body = %+v, want status=ok database=degraded", body)
	}
	if body["error"] == "" {
		t.Errorf("body missing non-empty \"error\" field describing the degradation, got: %+v", body)
	}
}
