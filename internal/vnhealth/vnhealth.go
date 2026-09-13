// Package vnhealth polls internal/vnclient across every configured validator-node
// JSON-RPC endpoint and upserts the results into Postgres (via internal/db) - the
// validator-health-polling subsystem for v1's `validators` table, per AGENTS.md's
// build order.
//
// IMPORTANT, per DISPATCH_BRIEF.md: no live tari_validator_node JSON-RPC endpoint was
// reachable in this environment to test this package against a real running node -
// see internal/vnclient's package doc for the same callout on the client this package
// depends on. This package's own tests are entirely mock-based (a fake VNClient, no
// real network or JSON-RPC involved).
//
// Two modes, matching internal/explorer's own Backfill/Follow split (see that
// package's doc comment) even though this package only has one meaningful mode: a
// single Poll(ctx) covers everything, so Follow(ctx, interval) is just Poll on a
// timer - there's no separate "one-shot full walk vs incremental" distinction here
// the way there is for internal/explorer's paginated template catalogue.
//
// # How a single VN endpoint is polled (see pollOne)
//
// For each configured endpoint: get_identity first (the cheapest possible liveness
// check - confirmed against handlers.rs, it does a single already-cached
// get_local_peer_info() call, no I/O to consensus/epoch-manager state). If that
// fails, the endpoint is considered unreachable/unhealthy for this poll and skipped
// entirely - no partial row is written, since there is no public_key to key it on.
//
// If get_identity succeeds, every other method is polled best-effort: a failure from
// any of them is logged and skipped, never aborts the whole poll for that endpoint
// (independent, additive enrichment - not a required chain), per the merge semantics
// UpsertValidatorHealth (internal/db) provides.
//
// # Why get_committee is NOT part of this poll, despite being a real vnclient method
//
// DISPATCH_BRIEF.md's method list includes get_committee as something to poll for
// shard_group enrichment. Reading the real response shapes (see
// internal/vnclient/types.go's GetCommitteeResponse doc comment) shows this doesn't
// actually work the way the brief assumed:
//   - GetCommitteeResponse carries a flat committee MEMBER LIST with no shard_group
//     field anywhere on it (confirmed against both GetCommitteeResponse and
//     Committee<TAddr> in the real Rust source) - there is nothing in this response to
//     populate shard_group_start/end_inclusive FROM.
//   - The request itself (GetCommitteeRequest{epoch, substate_address}) needs a
//     substate_address the caller must already know - there's no "get my own
//     committee" variant. The real handler that WOULD answer that
//     ("this validator's own current shard_group") is get_epoch_manager_stats's
//     committee_info field instead (confirmed against handlers.rs's
//     get_epoch_manager_stats, which populates CommitteeInfo from
//     epoch_manager.get_local_committee_info - explicitly the LOCAL, i.e. this
//     validator's own, committee).
//
// So this package uses get_epoch_manager_stats for shard_group enrichment (see
// pollOne) and doesn't call get_committee at all - internal/vnclient still exposes it
// correctly (a later dispatch, e.g. internal/server showing an arbitrary committee's
// membership for a given substate address, is a legitimate use), it's just not part
// of THIS package's health-polling flow. Flagged here explicitly since it's a
// deliberate deviation from DISPATCH_BRIEF.md's literal method list, not an oversight.
//
// get_comms_stats/get_connections are polled for observability (logged) but have no
// corresponding validators-table column in v1's schema, so their results aren't
// persisted - see migrations/0001_init.up.sql's validators columns.
package vnhealth

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/Snipa22/go-tari-ootle-explorer/internal/db"
	"github.com/Snipa22/go-tari-ootle-explorer/internal/vnclient"
)

