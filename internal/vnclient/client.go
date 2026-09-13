// Package vnclient is a Go JSON-RPC client for the subset of tari_validator_node's
// JSON-RPC API (POST /json_rpc, JSON-RPC 2.0 envelope - see AGENTS.md's "Primary
// source" section) that v1's internal/vnhealth needs:
//   - get_identity              -> GetIdentity
//   - get_committee             -> GetCommittee
//   - get_all_vns               -> GetAllVns
//   - get_consensus_status      -> GetConsensusStatus
//   - get_epoch_manager_stats   -> GetEpochManagerStats
//   - get_comms_stats           -> GetCommsStats
//   - get_connections           -> GetConnections
//
// Every method name and request/response shape here was confirmed against a fresh
// `git clone --depth 1 https://github.com/tari-project/tari-ootle.git`, commit
// d89dc92 (2026-09-11) - see types.go's package-level doc comment for the exact files
// read and the field-shape gotchas found along the way. Deliberately NOT covered:
// everything else server.rs's method-dispatch match statement lists (submit_transaction,
// get_transaction*, get_state, get_substate, list_blocks/get_block(s), get_tx_pool,
// get_template, get_shard_key, get_network_committees, add_peer,
// prepare_layer_one_transaction) - none of v1's health-polling scope needs them.
//
// IMPORTANT, per DISPATCH_BRIEF.md: no live tari_validator_node JSON-RPC endpoint was
// reachable in this environment to verify any of this against a real running node -
// this package is verified against the real Rust SOURCE (see above), with thorough
// mock-based tests (httptest.Server + hand-built-but-source-accurate fixtures - see
// client_test.go and testdata/), but has NOT been live-tested end to end. Say so
// explicitly wherever this package's correctness is discussed.
//
// Every method takes a context.Context and returns one of four typed errors -
// *NetworkError, *StatusError, *RPCError, or *DecodeError (see errors.go) - wrapped
// with fmt.Errorf's %w so callers can still errors.As() through the method's own
// wrapping, mirroring internal/indexerclient's error-handling convention for this
// repo's other upstream-Tari-service client.
package vnclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

// DefaultTimeout bounds a single request's total round-trip time - internal/vnhealth's
// polling loop must not hang forever against a dead or hung validator node.
const DefaultTimeout = 15 * time.Second

// jsonrpcVersion is the fixed "jsonrpc" envelope field value - axum_jrpc's
// JsonRpcRequest/JsonRpcResponse (de)serializers both hard-require this exact string
// (see docs.rs/axum-jrpc/0.8.0/src/axum_jrpc/lib.rs.html's Deserialize impls, which
// error on anything else via "Unknown jsonrpc version").
const jsonrpcVersion = "2.0"

// Client is a JSON-RPC client for a single tari_validator_node instance's JSON-RPC URL
// (e.g. a network's config-driven ValidatorJSONRPCURLs[i] - see internal/config -
// which already includes the "/json_rpc" path per that field's own convention; this
// client posts directly to whatever URL it's given, it does not append a path itself).
// Safe for concurrent use (http.Client is, and the request-id counter is atomic).
type Client struct {
	url        string
	httpClient *http.Client
	nextID     atomic.Int64
}

// New constructs a Client against url (the full JSON-RPC endpoint URL, e.g.
// "http://127.0.0.1:18200/json_rpc" - never a hardcoded default beyond this package's
// own DefaultTimeout, per AGENTS.md's standing rule against hardcoded infra
// assumptions). Uses an http.Client with DefaultTimeout; use NewWithHTTPClient to
// override.
func New(url string) *Client {
	return NewWithHTTPClient(url, &http.Client{Timeout: DefaultTimeout})
}

// NewWithHTTPClient is like New but with a caller-supplied *http.Client - used by this
// package's own tests (httptest.Server + the server's default client) and available to
// callers wanting non-default transport/timeout behavior.
func NewWithHTTPClient(url string, httpClient *http.Client) *Client {
	return &Client{url: url, httpClient: httpClient}
}

// jsonrpcRequest is the wire shape axum_jrpc's JsonRpcRequest deserializes
// (lib.rs.html): {"jsonrpc":"2.0","id":<num|string>,"method":<string>,"params":<any>}.
// params is always present (even as JSON null) - the real deserializer's Helper struct
// has no #[serde(default)] on it, so an OMITTED params field is a hard decode error on
// the server side, not "no params".
type jsonrpcRequest struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      int64       `json:"id"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params"`
}

// jsonrpcResponse is the wire shape axum_jrpc's JsonRpcResponse serializes: `result`
// XOR `error` at the top level (via JsonRpcAnswer's externally-tagged, #[serde(flatten)]'d
// enum - see lib.rs.html's Serialize impl), alongside jsonrpc/id. Both Result and Error
// are left as json.RawMessage here so this type can be decoded once regardless of
// which method was called / which branch came back; callers decode Result into their
// own method-specific struct afterward.
type jsonrpcResponse struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      json.RawMessage  `json:"id"`
	Result  json.RawMessage  `json:"result"`
	Error   *jsonrpcErrorObj `json:"error"`
}

