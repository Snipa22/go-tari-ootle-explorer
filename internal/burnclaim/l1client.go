package burnclaim

import (
	"context"
	"fmt"
	"io"
	"sync"

	"github.com/Snipa22/go-tari-grpc-lib/v3/tari_generated"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// GRPCClient talks to one or more L1 (Minotari base-node) GRPC endpoints
// (config.NetworkConfig.L1BaseNodeGRPCHosts), with the SAME multi-host,
// dial-per-host, advance-on-failure failover strategy as go-tari-explorer's own
// internal/nodeclient.Client
// (https://github.com/Snipa22/go-tari-explorer/blob/main/internal/nodeclient/nodeclient.go)
// - that package is this repo's own sibling and the pattern AGENTS.md/
// DISPATCH_BRIEF.md explicitly point at, but it lives in a different Go module this
// repo doesn't (and per AGENTS.md, shouldn't) depend on, so the same ~100 lines of
// failover plumbing is reimplemented here rather than imported.
//
// Per go-tari-grpc-lib's own nodeGRPC package doc comment (nodeGRPC/client.go): that
// package is intentionally a thin, single-connection, PACKAGE-LEVEL-GLOBAL wrapper -
// InitNodeGRPC(addr) dials into an unexported global, unsafe for polling multiple
// hosts concurrently. GRPCClient here does NOT call into nodeGRPC at all; instead, it
// dials its own *grpc.ClientConn per configured host and calls the generated
// tari_generated.NewBaseNodeClient(conn) directly - the exact same generated
// protobuf/gRPC stub nodeGRPC itself uses internally (confirmed by reading
// nodeGRPC/client.go and tari_generated/base_node_grpc.pb.go before writing this -
// see AGENTS.md's "check what's actually already available/wrapped in
// go-tari-grpc-lib before writing new GRPC call code" instruction). This keeps
// go-tari-grpc-lib as the single source of truth for the generated stubs while making
// multi-host polling safe, matching go-tari-explorer's own nodeclient rationale
// exactly.
//
// Only the two BaseNode RPCs internal/burnclaim actually needs are wrapped:
// GetTipInfo (current L1 chain tip, for stuck-detection's "how far behind is this
// burn" math) and GetBlockByHeight (the burn-scanner's block source). Live-verified
// against a real public L1 GRPC endpoint - see this package's doc comment and the
// dispatch report for the exact command/output.
type GRPCClient struct {
	mu      sync.Mutex
	hosts   []string
	conns   []*grpc.ClientConn // lazily dialed, index-aligned with hosts
	current int                // index into hosts/conns to try first on the next call
	opts    []grpc.DialOption  // extra dial options applied to every dial, appended after the default transport credentials
}

// NewGRPCClient constructs a GRPCClient for the given list of "host:port" L1
// base-node GRPC targets (config.NetworkConfig.L1BaseNodeGRPCHosts - never a
// hardcoded default beyond this package's local-dev fallback, per AGENTS.md's
// standing rule). Connections are dialed lazily (on first use per host), so
// constructing a GRPCClient never blocks or fails even if a host is currently
// unreachable.
//
// opts is an optional set of extra grpc.DialOption values appended to every dial,
// after the default insecure transport credentials - production callers reaching a
// TLS-terminated public endpoint (e.g. grpc.esmeralda.tari.com:443, fronted by
// Cloudflare - confirmed live during this dispatch, see the dispatch report) MUST
// pass grpc.WithTransportCredentials(credentials.NewTLS(...)) here to override the
// default, since a plaintext dial to a TLS-only host fails. Tests use this same seam
// to point a real GRPCClient at an in-process bufconn-backed fake server.
func NewGRPCClient(hosts []string, opts ...grpc.DialOption) (*GRPCClient, error) {
	if len(hosts) == 0 {
		return nil, fmt.Errorf("burnclaim: at least one L1 base-node GRPC host is required")
	}
	return &GRPCClient{
		hosts: hosts,
		conns: make([]*grpc.ClientConn, len(hosts)),
		opts:  opts,
	}, nil
}

// Close tears down every dialed connection. Safe to call even if some hosts were
// never dialed.
func (c *GRPCClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	var firstErr error
	for i, conn := range c.conns {
		if conn == nil {
			continue
		}
		if err := conn.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		c.conns[i] = nil
	}
	return firstErr
}

func (c *GRPCClient) connAt(i int) (*grpc.ClientConn, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conns[i] != nil {
		return c.conns[i], nil
	}
	dialOpts := append([]grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}, c.opts...)
	conn, err := grpc.NewClient(c.hosts[i], dialOpts...)
	if err != nil {
		return nil, err
	}
	c.conns[i] = conn
	return conn, nil
}

