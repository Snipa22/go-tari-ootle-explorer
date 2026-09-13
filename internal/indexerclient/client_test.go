package indexerclient

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// readFixture returns the raw bytes of a real, live-captured response saved under
// testdata/ - see each file's originating curl command in this test file's comments.
// Fixtures are pasted, not invented, per DISPATCH_BRIEF.md's requirement.
func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return raw
}

// newFixtureServer returns an httptest.Server that serves fixture (a real captured
// response body) with the given status code for every request, plus a Client pointed
// at it.
func newFixtureServer(t *testing.T, status int, fixture []byte) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(fixture)
	}))
	t.Cleanup(srv.Close)
	return NewWithHTTPClient(srv.URL, srv.Client()), srv
}

// TestGetInfo_RealFixture was captured with:
//
//	curl -sS https://ootle-indexer-a.tari.com/info
//
// against esmeralda, epoch 10990, on 2026-09-13.
func TestGetInfo_RealFixture(t *testing.T) {
	client, _ := newFixtureServer(t, http.StatusOK, readFixture(t, "info.json"))

	info, err := client.GetInfo(context.Background())
	if err != nil {
		t.Fatalf("GetInfo() error = %v", err)
	}
	if info.Network != "esmeralda" {
		t.Errorf("Network = %q, want esmeralda", info.Network)
	}
	if info.NetworkByte != 38 {
		t.Errorf("NetworkByte = %d, want 38", info.NetworkByte)
	}
	if info.CurrentEpoch != 10990 {
		t.Errorf("CurrentEpoch = %d, want 10990", info.CurrentEpoch)
	}
	if info.Version != "0.40.2" {
		t.Errorf("Version = %q, want 0.40.2", info.Version)
	}
	if info.SidechainID != nil {
		t.Errorf("SidechainID = %v, want nil", info.SidechainID)
	}
	if !info.IndexGossipedTransactions || !info.VerifySubstateProofs || !info.IndexesAllEvents {
		t.Errorf("expected all-true bool fields, got %+v", info)
	}
	if info.SubstateCacheMaxServeLagSecs != 300 {
		t.Errorf("SubstateCacheMaxServeLagSecs = %d, want 300", info.SubstateCacheMaxServeLagSecs)
	}
}

// TestListValidators_RealFixture was captured with:
//
//	curl -sS https://ootle-indexer-a.tari.com/validators
//
// against esmeralda, epoch 10990, on 2026-09-13.
func TestListValidators_RealFixture(t *testing.T) {
	client, _ := newFixtureServer(t, http.StatusOK, readFixture(t, "validators.json"))

	resp, err := client.ListValidators(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListValidators() error = %v", err)
	}
	if resp.Epoch != 10990 {
		t.Errorf("Epoch = %d, want 10990", resp.Epoch)
	}
	if len(resp.Validators) != 4 {
		t.Fatalf("len(Validators) = %d, want 4", len(resp.Validators))
	}
	v := resp.Validators[0]
	if v.PublicKey != "10780fe1b4d31f8350e4b881dccd8f8210ce056050ac6dd8cca88ca16710b044" {
		t.Errorf("PublicKey = %q", v.PublicKey)
	}
	if v.PeerID != "12D3L7AUwuH5t8xS61cqbACurQEccTQVu4WoH6hRCRWbxV9oXvMy" {
		t.Errorf("PeerID = %q", v.PeerID)
	}
	if v.ShardGroup != (ShardGroup{Start: 1, EndInclusive: 256}) {
		t.Errorf("ShardGroup = %+v, want {1 256}", v.ShardGroup)
	}
	if v.StartEpoch != 10517 {
		t.Errorf("StartEpoch = %d, want 10517", v.StartEpoch)
	}
	if v.EndEpoch != nil {
		t.Errorf("EndEpoch = %v, want nil", v.EndEpoch)
	}
	if v.VotePower != 1 {
		t.Errorf("VotePower = %d, want 1", v.VotePower)
	}
	if v.FeeClaimPublicKey != "3c6eebdcb2939ebad646a9d464874005aa979f1e575b72cd2a690289f10e5e50" {
		t.Errorf("FeeClaimPublicKey = %q", v.FeeClaimPublicKey)
	}
}

