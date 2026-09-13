package vnhealth

import (
	"bytes"
	"context"
	"errors"
	"log"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Snipa22/go-tari-ootle-explorer/internal/db"
	"github.com/Snipa22/go-tari-ootle-explorer/internal/vnclient"
)

// fakeVNClient is an in-memory VNClient double - no real network, no real JSON-RPC,
// consistent with DISPATCH_BRIEF.md's requirement that this package's tests are
// entirely mock-based (no live validator-node endpoint was available - see
// vnhealth.go's package doc).
type fakeVNClient struct {
	identity    *vnclient.GetIdentityResponse
	identityErr error

	consensusStatus    *vnclient.GetConsensusStatusResponse
	consensusStatusErr error

	epochManagerStats    *vnclient.GetEpochManagerStatsResponse
	epochManagerStatsErr error

	allVns    *vnclient.GetAllVnsResponse
	allVnsErr error

	commsStats    *vnclient.GetCommsStatsResponse
	commsStatsErr error

	connections    *vnclient.GetConnectionsResponse
	connectionsErr error

	// calls records how many times each method was invoked.
	getIdentityCalls          int
	getConsensusStatusCalls   int
	getEpochManagerStatsCalls int
	getAllVnsCalls            int
	getCommsStatsCalls        int
	getConnectionsCalls       int
	lastAllVnsEpoch           uint64
}

func (f *fakeVNClient) GetIdentity(ctx context.Context) (*vnclient.GetIdentityResponse, error) {
	f.getIdentityCalls++
	if f.identityErr != nil {
		return nil, f.identityErr
	}
	return f.identity, nil
}

func (f *fakeVNClient) GetConsensusStatus(ctx context.Context) (*vnclient.GetConsensusStatusResponse, error) {
	f.getConsensusStatusCalls++
	if f.consensusStatusErr != nil {
		return nil, f.consensusStatusErr
	}
	return f.consensusStatus, nil
}

func (f *fakeVNClient) GetEpochManagerStats(ctx context.Context) (*vnclient.GetEpochManagerStatsResponse, error) {
	f.getEpochManagerStatsCalls++
	if f.epochManagerStatsErr != nil {
		return nil, f.epochManagerStatsErr
	}
	return f.epochManagerStats, nil
}

func (f *fakeVNClient) GetAllVns(ctx context.Context, epoch uint64) (*vnclient.GetAllVnsResponse, error) {
	f.getAllVnsCalls++
	f.lastAllVnsEpoch = epoch
	if f.allVnsErr != nil {
		return nil, f.allVnsErr
	}
	return f.allVns, nil
}

func (f *fakeVNClient) GetCommsStats(ctx context.Context) (*vnclient.GetCommsStatsResponse, error) {
	f.getCommsStatsCalls++
	if f.commsStatsErr != nil {
		return nil, f.commsStatsErr
	}
	return f.commsStats, nil
}

func (f *fakeVNClient) GetConnections(ctx context.Context) (*vnclient.GetConnectionsResponse, error) {
	f.getConnectionsCalls++
	if f.connectionsErr != nil {
		return nil, f.connectionsErr
	}
	return f.connections, nil
}

// fakeStore is an in-memory Store double, recording every upsert for assertions.
type fakeStore struct {
	upserts          []db.ValidatorHealth
	upsertErrFor     map[string]error // public_key -> error to return for that upsert
	defaultUpsertErr error
}

func newFakeStore() *fakeStore {
	return &fakeStore{upsertErrFor: make(map[string]error)}
}

func (f *fakeStore) UpsertValidatorHealth(ctx context.Context, v db.ValidatorHealth) error {
	if err, ok := f.upsertErrFor[v.PublicKey]; ok {
		return err
	}
	if f.defaultUpsertErr != nil {
		return f.defaultUpsertErr
	}
	f.upserts = append(f.upserts, v)
	return nil
}

func (f *fakeStore) find(publicKey string) (db.ValidatorHealth, bool) {
	for _, u := range f.upserts {
		if u.PublicKey == publicKey {
			return u, true
		}
	}
	return db.ValidatorHealth{}, false
}

func sampleIdentity(pubKey string) *vnclient.GetIdentityResponse {
	return &vnclient.GetIdentityResponse{
		PeerID:    "peer-" + pubKey,
		PublicKey: pubKey,
	}
}