// VNClient is the subset of *vnclient.Client's methods VNHealth needs.
// *vnclient.Client satisfies this interface as-is; tests supply a fake.
type VNClient interface {
	GetIdentity(ctx context.Context) (*vnclient.GetIdentityResponse, error)
	GetConsensusStatus(ctx context.Context) (*vnclient.GetConsensusStatusResponse, error)
	GetEpochManagerStats(ctx context.Context) (*vnclient.GetEpochManagerStatsResponse, error)
	GetAllVns(ctx context.Context, epoch uint64) (*vnclient.GetAllVnsResponse, error)
	GetCommsStats(ctx context.Context) (*vnclient.GetCommsStatsResponse, error)
	GetConnections(ctx context.Context) (*vnclient.GetConnectionsResponse, error)
}

// Store is the subset of *db.DB's methods VNHealth needs to persist what it polls.
// *db.DB satisfies this interface as-is; tests supply a fake.
type Store interface {
	UpsertValidatorHealth(ctx context.Context, v db.ValidatorHealth) error
}

// Endpoint pairs a configured validator JSON-RPC URL (config.NetworkConfig's
// ValidatorJSONRPCURLs[i] - kept here purely for logging/error-attribution, never
// re-parsed or re-dialed by this package directly) with the VNClient constructed
// against it.
type Endpoint struct {
	URL    string
	Client VNClient
}

// VNHealth bundles the dependencies needed to poll every configured VN endpoint and
// persist the result. Construct with New.
type VNHealth struct {
	endpoints []Endpoint
	store     Store

	// warnedNoEndpoints ensures the "no VN endpoints configured" log line (see Poll)
	// is emitted once per process, not once per poll tick during Follow - a
	// zero-endpoint network is an expected steady state (see this package's doc
	// comment), not a transient condition worth re-announcing every interval.
	warnedNoEndpoints bool
}

// New constructs a VNHealth. endpoints may be empty - see Poll's doc comment for how
// that's handled.
func New(endpoints []Endpoint, store Store) *VNHealth {
	return &VNHealth{endpoints: endpoints, store: store}
}

// Poll runs a single polling pass across every configured endpoint. Each endpoint is
// independent: one endpoint's failure is logged and does not prevent the others from
// being polled. Returns a joined (errors.Join) error if any endpoint's poll failed,
// primarily so Follow's own per-tick logging has something concrete to report - the
// data for every OTHER, successfully-polled endpoint is still persisted regardless.
//
// If no VN endpoints are configured for the active network (endpoints is empty - a
// valid, expected state, e.g. this repo's own current environment which only has a
// public indexer reachable, no VN - see AGENTS.md/DISPATCH_BRIEF.md), Poll logs that
// fact ONCE and returns nil rather than erroring or crashing. The validators table can
// still be populated by internal/explorer from the indexer's own REST /validators
// route in that case - this package only ADDS richer, VN-JSON-RPC-sourced data on top
// when an endpoint IS configured.
func (h *VNHealth) Poll(ctx context.Context) error {
	if len(h.endpoints) == 0 {
		if !h.warnedNoEndpoints {
			log.Printf("vnhealth: no validator JSON-RPC endpoints configured for this network - skipping VN health polling (the validators table can still be populated by internal/explorer from the indexer's REST /validators route)")
			h.warnedNoEndpoints = true
		}
		return nil
	}

	var errs []error
	for _, ep := range h.endpoints {
		if err := h.pollOne(ctx, ep); err != nil {
			log.Printf("vnhealth: poll %s: %v", ep.URL, err)
			errs = append(errs, fmt.Errorf("%s: %w", ep.URL, err))
		}
	}
	return errors.Join(errs...)
}

// Follow repeatedly calls Poll every pollInterval until ctx is cancelled. A single
// failed Poll (i.e. errors.Join returned non-nil because at least one endpoint's poll
// failed) is already logged per-endpoint inside Poll itself - Follow doesn't log it
// again, it just keeps ticking, matching internal/explorer's own Follow convention of
// treating a failed poll as transient and retried on the next tick.
func (h *VNHealth) Follow(ctx context.Context, pollInterval time.Duration) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		_ = h.Poll(ctx)

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}