// TestListValidators_SetsEpochQueryParam confirms the optional epoch filter is sent as
// a query param when provided - the real handler (validators.rs) reads it via
// axum::extract::Query, so this must be a query param, not a path segment or header.
func TestListValidators_SetsEpochQueryParam(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(readFixture(t, "validators.json"))
	}))
	defer srv.Close()
	client := NewWithHTTPClient(srv.URL, srv.Client())

	epoch := uint64(10517)
	if _, err := client.ListValidators(context.Background(), &epoch); err != nil {
		t.Fatalf("ListValidators() error = %v", err)
	}
	if gotQuery != "epoch=10517" {
		t.Errorf("RawQuery = %q, want epoch=10517", gotQuery)
	}
}

// TestListTemplateCatalogue_RealFixture was captured with:
//
//	curl -sS "https://ootle-indexer-a.tari.com/templates/catalogue?limit=3"
//
// against esmeralda, epoch 10990, on 2026-09-13.
func TestListTemplateCatalogue_RealFixture(t *testing.T) {
	client, _ := newFixtureServer(t, http.StatusOK, readFixture(t, "templates_catalogue.json"))

	resp, err := client.ListTemplateCatalogue(context.Background(), 3, "")
	if err != nil {
		t.Fatalf("ListTemplateCatalogue() error = %v", err)
	}
	if len(resp.Entries) != 3 {
		t.Fatalf("len(Entries) = %d, want 3", len(resp.Entries))
	}
	first := resp.Entries[0]
	if first.TemplateAddress != "0000000000000000000000000000000000000000000000000000000000000000" {
		t.Errorf("TemplateAddress = %q", first.TemplateAddress)
	}
	if first.TemplateName != "Account" {
		t.Errorf("TemplateName = %q, want Account", first.TemplateName)
	}
	if first.BinaryHash != "cc58782ff4740a70aed33a8aa4d34ad5d0cabfc3495c87e5ec59359e235a382d" {
		t.Errorf("BinaryHash = %q", first.BinaryHash)
	}
	if first.AtEpoch != 0 {
		t.Errorf("AtEpoch = %d, want 0", first.AtEpoch)
	}
	// None of the live entries in this fixture have metadata_hash set - confirms the
	// optional field decodes to nil rather than a zero-value empty string when absent.
	if first.MetadataHash != nil {
		t.Errorf("MetadataHash = %v, want nil (field omitted upstream)", first.MetadataHash)
	}
}

// TestListTemplateCatalogue_DecodesOptionalMetadataHash is a synthetic (NOT
// live-captured) fixture covering TemplateCatalogueItem.metadata_hash when it IS
// present - none of the real templates checked during this dispatch had it set, so
// this exercises the Optional<MetadataHash> branch of the real struct
// (clients/tari_indexer_client/src/types.rs) that the live fixture alone can't cover.
func TestListTemplateCatalogue_DecodesOptionalMetadataHash(t *testing.T) {
	synthetic := `{"entries":[{"template_address":"aa","template_name":"Foo","author_public_key":"bb","binary_hash":"cc","at_epoch":42,"metadata_hash":"dd"}]}`
	client, _ := newFixtureServer(t, http.StatusOK, []byte(synthetic))

	resp, err := client.ListTemplateCatalogue(context.Background(), 0, "")
	if err != nil {
		t.Fatalf("ListTemplateCatalogue() error = %v", err)
	}
	if len(resp.Entries) != 1 || resp.Entries[0].MetadataHash == nil || *resp.Entries[0].MetadataHash != "dd" {
		t.Fatalf("Entries = %+v, want one entry with MetadataHash = \"dd\"", resp.Entries)
	}
}

