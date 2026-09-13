// Package indexerclient is a Go HTTP client for the subset of tari_indexer's REST API
// (see AGENTS.md's "Primary source" section) that v1's internal/explorer needs to
// populate the ootle_blocks/validators/template_registry tables:
//   - GET /info                       -> GetInfo
//   - GET /validators                 -> ListValidators
//   - GET /epoch-checkpoints/latest   -> GetLatestEpochCheckpoint
//   - GET /templates/catalogue        -> ListTemplateCatalogue
//
// Deliberately NOT covered here (later dispatches per AGENTS.md's build order):
// transaction/substate/UTXO/burn-claim routes (internal/burnclaim), and anything
// validator-JSON-RPC-shaped (internal/vnclient/internal/vnhealth talk to
// tari_validator_node directly, not the indexer).
//
// Every method takes a context.Context (for caller-driven cancellation/timeouts on top
// of this client's own request timeout) and returns one of three typed errors -
// *NetworkError, *StatusError, or *DecodeError (see errors.go) - wrapped with
// fmt.Errorf's %w so callers can still errors.As() through the method's own wrapping,
// letting internal/explorer's polling loop distinguish "retry" from "fail loud".
package indexerclient

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// DefaultTimeout bounds a single request's total round-trip time - the explorer's
// polling loop must not hang forever against a dead or hung indexer.
const DefaultTimeout = 15 * time.Second

// Client is a REST client for a single tari_indexer instance's base URL. Construct
// with New; safe for concurrent use (http.Client is), holds no other mutable state.
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// New constructs a Client against baseURL (e.g. a network's config-driven
// IndexerRESTBaseURL - see internal/config - never a hardcoded default beyond this
// package's own DefaultTimeout, per AGENTS.md's standing rule against hardcoded infra
// assumptions). Uses an http.Client with DefaultTimeout; use NewWithHTTPClient to
// override (e.g. from tests, or a caller wanting a different timeout/transport).
func New(baseURL string) *Client {
	return NewWithHTTPClient(baseURL, &http.Client{Timeout: DefaultTimeout})
}

// NewWithHTTPClient is like New but with a caller-supplied *http.Client - used by this
// package's own tests (httptest.Server + the server's default client) and available to
// callers wanting non-default transport/timeout behavior.
func NewWithHTTPClient(baseURL string, httpClient *http.Client) *Client {
	return &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		httpClient: httpClient,
	}
}