// pollOne runs a single endpoint's poll: get_identity first (cheapest liveness
// check - see this package's doc comment); if that fails, the endpoint is skipped
// entirely for this poll. Otherwise every other method is polled best-effort and
// merged into a single UpsertValidatorHealth call for this validator's own row, plus
// (when an epoch is known) a best-effort last_seen_epoch enrichment pass across the
// full roster from get_all_vns.
func (h *VNHealth) pollOne(ctx context.Context, ep Endpoint) error {
	identity, err := ep.Client.GetIdentity(ctx)
	if err != nil {
		return fmt.Errorf("get_identity: %w", err)
	}

	health := db.ValidatorHealth{PublicKey: identity.PublicKey}
	var knownEpoch *uint64

	if status, err := ep.Client.GetConsensusStatus(ctx); err != nil {
		log.Printf("vnhealth: %s: get_consensus_status: %v (continuing with partial data)", ep.URL, err)
	} else {
		epoch := status.Epoch
		state := status.State
		health.LastSeenEpoch = &epoch
		health.ConsensusStatus = &state
		knownEpoch = &epoch
	}

	if stats, err := ep.Client.GetEpochManagerStats(ctx); err != nil {
		log.Printf("vnhealth: %s: get_epoch_manager_stats: %v (continuing with partial data)", ep.URL, err)
	} else if stats.CommitteeInfo != nil {
		// See this package's doc comment for why CommitteeInfo (not get_committee)
		// is the real source for this validator's OWN shard_group.
		start := stats.CommitteeInfo.ShardGroup.Start
		end := stats.CommitteeInfo.ShardGroup.EndInclusive
		health.ShardGroupStart = &start
		health.ShardGroupEndInclusive = &end
		if knownEpoch == nil {
			epoch := stats.CommitteeInfo.Epoch
			health.LastSeenEpoch = &epoch
			knownEpoch = &epoch
		}
	}

	if err := h.store.UpsertValidatorHealth(ctx, health); err != nil {
		return fmt.Errorf("upsert validator health for %s: %w", identity.PublicKey, err)
	}

	// Best-effort: enrich the FULL roster's last_seen_epoch from get_all_vns, when an
	// epoch is known from one of the calls above. A failure here doesn't fail the
	// whole poll - this validator's own row (above) is the one piece of data this
	// endpoint is authoritative for; the rest of the roster is supplementary.
	if knownEpoch != nil {
		if vns, err := ep.Client.GetAllVns(ctx, *knownEpoch); err != nil {
			log.Printf("vnhealth: %s: get_all_vns: %v", ep.URL, err)
		} else {
			epoch := *knownEpoch
			for _, vn := range vns.Vns {
				if vn.PublicKey == identity.PublicKey {
					continue // already fully upserted above
				}
				if err := h.store.UpsertValidatorHealth(ctx, db.ValidatorHealth{
					PublicKey:     vn.PublicKey,
					LastSeenEpoch: &epoch,
				}); err != nil {
					log.Printf("vnhealth: %s: upsert roster entry %s: %v", ep.URL, vn.PublicKey, err)
				}
			}
		}
	}

	// get_comms_stats/get_connections: observability-only for v1 - see this
	// package's doc comment for why there's no validators-table column for either.
	if comms, err := ep.Client.GetCommsStats(ctx); err != nil {
		log.Printf("vnhealth: %s: get_comms_stats: %v", ep.URL, err)
	} else {
		log.Printf("vnhealth: %s: comms status = %s", ep.URL, comms.ConnectionStatus)
	}
	if conns, err := ep.Client.GetConnections(ctx); err != nil {
		log.Printf("vnhealth: %s: get_connections: %v", ep.URL, err)
	} else {
		log.Printf("vnhealth: %s: %d active connection(s)", ep.URL, len(conns.Connections))
	}

	return nil
}