func (c *GRPCClient) nextIndex(from int) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.current = (from + 1) % len(c.hosts)
	return c.current
}

func (c *GRPCClient) startIndex() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.current
}

// withFailover runs fn against each configured host in turn, starting from the
// current failover cursor and wrapping around, until one succeeds or every host has
// been tried - identical strategy to go-tari-explorer/internal/nodeclient's own
// withFailover (see this file's package doc comment for why it's reimplemented here
// rather than imported).
func withFailover[T any](c *GRPCClient, ctx context.Context, fn func(ctx context.Context, client tari_generated.BaseNodeClient) (T, error)) (T, error) {
	var zero T
	start := c.startIndex()
	var errs []error
	for attempt := 0; attempt < len(c.hosts); attempt++ {
		idx := (start + attempt) % len(c.hosts)
		conn, err := c.connAt(idx)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: dial: %w", c.hosts[idx], err))
			c.nextIndex(idx)
			continue
		}
		result, err := fn(ctx, tari_generated.NewBaseNodeClient(conn))
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", c.hosts[idx], err))
			c.nextIndex(idx)
			continue
		}
		c.mu.Lock()
		c.current = idx
		c.mu.Unlock()
		return result, nil
	}
	return zero, fmt.Errorf("burnclaim: all %d L1 host(s) failed: %w", len(c.hosts), joinErrs(errs))
}

func joinErrs(errs []error) error {
	if len(errs) == 0 {
		return nil
	}
	msg := errs[0].Error()
	for _, e := range errs[1:] {
		msg += "; " + e.Error()
	}
	return fmt.Errorf("%s", msg)
}

// GetTipInfo wraps the GetTipInfo GRPC call with failover across configured hosts -
// used by stuck-detection to learn the current L1 chain tip height.
func (c *GRPCClient) GetTipInfo(ctx context.Context) (*tari_generated.TipInfoResponse, error) {
	return withFailover(c, ctx, func(ctx context.Context, client tari_generated.BaseNodeClient) (*tari_generated.TipInfoResponse, error) {
		return client.GetTipInfo(ctx, &tari_generated.Empty{})
	})
}

// GetBlockByHeight retrieves blocks for the given heights, draining the streaming
// response into a slice, with failover across configured hosts - the burn-scanner's
// sole block source.
func (c *GRPCClient) GetBlockByHeight(ctx context.Context, heights []uint64) ([]*tari_generated.Block, error) {
	return withFailover(c, ctx, func(ctx context.Context, client tari_generated.BaseNodeClient) ([]*tari_generated.Block, error) {
		stream, err := client.GetBlocks(ctx, &tari_generated.GetBlocksRequest{Heights: heights}, grpc.MaxCallRecvMsgSize(32*1024*1024))
		if err != nil {
			return nil, err
		}
		resp := make([]*tari_generated.Block, 0, len(heights))
		for {
			blockResp, err := stream.Recv()
			if err != nil {
				if err == io.EOF {
					return resp, nil
				}
				return nil, err
			}
			resp = append(resp, blockResp.GetBlock())
		}
	})
}
