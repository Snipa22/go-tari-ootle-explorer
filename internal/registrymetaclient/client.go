// Package registrymetaclient is a Go HTTP client for the community
// template-metadata server's read API - the service tari-cli's `tari metadata
// publish` writes to (via its own POST /api/templates/<addr>/metadata[/signed]
// routes - NOT relevant here, see below) and this repo's internal/registry mirrors
// FROM, read-only.
//
// # Real, live-confirmed server shape (this session)
//
// Per DISPATCH_BRIEF.md, confirmed live and reachable this session against
// esmeralda's instance, https://ootle-templates-esme.tari.com:
//
//   - The configured base URL already includes a "/community-templates" path
//     prefix (per tari-cli's own DEFAULT_METADATA_SERVER_URL_ESMERALDA config
//     constant, crates/cli/src/project/config.rs) - this client appends only the
//     "/api/templates/..." route suffix on top of exactly the configured base URL,
//     never assumes or re-adds that prefix itself. See internal/config's
//     NetworkConfig.MetadataServerURL doc comment for the same point applied to this
//     repo's own config shape.
//   - GET /api/templates/featured is the one confirmed-working read/list route (see
//     ListFeatured below) - captured live:
//     `curl -sS https://ootle-templates-esme.tari.com/community-templates/api/templates/featured`
//     -> a JSON array of FeaturedTemplate (see types.go), esmeralda, checked
//     2026-09-13.
//   - There is NO plain unfiltered GET /api/templates list/paginated route: hitting
//     it (or e.g. /api/templates/list) live returns HTTP 200 with the SPA's own
//     index.html shell, not JSON - confirmed this session. This is a genuine,
//     confirmed gap (not a guess) - see this package's doc comment on DecodeError
//     for how that shows up to a caller (a *DecodeError, not a clean 404), and
//     DISPATCH_BRIEF.md's explicit instruction to build v1 against featured only
//     and flag this gap rather than inventing a list route.
//   - A genuine PER-ADDRESS route DOES exist and IS confirmed real, distinct from the
//     SPA catch-all above: GET /api/templates/{template_address} returns the same
//     FeaturedTemplate shape (200 JSON) for a real address, and a real
//     {"error":"Template not found"} 404 (not the SPA shell) for a well-formed but
//     unknown address - confirmed live both ways this session. This is NOT a
//     list/paginated route (DISPATCH_BRIEF.md's specific ask - "a genuine additional
//     list/paginated route"), so v1's ListFeatured (below) doesn't need it and this
//     package deliberately doesn't implement a wrapper for it yet; flagged here as a
//     real, confirmed finding for a later dispatch that wants per-template
//     enrichment (e.g. fetching the populated `definition` field for a specific
//     template a user is viewing - definition was observed null on every /featured
//     entry but non-null via this per-address route for the same template).
//   - The POST /api/templates/<addr>/metadata[/signed] write endpoints (confirmed via
//     tari-cli's build_metadata_url, crates/cli/src/cli/commands/metadata/publish.rs)
//     are publish-side only - internal/registry only reads/mirrors this server, so
//     this client doesn't implement them at all.
package registrymetaclient

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// DefaultTimeout bounds a single request's total round-trip time - internal/registry's
// polling loop must not hang forever against a dead or hung metadata server.
const DefaultTimeout = 15 * time.Second

// Client is an HTTP client for a single community template-metadata server instance's
// base URL (already including its "/community-templates" path prefix - see this
// package's doc comment). Construct with New; safe for concurrent use (http.Client
// is), holds no other mutable state.
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// New constructs a Client against baseURL (e.g. a network's config-driven
// NetworkConfig.MetadataServerURL - see internal/config - never a hardcoded default,
// per AGENTS.md's standing rule against hardcoded infra assumptions). Uses an
// http.Client with DefaultTimeout; use NewWithHTTPClient to override (e.g. from
// tests, or a caller wanting a different timeout/transport).
func New(baseURL string) *Client {
	return NewWithHTTPClient(baseURL, &http.Client{Timeout: DefaultTimeout})
}

// NewWithHTTPClient is like New but with a caller-supplied *http.Client - used by
// this package's own tests (httptest.Server + the server's default client) and
// available to callers wanting non-default transport/timeout behavior.
func NewWithHTTPClient(baseURL string, httpClient *http.Client) *Client {
	return &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		httpClient: httpClient,
	}
}

// ListFeatured fetches GET /api/templates/featured - the one confirmed-working
// read/list route this server exposes (see this package's doc comment for the "no
// unfiltered list route" gap this deliberately does NOT paper over).
func (c *Client) ListFeatured(ctx context.Context) ([]FeaturedTemplate, error) {
	const op = "ListFeatured"
	var out []FeaturedTemplate
	if err := c.doGet(ctx, op, "/api/templates/featured", &out); err != nil {
		return nil, err
	}
	return out, nil
}

// doGet performs a single GET request against baseURL+path, decoding a 2xx JSON body
// into out. Every failure mode is wrapped in one of this package's typed errors (see
// errors.go) before being further wrapped with the op name via fmt.Errorf's %w, so
// callers can errors.As() through to the typed error regardless of which exported
// method they called.
func (c *Client) doGet(ctx context.Context, op, path string, out interface{}) error {
	reqURL := c.baseURL + path

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return fmt.Errorf("registrymetaclient: %s: %w", op, &NetworkError{Op: op, Err: err})
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("registrymetaclient: %s: %w", op, &NetworkError{Op: op, Err: err})
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("registrymetaclient: %s: %w", op, &NetworkError{Op: op, Err: err})
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("registrymetaclient: %s: %w", op, &StatusError{
			Op:         op,
			StatusCode: resp.StatusCode,
			Body:       strings.TrimSpace(string(body)),
		})
	}

	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("registrymetaclient: %s: %w", op, &DecodeError{Op: op, Err: err})
	}
	return nil
}
