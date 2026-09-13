package registrymetaclient

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// readFixture returns the raw bytes of a real, live-captured response saved under
// testdata/ - see each test's originating curl command in its own comment. Fixtures
// are pasted, not invented, per DISPATCH_BRIEF.md's requirement.
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

// TestListFeatured_RealFixture was captured with:
//
//	curl -sS https://ootle-templates-esme.tari.com/community-templates/api/templates/featured
//
// against esmeralda on 2026-09-13. See testdata/featured.json for the raw body.
func TestListFeatured_RealFixture(t *testing.T) {
	client, _ := newFixtureServer(t, http.StatusOK, readFixture(t, "featured.json"))

	entries, err := client.ListFeatured(context.Background())
	if err != nil {
		t.Fatalf("ListFeatured() error = %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("len(entries) = %d, want 3", len(entries))
	}

	account := entries[0]
	if account.TemplateAddress != "0000000000000000000000000000000000000000000000000000000000000000" {
		t.Errorf("TemplateAddress = %q", account.TemplateAddress)
	}
	if account.TemplateName != "Account" {
		t.Errorf("TemplateName = %q, want Account", account.TemplateName)
	}
	if account.AuthorFriendlyName != nil {
		t.Errorf("AuthorFriendlyName = %v, want nil", account.AuthorFriendlyName)
	}
	if account.CodeSize != 510199 {
		t.Errorf("CodeSize = %d, want 510199", account.CodeSize)
	}
	if !account.IsFeatured {
		t.Errorf("IsFeatured = false, want true")
	}
	if account.MetadataHash != nil {
		t.Errorf("MetadataHash = %v, want nil", account.MetadataHash)
	}
	if string(account.Metadata) != "null" {
		t.Errorf("Metadata = %q, want the literal JSON null", string(account.Metadata))
	}
	if string(account.Definition) != "null" {
		t.Errorf("Definition = %q, want the literal JSON null", string(account.Definition))
	}

	// The second entry (TariStableCoin) is the one live example with a non-null
	// metadata blob - confirms this genuinely decodes as opaque JSON rather than
	// erroring or losing data, per this package's doc comment on why Metadata is
	// json.RawMessage rather than a guessed Go struct.
	stableCoin := entries[1]
	if stableCoin.TemplateName != "TariStableCoin" {
		t.Errorf("TemplateName = %q, want TariStableCoin", stableCoin.TemplateName)
	}
	if stableCoin.AtEpoch != 9013 {
		t.Errorf("AtEpoch = %d, want 9013", stableCoin.AtEpoch)
	}
	if len(stableCoin.Metadata) == 0 || string(stableCoin.Metadata) == "null" {
		t.Fatalf("Metadata = %q, want a real non-null JSON object", string(stableCoin.Metadata))
	}
	if !strings.Contains(string(stableCoin.Metadata), `"category":"token"`) {
		t.Errorf("Metadata = %q, want it to contain the real category field", string(stableCoin.Metadata))
	}
	if !strings.Contains(string(stableCoin.Metadata), `"tags":["token","fungible","defi","stablecoin"]`) {
		t.Errorf("Metadata = %q, want it to contain the real tags field", string(stableCoin.Metadata))
	}
}

// TestListFeatured_RequestsExactPath confirms the exact path requested (relative to
// the configured base URL, which already includes the "/community-templates"
// prefix per this package's doc comment) is "/api/templates/featured" - not, e.g., a
// duplicated or missing prefix.
func TestListFeatured_RequestsExactPath(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(readFixture(t, "featured.json"))
	}))
	defer srv.Close()
	// Simulate the real base URL shape: a "/community-templates" suffix already
	// baked into the configured base URL, exactly as tari-cli's own
	// DEFAULT_METADATA_SERVER_URL_ESMERALDA does.
	client := NewWithHTTPClient(srv.URL+"/community-templates", srv.Client())

	if _, err := client.ListFeatured(context.Background()); err != nil {
		t.Fatalf("ListFeatured() error = %v", err)
	}
	if gotPath != "/community-templates/api/templates/featured" {
		t.Errorf("Path = %q, want /community-templates/api/templates/featured", gotPath)
	}
}

// TestListFeatured_SPACatchAllShellSurfacesAsDecodeError confirms the real, live
// confirmed "wrong path returns the SPA's HTML shell with a 200 status" behavior
// (see this package's doc comment) surfaces as a *DecodeError, not a silently empty
// result or a panic - this is a genuine live-observed failure mode for this specific
// server, not a hypothetical one.
func TestListFeatured_SPACatchAllShellSurfacesAsDecodeError(t *testing.T) {
	const spaShell = `<!doctype html><html><head><title>Ootle Community Templates</title></head><body><div id="root"></div></body></html>`
	client, _ := newFixtureServer(t, http.StatusOK, []byte(spaShell))

	_, err := client.ListFeatured(context.Background())
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	var decodeErr *DecodeError
	if !errors.As(err, &decodeErr) {
		t.Fatalf("errors.As(%v, &DecodeError{}) = false, want true", err)
	}
}

// TestListFeatured_StatusError confirms a genuine non-2xx response (e.g. the real
// per-address route's confirmed 404 shape, {"error":"Template not found"}) surfaces
// as a *StatusError.
func TestListFeatured_StatusError(t *testing.T) {
	client, _ := newFixtureServer(t, http.StatusNotFound, []byte(`{"error":"Template not found"}`))

	_, err := client.ListFeatured(context.Background())
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
	if !strings.Contains(statusErr.Body, "Template not found") {
		t.Errorf("Body = %q, want it to contain the real error message", statusErr.Body)
	}
}

// TestListFeatured_NetworkError confirms an unreachable host surfaces as a
// *NetworkError.
func TestListFeatured_NetworkError(t *testing.T) {
	// Port 1 is a real-but-almost-always-unbound low port, so this connects to
	// nothing rather than a stale but real listener - no live network dependency
	// for this specific assertion.
	client := New("http://127.0.0.1:1")

	_, err := client.ListFeatured(context.Background())
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	var netErr *NetworkError
	if !errors.As(err, &netErr) {
		t.Fatalf("errors.As(%v, &NetworkError{}) = false, want true", err)
	}
}
