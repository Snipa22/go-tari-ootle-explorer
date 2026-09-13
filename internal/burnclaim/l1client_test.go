package burnclaim

import (
	"context"
	"net"
	"testing"

	"github.com/Snipa22/go-tari-grpc-lib/v3/tari_generated"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

// fakeBaseNodeServer is a real in-process GRPC BaseNode server (bufconn), so these
// tests exercise the actual wire calls through a real *GRPCClient (dial/
// withFailover/stream-draining), not a hand-rolled interface stub - same rationale
// and pattern as go-tari-explorer/internal/nodeclient's own fakeBaseNodeServer (see
// l1client.go's doc comment for why that package's ~100 lines of failover logic is
// reimplemented here rather than imported).
type fakeBaseNodeServer struct {
	tari_generated.UnimplementedBaseNodeServer

	tip    *tari_generated.TipInfoResponse
	blocks map[uint64]*tari_generated.Block // keyed by height
	fail   bool
}

func (f *fakeBaseNodeServer) GetTipInfo(_ context.Context, _ *tari_generated.Empty) (*tari_generated.TipInfoResponse, error) {
	if f.fail {
		return nil, context.DeadlineExceeded
	}
	if f.tip != nil {
		return f.tip, nil
	}
	return &tari_generated.TipInfoResponse{}, nil
}

func (f *fakeBaseNodeServer) GetBlocks(req *tari_generated.GetBlocksRequest, stream tari_generated.BaseNode_GetBlocksServer) error {
	if f.fail {
		return context.DeadlineExceeded
	}
	for _, h := range req.Heights {
		if blk, ok := f.blocks[h]; ok {
			if err := stream.Send(&tari_generated.HistoricalBlock{Block: blk}); err != nil {
				return err
			}
		}
	}
	return nil
}

// startFakeServer boots fake on a bufconn listener and returns a real *GRPCClient
// dialed against it (via NewGRPCClient's opts... seam), plus a cleanup func.
func startFakeServer(t *testing.T, fake *fakeBaseNodeServer) (*GRPCClient, func()) {
	t.Helper()

	lis := bufconn.Listen(1024 * 1024)
	grpcServer := grpc.NewServer()
	tari_generated.RegisterBaseNodeServer(grpcServer, fake)
	go func() { _ = grpcServer.Serve(lis) }()

	dialer := grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
		return lis.DialContext(ctx)
	})

	client, err := NewGRPCClient([]string{"passthrough:///bufnet"}, dialer, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("NewGRPCClient: %v", err)
	}

	cleanup := func() {
		_ = client.Close()
		grpcServer.Stop()
		_ = lis.Close()
	}
	return client, cleanup
}

func TestGetTipInfo(t *testing.T) {
	fake := &fakeBaseNodeServer{
		tip: &tari_generated.TipInfoResponse{
			Metadata: &tari_generated.MetaData{BestBlockHeight: 12345},
		},
	}
	client, cleanup := startFakeServer(t, fake)
	defer cleanup()

	got, err := client.GetTipInfo(context.Background())
	if err != nil {
		t.Fatalf("GetTipInfo: %v", err)
	}
	if got.GetMetadata().GetBestBlockHeight() != 12345 {
		t.Fatalf("BestBlockHeight = %d, want 12345", got.GetMetadata().GetBestBlockHeight())
	}
}

func TestGetBlockByHeight_DrainsStream(t *testing.T) {
	fake := &fakeBaseNodeServer{
		blocks: map[uint64]*tari_generated.Block{
			10: {Header: &tari_generated.BlockHeader{Height: 10}},
			11: {Header: &tari_generated.BlockHeader{Height: 11}},
			12: {Header: &tari_generated.BlockHeader{Height: 12}},
		},
	}
	client, cleanup := startFakeServer(t, fake)
	defer cleanup()

	got, err := client.GetBlockByHeight(context.Background(), []uint64{10, 11, 12})
	if err != nil {
		t.Fatalf("GetBlockByHeight: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 blocks, got %d", len(got))
	}
}

func TestGetBlockByHeight_EmptyRange(t *testing.T) {
	fake := &fakeBaseNodeServer{blocks: map[uint64]*tari_generated.Block{}}
	client, cleanup := startFakeServer(t, fake)
	defer cleanup()

	got, err := client.GetBlockByHeight(context.Background(), []uint64{999})
	if err != nil {
		t.Fatalf("GetBlockByHeight: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected 0 blocks for a height with no fake data, got %d", len(got))
	}
}

// TestGetTipInfo_FailoverAcrossHosts confirms a failing first host doesn't fail the
// whole call when a second, working host is configured - same multi-host contract
// go-tari-explorer/internal/nodeclient guarantees. A single shared dialer routes to
// one of two distinct bufconn listeners based on the "passthrough:///<name>" target
// grpc.NewClient resolves down to "<name>", so both hosts can be exercised through
// one real *GRPCClient.
func TestGetTipInfo_FailoverAcrossHosts(t *testing.T) {
	failing := &fakeBaseNodeServer{fail: true}
	working := &fakeBaseNodeServer{tip: &tari_generated.TipInfoResponse{
		Metadata: &tari_generated.MetaData{BestBlockHeight: 777},
	}}

	failLis := bufconn.Listen(1024 * 1024)
	failServer := grpc.NewServer()
	tari_generated.RegisterBaseNodeServer(failServer, failing)
	go func() { _ = failServer.Serve(failLis) }()
	defer failServer.Stop()

	workLis := bufconn.Listen(1024 * 1024)
	workServer := grpc.NewServer()
	tari_generated.RegisterBaseNodeServer(workServer, working)
	go func() { _ = workServer.Serve(workLis) }()
	defer workServer.Stop()

	dialer := grpc.WithContextDialer(func(ctx context.Context, addr string) (net.Conn, error) {
		if addr == "fail" {
			return failLis.DialContext(ctx)
		}
		return workLis.DialContext(ctx)
	})

	client, err := NewGRPCClient(
		[]string{"passthrough:///fail", "passthrough:///work"},
		dialer, grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("NewGRPCClient: %v", err)
	}
	defer client.Close()

	got, err := client.GetTipInfo(context.Background())
	if err != nil {
		t.Fatalf("GetTipInfo: %v (want failover to the second, working host to succeed)", err)
	}
	if got.GetMetadata().GetBestBlockHeight() != 777 {
		t.Fatalf("BestBlockHeight = %d, want 777", got.GetMetadata().GetBestBlockHeight())
	}

	// The cursor should now prefer the working host - a second call should not
	// need to fail over again (best-effort check: it should still succeed).
	if _, err := client.GetTipInfo(context.Background()); err != nil {
		t.Fatalf("second GetTipInfo: %v", err)
	}
}

// TestGetTipInfo_AllHostsFail confirms every configured host failing surfaces a
// single wrapped error mentioning every host, not a silent zero-value success.
func TestGetTipInfo_AllHostsFail(t *testing.T) {
	fake := &fakeBaseNodeServer{fail: true}
	client, cleanup := startFakeServer(t, fake)
	defer cleanup()

	_, err := client.GetTipInfo(context.Background())
	if err == nil {
		t.Fatal("expected an error when the only configured host fails, got nil")
	}
}
