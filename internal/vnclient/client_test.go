package vnclient

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// This file's tests are ALL mock-based (httptest.Server serving hand-built-but-
// source-accurate JSON-RPC response fixtures under testdata/) - per
// DISPATCH_BRIEF.md, no live tari_validator_node JSON-RPC endpoint was reachable in
// this environment, so unlike internal/indexerclient's client_test.go (which has
// several "_RealFixture" tests captured from a live public indexer), NONE of these
// fixtures were captured from a real running node. Every fixture's shape is derived
// from reading the real Rust request/response struct definitions and handler bodies
// directly (see types.go's package doc for the exact files and tari-ootle commit hash
// - d89dc92, 2026-09-11) - not guessed, but also not live-verified end to end.

// readFixture returns the raw bytes of a fixture file under testdata/.
func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return raw
}

// newFixtureServer returns an httptest.Server that serves fixture with the given
// status code for every request (regardless of the request body), plus a Client
// pointed at it. lastRequest, if non-nil, is populated with the raw request body of
// the most recent request, for assertions on what this package actually sent.
func newFixtureServer(t *testing.T, status int, fixture []byte, lastRequest *[]byte) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if lastRequest != nil {
			buf, _ := io.ReadAll(r.Body)
			*lastRequest = buf
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(fixture)
	}))
	t.Cleanup(srv.Close)
	return NewWithHTTPClient(srv.URL, srv.Client()), srv
}

// TestGetIdentity_Fixture models GetIdentityResponse
// (clients/validator_node_client/src/types.rs), tari-ootle commit d89dc92
// (2026-09-11).
func TestGetIdentity_Fixture(t *testing.T) {
	client, _ := newFixtureServer(t, http.StatusOK, readFixture(t, "get_identity_success.json"), nil)

	resp, err := client.GetIdentity(context.Background())
	if err != nil {
		t.Fatalf("GetIdentity() error = %v", err)
	}
	if resp.PublicKey != "10780fe1b4d31f8350e4b881dccd8f8210ce056050ac6dd8cca88ca16710b044" {
		t.Errorf("PublicKey = %q", resp.PublicKey)
	}
	if resp.PeerID != "12D3KooWBzNMTBpB8HkO1jQCSHUvKgTVs1YFcYb1gLahs4V7NFEr" {
		t.Errorf("PeerID = %q", resp.PeerID)
	}
	if len(resp.PublicAddresses) != 1 || resp.PublicAddresses[0] != "/ip4/127.0.0.1/tcp/18189" {
		t.Errorf("PublicAddresses = %v", resp.PublicAddresses)
	}
	if resp.ProtocolVersion != "0.40.2" {
		t.Errorf("ProtocolVersion = %q", resp.ProtocolVersion)
	}
	if resp.FeeClaimPublicKey != "3c6eebdcb2939ebad646a9d464874005aa979f1e575b72cd2a690289f10e5e50" {
		t.Errorf("FeeClaimPublicKey = %q", resp.FeeClaimPublicKey)
	}
}

// TestGetIdentity_SendsWellFormedRequestEnvelope confirms the request body matches
// axum_jrpc's real JsonRpcRequest deserialize shape (lib.rs.html): jsonrpc="2.0", a
// numeric id, the exact method name, and an explicit (never omitted) params field -
// see jsonrpcRequest's doc comment for why an omitted params field would be a decode
// error against the real server.
func TestGetIdentity_SendsWellFormedRequestEnvelope(t *testing.T) {
	var lastRequest []byte
	client, _ := newFixtureServer(t, http.StatusOK, readFixture(t, "get_identity_success.json"), &lastRequest)

	if _, err := client.GetIdentity(context.Background()); err != nil {
		t.Fatalf("GetIdentity() error = %v", err)
	}

	var req map[string]interface{}
	if err := json.Unmarshal(lastRequest, &req); err != nil {
		t.Fatalf("request body isn't valid JSON: %v (body=%s)", err, lastRequest)
	}
	if req["jsonrpc"] != "2.0" {
		t.Errorf(`jsonrpc = %v, want "2.0"`, req["jsonrpc"])
	}
	if req["method"] != "get_identity" {
		t.Errorf(`method = %v, want "get_identity"`, req["method"])
	}
	if _, ok := req["id"]; !ok {
		t.Errorf("request has no id field")
	}
	if v, ok := req["params"]; !ok {
		t.Errorf("request has no params field at all (real server requires it present, even as null)")
	} else if v != nil {
		t.Errorf("params = %v, want null for a no-arg method", v)
	}
}