// TestListTemplateCatalogue_SetsCursorQueryParams confirms pagination is sent as
// limit+after (cursor), NOT limit+offset - see ListTemplateCatalogue's doc comment for
// why this deliberately deviates from DISPATCH_BRIEF.md's snippet.
func TestListTemplateCatalogue_SetsCursorQueryParams(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(readFixture(t, "templates_catalogue.json"))
	}))
	defer srv.Close()
	client := NewWithHTTPClient(srv.URL, srv.Client())

	if _, err := client.ListTemplateCatalogue(context.Background(), 20, "0000000000000000000000000000000000000000000000000000000000000001"); err != nil {
		t.Fatalf("ListTemplateCatalogue() error = %v", err)
	}
	got, err := url.ParseQuery(gotQuery)
	if err != nil {
		t.Fatalf("parse query %q: %v", gotQuery, err)
	}
	if got.Get("limit") != "20" {
		t.Errorf("limit = %q, want 20", got.Get("limit"))
	}
	if got.Get("after") != "0000000000000000000000000000000000000000000000000000000000000001" {
		t.Errorf("after = %q", got.Get("after"))
	}
	if got.Has("offset") {
		t.Errorf("query has offset param, want none (real API has no offset param)")
	}
}

// TestGetLatestEpochCheckpoint_RealFixture was captured with:
//
//	curl -sS https://ootle-indexer-a.tari.com/epoch-checkpoints/latest
//
// against esmeralda, epoch 10990 (checkpoint epoch 10989), on 2026-09-13.
func TestGetLatestEpochCheckpoint_RealFixture(t *testing.T) {
	client, _ := newFixtureServer(t, http.StatusOK, readFixture(t, "epoch_checkpoint_latest.json"))

	cp, err := client.GetLatestEpochCheckpoint(context.Background())
	if err != nil {
		t.Fatalf("GetLatestEpochCheckpoint() error = %v", err)
	}
	h := cp.Header
	if h.Epoch != 10989 {
		t.Errorf("Header.Epoch = %d, want 10989", h.Epoch)
	}
	if h.Height != 1794 {
		t.Errorf("Header.Height = %d, want 1794", h.Height)
	}
	if h.Network != 38 {
		t.Errorf("Header.Network = %d, want 38", h.Network)
	}
	if h.ShardGroup != (ShardGroup{Start: 1, EndInclusive: 256}) {
		t.Errorf("Header.ShardGroup = %+v, want {1 256}", h.ShardGroup)
	}
	if h.ProposedBy != "c480fe3a76f03dcdce8d7c85ac7d0001ec97b667260172709b23818c8707e503" {
		t.Errorf("Header.ProposedBy = %q", h.ProposedBy)
	}
	if h.ParentID != "a3b677ebfbb91743463e99aacd46eb21b76390e15399132aaaf57008231ccf3c" {
		t.Errorf("Header.ParentID = %q", h.ParentID)
	}
	if h.JustifyID != "862ef172ca25cd407191aab78b89c8dac8b93a6ec8baef5fa39db692ce77cf60" {
		t.Errorf("Header.JustifyID = %q", h.JustifyID)
	}
	if h.CommandMerkleRoot != "2198c498437f9d3490b9f0aabdccd234b5ee1eff1ac48a85914f4b846d3747c2" {
		t.Errorf("Header.CommandMerkleRoot = %q", h.CommandMerkleRoot)
	}
	if h.StateMerkleRoot != "46cb70bc6034ad3109f3c7951d45a7c1a2db8dfaa4396f72d2d5fb888b0649c3" {
		t.Errorf("Header.StateMerkleRoot = %q", h.StateMerkleRoot)
	}
	if h.EpochHash != "812c83ec5551723e58ab8f6c1fd3d8c8be567e106d1964c5dda97951ff894d16" {
		t.Errorf("Header.EpochHash = %q", h.EpochHash)
	}
	if h.MetadataHash != "d74c0bbc91ee2b52157f697b806e2c64d4486e4f9dc2536f8ffaeb7dee95bab7" {
		t.Errorf("Header.MetadataHash = %q", h.MetadataHash)
	}
	if h.AccumulatedData.TotalExhaustBurn != 510776 {
		t.Errorf("Header.AccumulatedData.TotalExhaustBurn = %d, want 510776", h.AccumulatedData.TotalExhaustBurn)
	}
}

