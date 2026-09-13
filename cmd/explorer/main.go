// Command explorer walks tari_indexer's REST API (validators, the latest epoch
// checkpoint, and the template catalogue) and persists the result into Postgres.
// Supports both a one-shot backfill and a "keep polling" follow mode - see
// internal/explorer's package doc for the Backfill/Follow split.
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
	"github.com/Snipa22/go-tari-ootle-explorer/internal/explorer"
	"github.com/Snipa22/go-tari-ootle-explorer/internal/indexerclient"
)

func main() {
	configFile := flag.String("config", "", "Path to a TOML config file (env: TARI_OOTLE_EXPLORER_CONFIG_FILE)")
	network := flag.String("network", "", "Network name to run against, e.g. esmeralda (env: TARI_OOTLE_EXPLORER_NETWORK; default: localnet)")
	postgresDSN := flag.String("postgres-dsn", "", "Postgres connection string (env: TARI_OOTLE_EXPLORER_POSTGRES_DSN)")
	indexerURL := flag.String("indexer-rest-base-url", "", "tari_indexer REST base URL for the active network (env: TARI_OOTLE_EXPLORER_INDEXER_REST_BASE_URL)")
	mode := flag.String("mode", "backfill", `Mode: "backfill" (one-shot full sync) or "follow" (keeps polling for new data)`)
	pollInterval := flag.Duration("poll-interval", 30*time.Second, "Follow mode: how often to re-poll validators/checkpoint/catalogue")
	dryRun := flag.Bool("dry-run", false, "Fetch from the indexer and log what would be upserted, without connecting to or writing into Postgres - useful for live-fetch verification when no Postgres is reachable")
	flag.Parse()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	cfg, err := config.Load(config.Flags{
		ConfigFile:         *configFile,
		Network:            *network,
		PostgresDSN:        *postgresDSN,
		IndexerRESTBaseURL: *indexerURL,
	})
	if err != nil {
		log.Fatalf("explorer: %v", err)
	}

	indexer := indexerclient.New(cfg.Network.IndexerRESTBaseURL)

	var store explorer.Store
	if *dryRun {
		log.Printf("explorer: -dry-run set - fetching from %s but not connecting to Postgres", cfg.Network.IndexerRESTBaseURL)
		store = dryRunStore{}
	} else {
		database, err := db.Connect(ctx, cfg.PostgresDSN)
		if err != nil {
			log.Fatalf("explorer: %v", err)
		}
		defer database.Close()

		if err := database.Migrate(ctx); err != nil {
			log.Fatalf("explorer: migrate: %v", err)
		}
		store = database
	}

	ex := explorer.New(indexer, store)

	switch *mode {
	case "backfill":
		log.Printf("explorer: backfilling against network %q (%s)", cfg.ActiveNetwork, cfg.Network.IndexerRESTBaseURL)
		if err := ex.Backfill(ctx); err != nil {
			log.Fatalf("explorer: backfill: %v", err)
		}
		log.Printf("explorer: backfill complete")
	case "follow":
		log.Printf("explorer: following network %q (%s), poll interval %s", cfg.ActiveNetwork, cfg.Network.IndexerRESTBaseURL, *pollInterval)
		if err := ex.Follow(ctx, *pollInterval); err != nil && ctx.Err() == nil {
			log.Fatalf("explorer: follow: %v", err)
		}
		log.Printf("explorer: shutting down")
	default:
		log.Fatalf("explorer: unknown -mode %q (want \"backfill\" or \"follow\")", *mode)
	}
}

// dryRunStore is an explorer.Store that logs what it would have upserted instead of
// actually writing anywhere - backs the -dry-run flag, for live-fetch verification
// against a real indexer when no Postgres connection is available/desired (see
// DISPATCH_BRIEF.md's verification bar).
type dryRunStore struct{}

func (dryRunStore) UpsertValidator(_ context.Context, v db.Validator) error {
	log.Printf("explorer: dry-run: would upsert validator %s (last_seen_epoch=%d shard=[%d,%d])",
		v.PublicKey, v.LastSeenEpoch, v.ShardGroupStart, v.ShardGroupEndInclusive)
	return nil
}

func (dryRunStore) UpsertOotleBlock(_ context.Context, b db.OotleBlock) error {
	log.Printf("explorer: dry-run: would upsert ootle_block %s (height=%d epoch=%d)",
		b.BlockID, b.Height, b.Epoch)
	return nil
}

func (dryRunStore) UpsertTemplateRegistryEntry(_ context.Context, t db.TemplateRegistryEntry) error {
	log.Printf("explorer: dry-run: would upsert template_registry entry %s (%q at_epoch=%d)",
		t.TemplateAddress, t.TemplateName, t.AtEpoch)
	return nil
}