// jsonrpcErrorObj is the wire shape of axum_jrpc::error::JsonRpcError
// (error.rs.html): {"code": i32, "message": string, "data": <any>}.
type jsonrpcErrorObj struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

// GetIdentity calls `get_identity` - the validator's own public key/node id (no
// request params).
func (c *Client) GetIdentity(ctx context.Context) (*GetIdentityResponse, error) {
	const op = "GetIdentity"
	var out GetIdentityResponse
	if err := c.call(ctx, op, "get_identity", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetCommittee calls `get_committee` - the committee covering substateAddress
// (hex-encoded, see GetCommitteeRequest's doc comment) as of epoch.
func (c *Client) GetCommittee(ctx context.Context, epoch uint64, substateAddress string) (*GetCommitteeResponse, error) {
	const op = "GetCommittee"
	var out GetCommitteeResponse
	params := GetCommitteeRequest{Epoch: epoch, SubstateAddress: substateAddress}
	if err := c.call(ctx, op, "get_committee", params, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetAllVns calls `get_all_vns` - the full validator-node roster as of epoch.
func (c *Client) GetAllVns(ctx context.Context, epoch uint64) (*GetAllVnsResponse, error) {
	const op = "GetAllVns"
	var out GetAllVnsResponse
	params := GetAllVnsRequest{Epoch: epoch}
	if err := c.call(ctx, op, "get_all_vns", params, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetConsensusStatus calls `get_consensus_status` - the validator's real-time
// consensus state (no request params).
func (c *Client) GetConsensusStatus(ctx context.Context) (*GetConsensusStatusResponse, error) {
	const op = "GetConsensusStatus"
	var out GetConsensusStatusResponse
	if err := c.call(ctx, op, "get_consensus_status", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetEpochManagerStats calls `get_epoch_manager_stats` - the epoch manager's internal
// state, including (per CommitteeInfo, when the validator is registered and past
// initial scanning) this validator's OWN current shard_group (no request params).
func (c *Client) GetEpochManagerStats(ctx context.Context) (*GetEpochManagerStatsResponse, error) {
	const op = "GetEpochManagerStats"
	var out GetEpochManagerStatsResponse
	if err := c.call(ctx, op, "get_epoch_manager_stats", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetCommsStats calls `get_comms_stats` - a coarse "Online"/"Offline" P2P liveness
// summary (no request params).
func (c *Client) GetCommsStats(ctx context.Context) (*GetCommsStatsResponse, error) {
	const op = "GetCommsStats"
	var out GetCommsStatsResponse
	if err := c.call(ctx, op, "get_comms_stats", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetConnections calls `get_connections` - the list of this validator's active P2P
// connections (no request params).
func (c *Client) GetConnections(ctx context.Context) (*GetConnectionsResponse, error) {
	const op = "GetConnections"
	var out GetConnectionsResponse
	if err := c.call(ctx, op, "get_connections", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// call performs a single JSON-RPC request for method with the given params (nil for a
// no-params method - always encoded as a literal JSON `null`, never an omitted field;
// see jsonrpcRequest's doc comment for why that distinction matters against the real
// server), decoding a successful `result` into out (skipped if out is nil). Every
// failure mode is wrapped in one of this package's typed errors (see errors.go) before
// being further wrapped with the op name via fmt.Errorf's %w.
func (c *Client) call(ctx context.Context, op, method string, params interface{}, out interface{}) error {
	reqID := c.nextID.Add(1)
	reqBody := jsonrpcRequest{
		JSONRPC: jsonrpcVersion,
		ID:      reqID,
		Method:  method,
		Params:  params,
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("vnclient: %s: %w", op, &NetworkError{Op: op, Err: err})
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(bodyBytes))
	if err != nil {
		return fmt.Errorf("vnclient: %s: %w", op, &NetworkError{Op: op, Err: err})
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("vnclient: %s: %w", op, &NetworkError{Op: op, Err: err})
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("vnclient: %s: %w", op, &NetworkError{Op: op, Err: err})
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("vnclient: %s: %w", op, &StatusError{
			Op:         op,
			StatusCode: resp.StatusCode,
			Body:       strings.TrimSpace(string(respBytes)),
		})
	}

	var envelope jsonrpcResponse
	if err := json.Unmarshal(respBytes, &envelope); err != nil {
		return fmt.Errorf("vnclient: %s: %w", op, &DecodeError{Op: op, Err: err})
	}

	if envelope.Error != nil {
		return fmt.Errorf("vnclient: %s: %w", op, &RPCError{
			Op:      op,
			Code:    envelope.Error.Code,
			Message: envelope.Error.Message,
			Data:    envelope.Error.Data,
		})
	}

	if out == nil || len(envelope.Result) == 0 {
		return nil
	}
	if err := json.Unmarshal(envelope.Result, out); err != nil {
		return fmt.Errorf("vnclient: %s: %w", op, &DecodeError{Op: op, Err: err})
	}
	return nil
}
