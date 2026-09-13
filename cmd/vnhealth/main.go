// Command vnhealth polls tari_validator_node's JSON-RPC API across every configured
// validator endpoint and persists the result into Postgres. Supports both a one-shot
// poll and a "keep polling" follow mode - see internal/vnhealth's package doc for the
// Poll/Follow split.
//
// IMPORTANT, per DISPATCH_BRIEF.md: no live tari_validator_node JSON-RPC endpoint was
// available to run this against in this environment - see internal/vnclient and
// internal/vnhealth's own package docs for the same callout.
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Snipa22/go-tari-ootle-explorer/internal/config"
	"github.com/Snipa22/go-tari-ootle-explorer/internal/db"
	"github.com/Snipa22/go-tari-ootle-explorer/internal/vnclient"
	"github.com/Snipa22/go-tari-ootle-explorer/internal/vnhealth"
)

func main() {
	configFile := flag.String("config", "", "Path to a TOML config file (env: TARI_OOTLE_EXPLORER_CONFIG_FILE)")
	network := flag.String("network", "", "Network name to run against, e.g. esmeralda (env: TARI_OOTLE_EXPLORER_NETWORK; default: localnet)")
	postgresDSN := flag.String("postgres-dsn", "", "Postgres connection string (env: TARI_OOTLE_EXPLORER_POSTGRES_DSN)")
	validatorURLs := flag.String("validator-jsonrpc-urls", "", "Comma-separated tari_validator_node JSON-RPC URL(s) for the active network (env: TARI_OOTLE_EXPLORER_VALIDATOR_JSONRPC_URLS) - may be empty, see internal/vnhealth's doc comment on the zero-endpoint case")
	mode := flag.String("mode", "poll", `Mode: "poll" (one-shot pass across every configured VN endpoint) or "follow" (keeps polling)`)
	pollInterval := flag.Duration("poll-interval", 30*time.Second, "Follow mode: how often to re-poll every configured VN endpoint")
	dryRun := flag.Bool("dry-run", false, "Poll every configured VN endpoint and log what would be upserted, without connecting to or writing into Postgres - useful for live-fetch verification when no Postgres is reachable")
	flag.Parse()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	cfg, err := config.Load(config.Flags{
		ConfigFile:           *configFile,
		Network:              *network,
		PostgresDSN:          *postgresDSN,
		ValidatorJSONRPCURLs: *validatorURLs,
	})
	if err != nil {
		log.Fatalf("vnhealth: %v", err)
	}

	endpoints := make([]vnhealth.Endpoint, 0, len(cfg.Network.ValidatorJSONRPCURLs))
	for _, url := range cfg.Network.ValidatorJSONRPCURLs {
		endpoints = append(endpoints, vnhealth.Endpoint{URL: url, Client: vnclient.New(url)})
	}

	var store vnhealth.Store
	if *dryRun {
		log.Printf("vnhealth: -dry-run set - polling %d VN endpoint(s) but not connecting to Postgres", len(endpoints))
		store = dryRunStore{}
	} else {
		database, err := db.Connect(ctx, cfg.PostgresDSN)
		if err != nil {
			log.Fatalf("vnhealth: %v", err)
		}
		defer database.Close()

		if err := database.Migrate(ctx); err != nil {
			log.Fatalf("vnhealth: migrate: %v", err)
		}
		store = database
	}

	h := vnhealth.New(endpoints, store)

	switch *mode {
	case "poll":
		log.Printf("vnhealth: polling network %q (%d VN endpoint(s) configured)", cfg.ActiveNetwork, len(endpoints))
		if err := h.Poll(ctx); err != nil {
			log.Fatalf("vnhealth: poll: %v", err)
		}
		log.Printf("vnhealth: poll complete")
	case "follow":
		log.Printf("vnhealth: following network %q (%d VN endpoint(s) configured), poll interval %s", cfg.ActiveNetwork, len(endpoints), *pollInterval)
		if err := h.Follow(ctx, *pollInterval); err != nil && ctx.Err() == nil {
			log.Fatalf("vnhealth: follow: %v", err)
		}
		log.Printf("vnhealth: shutting down")
	default:
		log.Fatalf("vnhealth: unknown -mode %q (want \"poll\" or \"follow\")", *mode)
	}
}

// dryRunStore is a vnhealth.Store that logs what it would have upserted instead of
// actually writing anywhere - backs the -dry-run flag, matching cmd/explorer's own
// -dry-run convention for live-fetch verification when no Postgres connection is
// available/desired.
type dryRunStore struct{}

func (dryRunStore) UpsertValidatorHealth(_ context.Context, v db.ValidatorHealth) error {
	log.Printf("vnhealth: dry-run: would upsert validator health %s (last_seen_epoch=%v shard=[%v,%v] consensus_status=%v)",
		v.PublicKey, derefUint64(v.LastSeenEpoch), derefUint32(v.ShardGroupStart), derefUint32(v.ShardGroupEndInclusive), derefString(v.ConsensusStatus))
	return nil
}

func derefUint64(p *uint64) interface{} {
	if p == nil {
		return "?"
	}
	return *p
}

func derefUint32(p *uint32) interface{} {
	if p == nil {
		return "?"
	}
	return *p
}

func derefString(p *string) interface{} {
	if p == nil {
		return "?"
	}
	return *p
}