// TestPoll_ZeroEndpoints_NoOpNoError confirms a network with no configured VN
// endpoints (a valid, expected state - see AGENTS.md/DISPATCH_BRIEF.md) doesn't error
// or panic, and logs the "no VN endpoints configured" message exactly ONCE across
// multiple Poll calls, not once per call.
func TestPoll_ZeroEndpoints_NoOpNoError(t *testing.T) {
	var logBuf bytes.Buffer
	log.SetOutput(&logBuf)
	defer log.SetOutput(os.Stderr)

	store := newFakeStore()
	h := New(nil, store)

	for i := 0; i < 3; i++ {
		if err := h.Poll(context.Background()); err != nil {
			t.Fatalf("Poll() [%d] error = %v, want nil", i, err)
		}
	}

	if len(store.upserts) != 0 {
		t.Errorf("len(upserts) = %d, want 0", len(store.upserts))
	}
	logged := strings.Count(logBuf.String(), "no validator JSON-RPC endpoints configured")
	if logged != 1 {
		t.Errorf("logged the no-endpoints message %d times, want exactly 1", logged)
	}
}

// TestPoll_FullySuccessfulPoll_UpsertsSelfAndEnrichesRoster covers the "everything
// succeeds" path: get_identity + get_consensus_status + get_epoch_manager_stats (with
// a non-nil committee_info) + get_all_vns (roster including a second validator) all
// succeed. Confirms the polled validator's own row gets full data (epoch, shard
// group, consensus status) and the OTHER roster entry gets a last_seen_epoch-only
// enrichment row (no shard_group/consensus_status - vnhealth only has that for
// itself, not for other roster members - see pollOne's doc comment).
func TestPoll_FullySuccessfulPoll_UpsertsSelfAndEnrichesRoster(t *testing.T) {
	const selfKey = "self-pk"
	const otherKey = "other-pk"

	client := &fakeVNClient{
		identity: sampleIdentity(selfKey),
		consensusStatus: &vnclient.GetConsensusStatusResponse{
			Epoch:  10990,
			Height: 1794,
			State:  "Running",
		},
		epochManagerStats: &vnclient.GetEpochManagerStatsResponse{
			CurrentEpoch: 10990,
			CommitteeInfo: &vnclient.CommitteeInfo{
				ShardGroup: vnclient.ShardGroup{Start: 1, EndInclusive: 256},
				Epoch:      10990,
			},
		},
		allVns: &vnclient.GetAllVnsResponse{
			Vns: []vnclient.BaseLayerValidatorNode{
				{PublicKey: selfKey, ShardKey: "aa"},
				{PublicKey: otherKey, ShardKey: "bb"},
			},
		},
		commsStats:  &vnclient.GetCommsStatsResponse{ConnectionStatus: "Online"},
		connections: &vnclient.GetConnectionsResponse{},
	}
	store := newFakeStore()
	h := New([]Endpoint{{URL: "http://vn1/json_rpc", Client: client}}, store)

	if err := h.Poll(context.Background()); err != nil {
		t.Fatalf("Poll() error = %v", err)
	}

	self, ok := store.find(selfKey)
	if !ok {
		t.Fatalf("expected an upsert for %s", selfKey)
	}
	if self.LastSeenEpoch == nil || *self.LastSeenEpoch != 10990 {
		t.Errorf("self.LastSeenEpoch = %v, want 10990", self.LastSeenEpoch)
	}
	if self.ShardGroupStart == nil || *self.ShardGroupStart != 1 {
		t.Errorf("self.ShardGroupStart = %v, want 1", self.ShardGroupStart)
	}
	if self.ShardGroupEndInclusive == nil || *self.ShardGroupEndInclusive != 256 {
		t.Errorf("self.ShardGroupEndInclusive = %v, want 256", self.ShardGroupEndInclusive)
	}
	if self.ConsensusStatus == nil || *self.ConsensusStatus != "Running" {
		t.Errorf("self.ConsensusStatus = %v, want Running", self.ConsensusStatus)
	}

	other, ok := store.find(otherKey)
	if !ok {
		t.Fatalf("expected an enrichment upsert for %s (from get_all_vns)", otherKey)
	}
	if other.LastSeenEpoch == nil || *other.LastSeenEpoch != 10990 {
		t.Errorf("other.LastSeenEpoch = %v, want 10990", other.LastSeenEpoch)
	}
	if other.ShardGroupStart != nil || other.ShardGroupEndInclusive != nil || other.ConsensusStatus != nil {
		t.Errorf("other = %+v, want shard_group/consensus_status all nil (vnhealth doesn't have that data for non-self roster entries)", other)
	}

	if client.getAllVnsCalls != 1 {
		t.Errorf("getAllVnsCalls = %d, want 1", client.getAllVnsCalls)
	}
	if client.lastAllVnsEpoch != 10990 {
		t.Errorf("lastAllVnsEpoch = %d, want 10990", client.lastAllVnsEpoch)
	}
	if client.getCommsStatsCalls != 1 || client.getConnectionsCalls != 1 {
		t.Errorf("getCommsStatsCalls/getConnectionsCalls = %d/%d, want 1/1 (observability calls, always attempted)",
			client.getCommsStatsCalls, client.getConnectionsCalls)
	}
}