// TestGetCommittee_Fixture models GetCommitteeResponse/CommitteeMember<PeerAddress>
// (clients/validator_node_client/src/types.rs + crates/common_types/src/committee.rs),
// same commit as above. Confirms CommitteeMember.address decodes as the base58 PeerId
// string this package's PeerId-serialization note documents, not raw bytes.
func TestGetCommittee_Fixture(t *testing.T) {
	client, _ := newFixtureServer(t, http.StatusOK, readFixture(t, "get_committee_success.json"), nil)

	resp, err := client.GetCommittee(context.Background(), 10990, "0000000000000000000000000000000000000000000000000000000000000100")
	if err != nil {
		t.Fatalf("GetCommittee() error = %v", err)
	}
	if len(resp.Committee.Members) != 2 {
		t.Fatalf("len(Members) = %d, want 2", len(resp.Committee.Members))
	}
	m := resp.Committee.Members[0]
	if m.Address != "12D3KooWBzNMTBpB8HkO1jQCSHUvKgTVs1YFcYb1gLahs4V7NFEr" {
		t.Errorf("Address = %q", m.Address)
	}
	if m.PublicKey != "10780fe1b4d31f8350e4b881dccd8f8210ce056050ac6dd8cca88ca16710b044" {
		t.Errorf("PublicKey = %q", m.PublicKey)
	}
	if m.VotePower != 1 {
		t.Errorf("VotePower = %d, want 1", m.VotePower)
	}
}

// TestGetCommittee_SendsRequestParams confirms epoch/substate_address are sent as the
// JSON-RPC params object, matching GetCommitteeRequest's field names exactly.
func TestGetCommittee_SendsRequestParams(t *testing.T) {
	var lastRequest []byte
	client, _ := newFixtureServer(t, http.StatusOK, readFixture(t, "get_committee_success.json"), &lastRequest)

	if _, err := client.GetCommittee(context.Background(), 10990, "00aa"); err != nil {
		t.Fatalf("GetCommittee() error = %v", err)
	}

	var req struct {
		Params GetCommitteeRequest `json:"params"`
	}
	if err := json.Unmarshal(lastRequest, &req); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}
	if req.Params.Epoch != 10990 || req.Params.SubstateAddress != "00aa" {
		t.Errorf("Params = %+v, want {10990 00aa}", req.Params)
	}
}

// TestGetAllVns_Fixture models GetAllVnsResponse/BaseLayerValidatorNode
// (clients/validator_node_client/src/types.rs + clients/base_node_client/src/types.rs),
// same commit.
func TestGetAllVns_Fixture(t *testing.T) {
	client, _ := newFixtureServer(t, http.StatusOK, readFixture(t, "get_all_vns_success.json"), nil)

	resp, err := client.GetAllVns(context.Background(), 10990)
	if err != nil {
		t.Fatalf("GetAllVns() error = %v", err)
	}
	if len(resp.Vns) != 2 {
		t.Fatalf("len(Vns) = %d, want 2", len(resp.Vns))
	}
	if resp.Vns[0].PublicKey != "10780fe1b4d31f8350e4b881dccd8f8210ce056050ac6dd8cca88ca16710b044" {
		t.Errorf("Vns[0].PublicKey = %q", resp.Vns[0].PublicKey)
	}
	if resp.Vns[0].ShardKey != "0000000000000000000000000000000000000000000000000000000000000100" {
		t.Errorf("Vns[0].ShardKey = %q", resp.Vns[0].ShardKey)
	}
	if resp.Vns[0].SidechainID != nil {
		t.Errorf("Vns[0].SidechainID = %v, want nil", resp.Vns[0].SidechainID)
	}
}

// TestGetConsensusStatus_Fixture models GetConsensusStatusResponse
// (clients/validator_node_client/src/types.rs), same commit. State is confirmed
// (handlers.rs's get_consensus_status) to be ConsensusCurrentState::to_string() -
// free-form text, matched here as the literal "Running" example.
func TestGetConsensusStatus_Fixture(t *testing.T) {
	client, _ := newFixtureServer(t, http.StatusOK, readFixture(t, "get_consensus_status_success.json"), nil)

	resp, err := client.GetConsensusStatus(context.Background())
	if err != nil {
		t.Fatalf("GetConsensusStatus() error = %v", err)
	}
	if resp.Epoch != 10990 {
		t.Errorf("Epoch = %d, want 10990", resp.Epoch)
	}
	if resp.Height != 1794 {
		t.Errorf("Height = %d, want 1794", resp.Height)
	}
	if resp.State != "Running" {
		t.Errorf("State = %q, want Running", resp.State)
	}
}

