package vnclient

import "fmt"

// This file defines the distinct failure modes internal/vnhealth's polling loop needs
// to tell apart - the same three-way split as internal/indexerclient/errors.go (for
// consistency across this repo's two "talk to an upstream Tari service" clients), plus
// one JSON-RPC-specific fourth mode a plain REST client doesn't have:
//
//   - NetworkError: the HTTP request itself failed (dial/TLS/timeout/connection reset/
//     context cancellation) - the validator node was never reached. Typically worth
//     retrying.
//   - StatusError: the validator node was reached but answered with a non-2xx HTTP
//     status. The real axum handler (server.rs) always answers 200 OK even for a
//     JSON-RPC-level error (see RPCError below) - a non-2xx response here means
//     something OUTSIDE the JSON-RPC handler itself objected (a reverse proxy, a
//     load balancer's health-check gate, TLS termination returning its own error
//     page, etc), not the validator's own JSON-RPC error path. Kept for defensive
//     symmetry with indexerclient, not because the real handler is known to ever
//     produce one.
//   - RPCError: the validator node answered 200 OK with a well-formed JSON-RPC 2.0
//     envelope, but that envelope's top-level shape was `{"error": {...}}` rather
//     than `{"result": ...}` - a JSON-RPC-level application error (bad params,
//     method not found, an internal error the handler itself caught and reported).
//     This is the JSON-RPC analogue of indexerclient's StatusError: the peer
//     responded, but with its own error rather than a usable result.
//   - DecodeError: the response's `result` value came back but didn't decode into the
//     expected Go struct for the method that was called - almost always this
//     package's Go types having drifted from the real upstream JSON-RPC shape (see
//     types.go's "Primary source" citations) rather than a transient condition worth
//     retrying.

// NetworkError indicates the HTTP request itself failed - the validator node was
// never reached, or no response was ever received.
type NetworkError struct {
	Op  string
	Err error
}

func (e *NetworkError) Error() string {
	return fmt.Sprintf("vnclient: %s: network error: %v", e.Op, e.Err)
}

func (e *NetworkError) Unwrap() error { return e.Err }

// StatusError indicates the validator node's HTTP endpoint was reached and answered,
// but with a non-2xx HTTP status - see this file's top comment for why that's notable
// (the real JSON-RPC handler itself always answers 200 OK).
type StatusError struct {
	Op         string
	StatusCode int
	Body       string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("vnclient: %s: validator node returned HTTP status %d: %s", e.Op, e.StatusCode, e.Body)
}

// RPCError indicates a well-formed JSON-RPC 2.0 error response - see
// axum_jrpc::error::JsonRpcError (docs.rs/axum-jrpc/0.8.0/src/axum_jrpc/error.rs.html,
// the exact JSON-RPC library applications/tari_validator_node depends on, confirmed
// against its Cargo.toml): {"code": i32, "message": string, "data": <any>}.
type RPCError struct {
	Op      string
	Code    int
	Message string
	Data    []byte // raw JSON, may be "null"
}

func (e *RPCError) Error() string {
	return fmt.Sprintf("vnclient: %s: validator node returned JSON-RPC error %d: %s", e.Op, e.Code, e.Message)
}

// DecodeError indicates the validator node answered with a usable `result`, but it
// didn't decode into the expected Go struct for the method that was called.
type DecodeError struct {
	Op  string
	Err error
}

func (e *DecodeError) Error() string {
	return fmt.Sprintf("vnclient: %s: decode response: %v", e.Op, e.Err)
}

func (e *DecodeError) Unwrap() error { return e.Err }
