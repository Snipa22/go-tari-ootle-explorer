package indexerclient

import "fmt"

// This file defines the three distinct failure modes internal/explorer's polling loop
// needs to tell apart (per DISPATCH_BRIEF.md): a network-level failure (dial/timeout/
// connection reset - transient, worth retrying), the indexer answering with a non-2xx
// status (its own error, may or may not be transient depending on the code), and a
// response that came back 2xx but didn't parse as the expected shape (almost always a
// real bug - a stale Go struct against a changed real API shape - worth failing loud
// on rather than silently retrying forever).

// NetworkError indicates the HTTP request itself failed (DNS, dial, TLS, timeout,
// connection reset, context cancellation, etc.) - the indexer was never reached, or the
// response was never received. Typically worth retrying.
type NetworkError struct {
	Op  string
	Err error
}

func (e *NetworkError) Error() string {
	return fmt.Sprintf("indexerclient: %s: network error: %v", e.Op, e.Err)
}

func (e *NetworkError) Unwrap() error { return e.Err }

// StatusError indicates the indexer was reached and answered, but with a non-2xx HTTP
// status. Body is the raw response body (usually a small {"error": "..."} JSON object
// per the real indexer's ErrorResponse shape - see
// applications/tari_indexer/src/rest_api/error.rs - but captured as a raw string here
// since not every non-2xx response is guaranteed to be that shape, e.g. a
// reverse-proxy's own error page).
type StatusError struct {
	Op         string
	StatusCode int
	Body       string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("indexerclient: %s: indexer returned status %d: %s", e.Op, e.StatusCode, e.Body)
}

// DecodeError indicates the indexer answered 2xx but the response body didn't decode
// into the expected Go struct - almost always means this client's Go types have
// drifted from the real, fast-moving upstream API shape (see AGENTS.md's "Primary
// source" rule) rather than a transient condition worth retrying.
type DecodeError struct {
	Op  string
	Err error
}

func (e *DecodeError) Error() string {
	return fmt.Sprintf("indexerclient: %s: decode response: %v", e.Op, e.Err)
}

func (e *DecodeError) Unwrap() error { return e.Err }