// TestDoGet_StatusError_RealFixture was captured with:
//
//	curl -sS -w '\n%{http_code}' \
//	  https://ootle-indexer-a.tari.com/templates/catalogue/0000000000000000000000000000000000000000000000000000000000009999
//
// (a well-formed but nonexistent template address), which returned HTTP 404 with the
// real indexer's ErrorResponse JSON shape ({"error": "..."})  - confirmed against
// applications/tari_indexer/src/rest_api/error.rs's IntoResponse impl.
func TestGetTemplateCatalogueEntryLike_StatusError_RealFixture(t *testing.T) {
	client, _ := newFixtureServer(t, http.StatusNotFound, readFixture(t, "error_not_found.json"))

	_, err := client.ListTemplateCatalogue(context.Background(), 0, "")
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	var statusErr *StatusError
	if !errors.As(err, &statusErr) {
		t.Fatalf("errors.As(%v, &StatusError{}) = false, want true", err)
	}
	if statusErr.StatusCode != http.StatusNotFound {
		t.Errorf("StatusCode = %d, want 404", statusErr.StatusCode)
	}
	if !strings.Contains(statusErr.Body, "not found in the catalogue") {
		t.Errorf("Body = %q, want it to contain the real error message", statusErr.Body)
	}
}

// TestDoGet_DecodeError confirms a 2xx response with a body that isn't valid JSON for
// the expected type surfaces as a *DecodeError, not a *StatusError or generic error -
// the explorer's polling loop needs this distinction to know its own Go types (not the
// indexer) are the problem.
func TestDoGet_DecodeError(t *testing.T) {
	client, _ := newFixtureServer(t, http.StatusOK, []byte("not json"))

	_, err := client.GetInfo(context.Background())
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	var decodeErr *DecodeError
	if !errors.As(err, &decodeErr) {
		t.Fatalf("errors.As(%v, &DecodeError{}) = false, want true", err)
	}
}

// TestGetSubstate_RealFixture_Found was captured with:
//
//	curl -sS https://ootle-indexer-a.tari.com/substates/resource_0101010101010101010101010101010101010101010101010101010101010101
//
// against esmeralda on 2026-09-13 - the real, live STEALTH_TARI_RESOURCE_ADDRESS
// substate (crates/template_lib_types/src/constants.rs), used here purely to confirm
// the real 200 response shape (externally-tagged {"Resource": {...}} substate value -
// see SubstateValue's doc comment) since no real ClaimedOutputTombstone substate was
// found to exist on this network during this dispatch (see the dispatch report).
func TestGetSubstate_RealFixture_Found(t *testing.T) {
	client, _ := newFixtureServer(t, http.StatusOK, readFixture(t, "substate_resource_found.json"))

	resp, err := client.GetSubstate(context.Background(), "resource_0101010101010101010101010101010101010101010101010101010101010101")
	if err != nil {
		t.Fatalf("GetSubstate() error = %v", err)
	}
	if resp.Version != 0 {
		t.Errorf("Version = %d, want 0", resp.Version)
	}
	if !resp.Verified {
		t.Errorf("Verified = false, want true")
	}
	if resp.Substate.ClaimedOutputTombstone != nil {
		t.Errorf("ClaimedOutputTombstone = %+v, want nil (this fixture is a Resource substate)", resp.Substate.ClaimedOutputTombstone)
	}
}