// GetInfo fetches GET /info - this indexer node's own local configuration (network,
// current epoch, version, etc). Useful for network-agnostic sanity checks (e.g.
// confirming the configured base URL actually points at the expected network) before
// trusting other responses.
func (c *Client) GetInfo(ctx context.Context) (*IndexerInfo, error) {
	const op = "GetInfo"
	var out IndexerInfo
	if err := c.doGet(ctx, op, "/info", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListValidators fetches GET /validators - the validator roster for an epoch. epoch
// nil means "the indexer's current epoch" (the real API's own default - it does not
// default to epoch 0 or similar, so this client doesn't either).
func (c *Client) ListValidators(ctx context.Context, epoch *uint64) (*ListValidatorsResponse, error) {
	const op = "ListValidators"
	q := url.Values{}
	if epoch != nil {
		q.Set("epoch", strconv.FormatUint(*epoch, 10))
	}
	var out ListValidatorsResponse
	if err := c.doGet(ctx, op, "/validators", q, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetLatestEpochCheckpoint fetches GET /epoch-checkpoints/latest and extracts the
// commit proof's header - see EpochCheckpointHeader's doc comment for the important
// caveat that this header has neither a block id nor a timestamp field in the real
// upstream response.
func (c *Client) GetLatestEpochCheckpoint(ctx context.Context) (*EpochCheckpoint, error) {
	const op = "GetLatestEpochCheckpoint"
	var env epochCheckpointEnvelope
	if err := c.doGet(ctx, op, "/epoch-checkpoints/latest", nil, &env); err != nil {
		return nil, err
	}
	return &EpochCheckpoint{Header: env.Checkpoint.Proof.V1.CommitProof.Header}, nil
}

// ListTemplateCatalogue fetches a page of GET /templates/catalogue.
//
// IMPORTANT: the real API paginates by CURSOR, not offset (confirmed against
// ListTemplateCatalogueRequest in clients/tari_indexer_client/src/types.rs and its
// handler - applications/tari_indexer/src/rest_api/handlers/templates.rs -
// DISPATCH_BRIEF.md's "limit=&offset=" snippet does not match the real query params
// and is not followed here). after is the template_address of the last entry from the
// previous page (empty string for the first page); the indexer returns entries
// "inserted after" that address. limit <= 0 lets the indexer apply its own default
// (20, per the real handler); the real handler caps limit at 100 and 400s above that -
// this client passes limit through unchanged and lets that validation surface as a
// *StatusError rather than silently clamping it itself.
func (c *Client) ListTemplateCatalogue(ctx context.Context, limit uint64, after string) (*ListTemplateCatalogueResponse, error) {
	const op = "ListTemplateCatalogue"
	q := url.Values{}
	if limit > 0 {
		q.Set("limit", strconv.FormatUint(limit, 10))
	}
	if after != "" {
		q.Set("after", after)
	}
	var out ListTemplateCatalogueResponse
	if err := c.doGet(ctx, op, "/templates/catalogue", q, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetSubstate fetches GET /substates/{substate_id} - added in this dispatch for
// internal/burnclaim's L2 claim-checker, which looks up a burn's
// ClaimedOutputTombstoneAddress substate (id "tombstone_" + hex(commitment) - see
// crates/template_lib_types/src/substates/claimed_output_tombstone.rs's
// ClaimedOutputTombstoneAddress::from_commitment) to decide whether that burn has
// been claimed on L2 yet.
//
// substateID is passed through verbatim as a path segment (URL-escaped) - callers
// build the real substate-id STRING themselves (e.g. via burnclaim's
// tombstoneSubstateID helper), this method doesn't know or care which substate kind
// it is.
//
// IMPORTANT: a substate that doesn't exist (e.g. a burn nobody has claimed yet) is a
// real 404 from the indexer (confirmed live:
// https://ootle-indexer-a.tari.com/substates/tombstone_<64 zero hex chars> ->
// {"error":"Substate tombstone_... not found"}, esmeralda, checked 2026-09-13) - NOT
// folded into a 200 response with some "not found" GetSubstateResponse variant. This
// method returns that as an ordinary *StatusError (via doGet, same as every other
// non-2xx response in this client) with StatusCode 404 - callers that need to treat
// "not found" as an expected, non-error outcome (like internal/burnclaim's claim
// checker) must errors.As() for *StatusError and check StatusCode themselves, rather
// than this method silently swallowing it into a nil/ok response.
func (c *Client) GetSubstate(ctx context.Context, substateID string) (*GetSubstateResponse, error) {
	const op = "GetSubstate"
	var out GetSubstateResponse
	if err := c.doGet(ctx, op, "/substates/"+url.PathEscape(substateID), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// doGet performs a single GET request against path (+ query, if non-empty), decoding a
// 2xx JSON body into out (skipped if out is nil). Every failure mode is wrapped in one
// of this package's typed errors (see errors.go) before being further wrapped with the
// op name via fmt.Errorf's %w, so callers can errors.As() through to the typed error
// regardless of which exported method they called.
func (c *Client) doGet(ctx context.Context, op, path string, query url.Values, out interface{}) error {
	reqURL := c.baseURL + path
	if len(query) > 0 {
		reqURL += "?" + query.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return fmt.Errorf("indexerclient: %s: %w", op, &NetworkError{Op: op, Err: err})
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("indexerclient: %s: %w", op, &NetworkError{Op: op, Err: err})
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("indexerclient: %s: %w", op, &NetworkError{Op: op, Err: err})
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("indexerclient: %s: %w", op, &StatusError{
			Op:         op,
			StatusCode: resp.StatusCode,
			Body:       strings.TrimSpace(string(body)),
		})
	}

	if out == nil {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("indexerclient: %s: %w", op, &DecodeError{Op: op, Err: err})
	}
	return nil
}