// TestPoll_GetIdentityFails_SkipsEndpointEntirely confirms a get_identity failure
// (the cheapest liveness check) skips this endpoint entirely for this poll - no
// upsert happens at all (there's no public_key to key one on), and the failure is
// surfaced via Poll's returned error.
func TestPoll_GetIdentityFails_SkipsEndpointEntirely(t *testing.T) {
	wantErr := errors.New("dial tcp: connection refused")
	client := &fakeVNClient{identityErr: wantErr}
	store := newFakeStore()
	h := New([]Endpoint{{URL: "http://vn1/json_rpc", Client: client}}, store)

	err := h.Poll(context.Background())
	if err == nil || !errors.Is(err, wantErr) {
		t.Fatalf("Poll() error = %v, want wrapping %v", err, wantErr)
	}
	if len(store.upserts) != 0 {
		t.Errorf("len(upserts) = %d, want 0 (no public_key to key an upsert on)", len(store.upserts))
	}
	if client.getConsensusStatusCalls != 0 {
		t.Errorf("getConsensusStatusCalls = %d, want 0 (should never be reached after get_identity fails)", client.getConsensusStatusCalls)
	}
}

// TestPoll_ConsensusStatusFails_StillEnrichesFromEpochManagerStats confirms a partial
// failure (get_consensus_status errors, but get_epoch_manager_stats still succeeds
// with a non-nil committee_info) still upserts shard_group AND falls back to
// committee_info's own epoch for last_seen_epoch, since consensus_status's epoch
// wasn't available this round - proving the two enrichment calls are independent, not
// a required chain.
func TestPoll_ConsensusStatusFails_StillEnrichesFromEpochManagerStats(t *testing.T) {
	const selfKey = "self-pk"
	client := &fakeVNClient{
		identity:           sampleIdentity(selfKey),
		consensusStatusErr: errors.New("get_consensus_status: internal error"),
		epochManagerStats: &vnclient.GetEpochManagerStatsResponse{
			CurrentEpoch: 10991,
			CommitteeInfo: &vnclient.CommitteeInfo{
				ShardGroup: vnclient.ShardGroup{Start: 257, EndInclusive: 512},
				Epoch:      10991,
			},
		},
		allVns:      &vnclient.GetAllVnsResponse{},
		commsStats:  &vnclient.GetCommsStatsResponse{ConnectionStatus: "Online"},
		connections: &vnclient.GetConnectionsResponse{},
	}
	store := newFakeStore()
	h := New([]Endpoint{{URL: "http://vn1/json_rpc", Client: client}}, store)

	if err := h.Poll(context.Background()); err != nil {
		t.Fatalf("Poll() error = %v, want nil (get_consensus_status failing shouldn't fail the whole poll)", err)
	}

	self, ok := store.find(selfKey)
	if !ok {
		t.Fatalf("expected an upsert for %s", selfKey)
	}
	if self.ConsensusStatus != nil {
		t.Errorf("self.ConsensusStatus = %v, want nil (get_consensus_status failed)", self.ConsensusStatus)
	}
	if self.LastSeenEpoch == nil || *self.LastSeenEpoch != 10991 {
		t.Errorf("self.LastSeenEpoch = %v, want 10991 (falls back to committee_info's epoch)", self.LastSeenEpoch)
	}
	if self.ShardGroupStart == nil || *self.ShardGroupStart != 257 {
		t.Errorf("self.ShardGroupStart = %v, want 257", self.ShardGroupStart)
	}
	if client.lastAllVnsEpoch != 10991 {
		t.Errorf("lastAllVnsEpoch = %d, want 10991 (fallback epoch used for get_all_vns too)", client.lastAllVnsEpoch)
	}
}

// TestPoll_NilCommitteeInfo_LeavesShardGroupUnset confirms a nil committee_info (the
// real handler's own behavior when the validator isn't registered/past initial
// scanning yet - see vnclient's doc comment) results in a ValidatorHealth with nil
// ShardGroupStart/EndInclusive, not zero values - preserving UpsertValidatorHealth's
// "nil means unknown, don't touch" merge semantics (internal/db) end to end.
func TestPoll_NilCommitteeInfo_LeavesShardGroupUnset(t *testing.T) {
	const selfKey = "self-pk"
	client := &fakeVNClient{
		identity: sampleIdentity(selfKey),
		consensusStatus: &vnclient.GetConsensusStatusResponse{
			Epoch: 5, Height: 1, State: "Initialising",
		},
		epochManagerStats: &vnclient.GetEpochManagerStatsResponse{
			CurrentEpoch:  5,
			CommitteeInfo: nil, // not yet registered
		},
		allVns:      &vnclient.GetAllVnsResponse{},
		commsStats:  &vnclient.GetCommsStatsResponse{ConnectionStatus: "Offline"},
		connections: &vnclient.GetConnectionsResponse{},
	}
	store := newFakeStore()
	h := New([]Endpoint{{URL: "http://vn1/json_rpc", Client: client}}, store)

	if err := h.Poll(context.Background()); err != nil {
		t.Fatalf("Poll() error = %v", err)
	}

	self, ok := store.find(selfKey)
	if !ok {
		t.Fatalf("expected an upsert for %s", selfKey)
	}
	if self.ShardGroupStart != nil || self.ShardGroupEndInclusive != nil {
		t.Errorf("self.ShardGroup = [%v,%v], want both nil", self.ShardGroupStart, self.ShardGroupEndInclusive)
	}
	if self.ConsensusStatus == nil || *self.ConsensusStatus != "Initialising" {
		t.Errorf("self.ConsensusStatus = %v, want Initialising", self.ConsensusStatus)
	}
}

