package registrymetaclient

import "fmt"

// This file defines the same three-way error split as internal/indexerclient/errors.go
// and internal/vnclient/errors.go, for consistency across this repo's three "talk to
// an upstream Tari-ecosystem HTTP service" clients:
//
//   - NetworkError: the HTTP request itself failed (dial/TLS/timeout/connection reset/
//     context cancellation) - the metadata server was never reached.
//   - StatusError: the metadata server was reached but answered with a non-2xx HTTP
//     status.
//   - DecodeError: the response came back 2xx but didn't decode into the expected Go
//     struct. Notably more likely to fire against THIS server than against
//     indexerclient/vnclient: this server's own frontend is a single-page app with a
//     catch-all route (confirmed live - see client.go's doc comment), so a wrong
//     path/typo doesn't reliably surface as a clean 404 the way tari_indexer's REST
//     API does; it can come back 200 with the SPA's HTML shell instead, which fails
//     JSON decoding and is correctly reported as a DecodeError, not silently treated
//     as an empty/successful result.

// NetworkError indicates the HTTP request itself failed - the metadata server was
// never reached, or no response was ever received.
type NetworkError struct {
	Op  string
	Err error
}

func (e *NetworkError) Error() string {
	return fmt.Sprintf("registrymetaclient: %s: network error: %v", e.Op, e.Err)
}

func (e *NetworkError) Unwrap() error { return e.Err }

// StatusError indicates the metadata server's HTTP endpoint was reached and answered,
// but with a non-2xx HTTP status.
type StatusError struct {
	Op         string
	StatusCode int
	Body       string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("registrymetaclient: %s: metadata server returned HTTP status %d: %s", e.Op, e.StatusCode, e.Body)
}

// DecodeError indicates the metadata server answered 2xx but the response body didn't
// decode into the expected Go struct - see this file's top comment for why this is a
// real, not merely theoretical, failure mode for this specific server.
type DecodeError struct {
	Op  string
	Err error
}

func (e *DecodeError) Error() string {
	return fmt.Sprintf("registrymetaclient: %s: decode response: %v", e.Op, e.Err)
}

func (e *DecodeError) Unwrap() error { return e.Err }