// TestGetSubstate_RealFixture_NotFound was captured with:
//
//	curl -sS -w '\n%{http_code}' \
//	  https://ootle-indexer-a.tari.com/substates/tombstone_0000000000000000000000000000000000000000000000000000000000000000
//
// against esmeralda on 2026-09-13 - a syntactically valid (64 hex chars = the real
// 32-byte ObjectKey length) but never-created tombstone address, confirming the real
// "not claimed" response is a 404 with the indexer's standard {"error": "..."} shape,
// not a 200 with some empty/null substate value.
func TestGetSubstate_RealFixture_NotFound(t *testing.T) {
	client, _ := newFixtureServer(t, http.StatusNotFound, readFixture(t, "substate_tombstone_not_found.json"))

	_, err := client.GetSubstate(context.Background(), "tombstone_0000000000000000000000000000000000000000000000000000000000000000")
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	var statusErr *StatusError
	if !errors.As(err, &statusErr) {
		t.Fatalf("errors.As(%v, &StatusError{}) = false, want true", err)
	}
	if statusErr.StatusCode != http.StatusNotFound {
		t.Errorf("StatusCode = %d, want 404", statusErr.StatusCode)
	}
	if !strings.Contains(statusErr.Body, "not found") {
		t.Errorf("Body = %q, want it to contain the real error message", statusErr.Body)
	}
}

// TestGetSubstate_DecodesClaimedOutputTombstone is a synthetic (NOT live-captured)
// fixture covering the ClaimedOutputTombstone variant itself - no real claimed burn
// was found to exist on the live network during this dispatch (see the dispatch
// report), so this exercises the branch using the real Rust shape confirmed by
// reading crates/engine_types/src/confidential/unclaimed.rs directly (a single
// `value: u64` field) and crates/engine_types/src/substate.rs's SubstateValue enum
// (externally tagged, variant name "ClaimedOutputTombstone").
func TestGetSubstate_DecodesClaimedOutputTombstone(t *testing.T) {
	synthetic := `{"version":0,"substate":{"ClaimedOutputTombstone":{"value":1000000}},"verified":true}`
	client, _ := newFixtureServer(t, http.StatusOK, []byte(synthetic))

	resp, err := client.GetSubstate(context.Background(), "tombstone_aa")
	if err != nil {
		t.Fatalf("GetSubstate() error = %v", err)
	}
	if resp.Substate.ClaimedOutputTombstone == nil {
		t.Fatal("ClaimedOutputTombstone = nil, want non-nil")
	}
	if resp.Substate.ClaimedOutputTombstone.Value != 1000000 {
		t.Errorf("Value = %d, want 1000000", resp.Substate.ClaimedOutputTombstone.Value)
	}
}

// TestGetSubstate_URLEscapesPathSegment confirms the substate id is sent as a single
// URL-escaped path segment.
func TestGetSubstate_URLEscapesPathSegment(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(readFixture(t, "substate_resource_found.json"))
	}))
	defer srv.Close()
	client := NewWithHTTPClient(srv.URL, srv.Client())

	if _, err := client.GetSubstate(context.Background(), "resource_0101010101010101010101010101010101010101010101010101010101010101"); err != nil {
		t.Fatalf("GetSubstate() error = %v", err)
	}
	if gotPath != "/substates/resource_0101010101010101010101010101010101010101010101010101010101010101" {
		t.Errorf("Path = %q", gotPath)
	}
}

// TestDoGet_NetworkError confirms an unreachable host surfaces as a *NetworkError.
func TestDoGet_NetworkError(t *testing.T) {
	// Port 1 is a real-but-almost-always-unbound low port, so this connects to
	// nothing rather than a stale but real listener - no live network dependency for
	// this specific assertion.
	client := New("http://127.0.0.1:1")

	_, err := client.GetInfo(context.Background())
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	var netErr *NetworkError
	if !errors.As(err, &netErr) {
		t.Fatalf("errors.As(%v, &NetworkError{}) = false, want true", err)
	}
}