// TestGetEpochManagerStats_Fixture models GetEpochManagerStatsResponse/CommitteeInfo
// (clients/validator_node_client/src/types.rs + crates/common_types/src/committee.rs),
// same commit. Confirms CommitteeInfo.shard_group decodes correctly - this is the
// field internal/vnhealth uses to populate validators.shard_group_start/
// end_inclusive (see internal/vnhealth's doc comment for why, in preference to
// get_committee's response, which has no shard_group field at all).
func TestGetEpochManagerStats_Fixture(t *testing.T) {
	client, _ := newFixtureServer(t, http.StatusOK, readFixture(t, "get_epoch_manager_stats_success.json"), nil)

	resp, err := client.GetEpochManagerStats(context.Background())
	if err != nil {
		t.Fatalf("GetEpochManagerStats() error = %v", err)
	}
	if resp.CurrentEpoch != 10990 {
		t.Errorf("CurrentEpoch = %d, want 10990", resp.CurrentEpoch)
	}
	if resp.CurrentBlockHeight != 1794 {
		t.Errorf("CurrentBlockHeight = %d, want 1794", resp.CurrentBlockHeight)
	}
	if !resp.IsValid || !resp.IsInitialScanningComplete {
		t.Errorf("IsValid/IsInitialScanningComplete = %v/%v, want true/true", resp.IsValid, resp.IsInitialScanningComplete)
	}
	if resp.StartEpoch == nil || *resp.StartEpoch != 10517 {
		t.Errorf("StartEpoch = %v, want 10517", resp.StartEpoch)
	}
	if resp.CommitteeInfo == nil {
		t.Fatal("CommitteeInfo = nil, want non-nil")
	}
	if resp.CommitteeInfo.ShardGroup != (ShardGroup{Start: 1, EndInclusive: 256}) {
		t.Errorf("CommitteeInfo.ShardGroup = %+v, want {1 256}", resp.CommitteeInfo.ShardGroup)
	}
	if resp.CommitteeInfo.Epoch != 10990 {
		t.Errorf("CommitteeInfo.Epoch = %d, want 10990", resp.CommitteeInfo.Epoch)
	}
}

// TestGetEpochManagerStats_NilCommitteeInfoWhenNotRegistered confirms a null
// committee_info (the real handler's own behavior when the validator isn't
// registered yet, or the epoch isn't established - see handlers.rs's
// get_epoch_manager_stats, the `.optional()` + `Ok(None)` branch) decodes to a nil
// pointer, not a zero-value struct.
func TestGetEpochManagerStats_NilCommitteeInfoWhenNotRegistered(t *testing.T) {
	synthetic := `{"jsonrpc":"2.0","id":1,"result":{"current_epoch":0,"current_block_height":0,"current_block_hash":[0],"is_valid":false,"is_initial_scanning_complete":false,"start_epoch":null,"committee_info":null}}`
	client, _ := newFixtureServer(t, http.StatusOK, []byte(synthetic), nil)

	resp, err := client.GetEpochManagerStats(context.Background())
	if err != nil {
		t.Fatalf("GetEpochManagerStats() error = %v", err)
	}
	if resp.CommitteeInfo != nil {
		t.Errorf("CommitteeInfo = %+v, want nil", resp.CommitteeInfo)
	}
	if resp.StartEpoch != nil {
		t.Errorf("StartEpoch = %v, want nil", resp.StartEpoch)
	}
	if resp.IsValid {
		t.Errorf("IsValid = true, want false")
	}
}

// TestGetCommsStats_Fixture models GetCommsStatsResponse
// (clients/validator_node_client/src/types.rs), same commit. ConnectionStatus is
// confirmed (handlers.rs's get_comms_stats) to be the literal string "Online" or
// "Offline".
func TestGetCommsStats_Fixture(t *testing.T) {
	client, _ := newFixtureServer(t, http.StatusOK, readFixture(t, "get_comms_stats_success.json"), nil)

	resp, err := client.GetCommsStats(context.Background())
	if err != nil {
		t.Fatalf("GetCommsStats() error = %v", err)
	}
	if resp.ConnectionStatus != "Online" {
		t.Errorf("ConnectionStatus = %q, want Online", resp.ConnectionStatus)
	}
}

// TestGetConnections_Fixture models GetConnectionsResponse/Connection
// (clients/validator_node_client/src/types.rs), same commit.
func TestGetConnections_Fixture(t *testing.T) {
	client, _ := newFixtureServer(t, http.StatusOK, readFixture(t, "get_connections_success.json"), nil)

	resp, err := client.GetConnections(context.Background())
	if err != nil {
		t.Fatalf("GetConnections() error = %v", err)
	}
	if len(resp.Connections) != 2 {
		t.Fatalf("len(Connections) = %d, want 2", len(resp.Connections))
	}
	if resp.Connections[0].Direction != "Inbound" {
		t.Errorf("Connections[0].Direction = %q, want Inbound", resp.Connections[0].Direction)
	}
	if resp.Connections[1].Direction != "Outbound" {
		t.Errorf("Connections[1].Direction = %q, want Outbound", resp.Connections[1].Direction)
	}
	if resp.Connections[1].UserAgent != nil {
		t.Errorf("Connections[1].UserAgent = %v, want nil", resp.Connections[1].UserAgent)
	}
}