// TestPoll_MultipleEndpoints_OneFailingDoesNotBlockOthers confirms independent
// endpoints are each polled regardless of another endpoint's failure - a single dead
// VN doesn't prevent the others from being health-checked.
func TestPoll_MultipleEndpoints_OneFailingDoesNotBlockOthers(t *testing.T) {
	failing := &fakeVNClient{identityErr: errors.New("boom")}
	healthy := &fakeVNClient{
		identity: sampleIdentity("healthy-pk"),
		consensusStatus: &vnclient.GetConsensusStatusResponse{
			Epoch: 1, Height: 1, State: "Running",
		},
		epochManagerStats: &vnclient.GetEpochManagerStatsResponse{CurrentEpoch: 1},
		allVns:            &vnclient.GetAllVnsResponse{},
		commsStats:        &vnclient.GetCommsStatsResponse{ConnectionStatus: "Online"},
		connections:       &vnclient.GetConnectionsResponse{},
	}
	store := newFakeStore()
	h := New([]Endpoint{
		{URL: "http://dead/json_rpc", Client: failing},
		{URL: "http://alive/json_rpc", Client: healthy},
	}, store)

	err := h.Poll(context.Background())
	if err == nil {
		t.Fatal("expected a non-nil joined error (one endpoint failed)")
	}

	if _, ok := store.find("healthy-pk"); !ok {
		t.Errorf("expected the healthy endpoint's validator to still be upserted despite the other endpoint failing")
	}
	if healthy.getIdentityCalls != 1 {
		t.Errorf("healthy.getIdentityCalls = %d, want 1", healthy.getIdentityCalls)
	}
}

// TestPoll_StoreErrorPropagates confirms a Store failure (e.g. a real DB error)
// surfaces via Poll's returned error rather than being silently swallowed.
func TestPoll_StoreErrorPropagates(t *testing.T) {
	wantErr := errors.New("db boom")
	client := &fakeVNClient{
		identity: sampleIdentity("pk"),
		consensusStatus: &vnclient.GetConsensusStatusResponse{
			Epoch: 1, Height: 1, State: "Running",
		},
		epochManagerStats: &vnclient.GetEpochManagerStatsResponse{},
		commsStats:        &vnclient.GetCommsStatsResponse{},
		connections:       &vnclient.GetConnectionsResponse{},
	}
	store := newFakeStore()
	store.defaultUpsertErr = wantErr
	h := New([]Endpoint{{URL: "http://vn1/json_rpc", Client: client}}, store)

	err := h.Poll(context.Background())
	if err == nil || !errors.Is(err, wantErr) {
		t.Fatalf("Poll() error = %v, want wrapping %v", err, wantErr)
	}
}

// TestFollow_PollsUntilContextCancelled confirms Follow ticks at least twice within a
// bounded window and then returns cleanly once ctx is cancelled - same shape as
// internal/explorer's TestFollow_PollsUntilContextCancelled.
func TestFollow_PollsUntilContextCancelled(t *testing.T) {
	client := &fakeVNClient{
		identity: sampleIdentity("pk"),
		consensusStatus: &vnclient.GetConsensusStatusResponse{
			Epoch: 1, Height: 1, State: "Running",
		},
		epochManagerStats: &vnclient.GetEpochManagerStatsResponse{},
		allVns:            &vnclient.GetAllVnsResponse{},
		commsStats:        &vnclient.GetCommsStatsResponse{},
		connections:       &vnclient.GetConnectionsResponse{},
	}
	store := newFakeStore()
	h := New([]Endpoint{{URL: "http://vn1/json_rpc", Client: client}}, store)

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()

	err := h.Follow(ctx, 5*time.Millisecond)
	if err == nil || ctx.Err() == nil {
		t.Fatalf("Follow() error = %v, ctx.Err() = %v, want a context-cancellation error", err, ctx.Err())
	}
	if client.getIdentityCalls < 2 {
		t.Errorf("getIdentityCalls = %d, want >= 2", client.getIdentityCalls)
	}
}