// TestCall_RPCError_MethodNotFound confirms a well-formed JSON-RPC {"error": {...}}
// envelope (the real shape axum_jrpc's JsonRpcError serializes to - error.rs.html)
// surfaces as a *RPCError with the real code/message, not a *DecodeError or generic
// error.
func TestCall_RPCError_MethodNotFound(t *testing.T) {
	client, _ := newFixtureServer(t, http.StatusOK, readFixture(t, "error_method_not_found.json"), nil)

	_, err := client.GetIdentity(context.Background())
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	var rpcErr *RPCError
	if !errors.As(err, &rpcErr) {
		t.Fatalf("errors.As(%v, &RPCError{}) = false, want true", err)
	}
	if rpcErr.Code != -32601 {
		t.Errorf("Code = %d, want -32601", rpcErr.Code)
	}
	if rpcErr.Message != "Method `get_bogus` not found" {
		t.Errorf("Message = %q", rpcErr.Message)
	}
}

// TestCall_StatusError confirms a non-2xx HTTP status (e.g. a reverse proxy rejecting
// the request before it ever reaches the real JSON-RPC handler - see StatusError's doc
// comment) surfaces as a *StatusError.
func TestCall_StatusError(t *testing.T) {
	client, _ := newFixtureServer(t, http.StatusServiceUnavailable, []byte(`{"error":"unavailable"}`), nil)

	_, err := client.GetIdentity(context.Background())
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	var statusErr *StatusError
	if !errors.As(err, &statusErr) {
		t.Fatalf("errors.As(%v, &StatusError{}) = false, want true", err)
	}
	if statusErr.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("StatusCode = %d, want 503", statusErr.StatusCode)
	}
}

// TestCall_DecodeError confirms a 2xx response with a body that isn't a valid
// JSON-RPC envelope at all surfaces as a *DecodeError.
func TestCall_DecodeError(t *testing.T) {
	client, _ := newFixtureServer(t, http.StatusOK, []byte("not json"), nil)

	_, err := client.GetIdentity(context.Background())
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	var decodeErr *DecodeError
	if !errors.As(err, &decodeErr) {
		t.Fatalf("errors.As(%v, &DecodeError{}) = false, want true", err)
	}
}

// TestCall_DecodeError_ResultDoesNotMatchExpectedShape confirms a 2xx response with a
// well-formed JSON-RPC envelope but a `result` that doesn't decode into the expected
// Go struct (e.g. this package's types having drifted from the real API) ALSO
// surfaces as a *DecodeError, distinct from an envelope-level parse failure.
func TestCall_DecodeError_ResultDoesNotMatchExpectedShape(t *testing.T) {
	synthetic := `{"jsonrpc":"2.0","id":1,"result":"unexpected string, not an object"}`
	client, _ := newFixtureServer(t, http.StatusOK, []byte(synthetic), nil)

	_, err := client.GetIdentity(context.Background())
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	var decodeErr *DecodeError
	if !errors.As(err, &decodeErr) {
		t.Fatalf("errors.As(%v, &DecodeError{}) = false, want true", err)
	}
}

// TestCall_NetworkError confirms an unreachable host surfaces as a *NetworkError.
func TestCall_NetworkError(t *testing.T) {
	// Port 1 is a real-but-almost-always-unbound low port, so this connects to
	// nothing rather than a stale but real listener - no live network dependency for
	// this specific assertion.
	client := New("http://127.0.0.1:1/json_rpc")

	_, err := client.GetIdentity(context.Background())
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	var netErr *NetworkError
	if !errors.As(err, &netErr) {
		t.Fatalf("errors.As(%v, &NetworkError{}) = false, want true", err)
	}
}

// TestCall_RequestIDsIncrementAcrossCalls confirms each call sends a distinct,
// incrementing numeric id, matching axum_jrpc's Id::Num variant shape.
func TestCall_RequestIDsIncrementAcrossCalls(t *testing.T) {
	var lastRequest []byte
	client, _ := newFixtureServer(t, http.StatusOK, readFixture(t, "get_identity_success.json"), &lastRequest)

	ids := make(map[float64]bool)
	for i := 0; i < 3; i++ {
		if _, err := client.GetIdentity(context.Background()); err != nil {
			t.Fatalf("GetIdentity() [%d] error = %v", i, err)
		}
		var req struct {
			ID float64 `json:"id"`
		}
		if err := json.Unmarshal(lastRequest, &req); err != nil {
			t.Fatalf("unmarshal request [%d]: %v", i, err)
		}
		if ids[req.ID] {
			t.Errorf("id %v reused across calls", req.ID)
		}
		ids[req.ID] = true
	}
	if len(ids) != 3 {
		t.Errorf("len(distinct ids) = %d, want 3", len(ids))
	}
}
