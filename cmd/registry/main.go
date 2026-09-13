// Command registry mirrors the community template-metadata server's featured
// templates into Postgres. Supports both a one-shot sync and a "keep polling"
// follow mode - see internal/registry's package doc for the Sync/Follow split.
//
// Per DISPATCH_BRIEF.md and internal/registrymetaclient's own doc comment: this
// mirrors ONLY the metadata server's "featured" template list - there is no
// confirmed unfiltered list/paginated route on that server, so this command cannot
// (and does not claim to) mirror every template that has ever published metadata to
// it.
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
	"github.com/Snipa22/go-tari-ootle-explorer/internal/registry"
	"github.com/Snipa22/go-tari-ootle-explorer/internal/registrymetaclient"
)

func main() {
	configFile := flag.String("config", "", "Path to a TOML config file (env: TARI_OOTLE_EXPLORER_CONFIG_FILE)")
	network := flag.String("network", "", "Network name to run against, e.g. esmeralda (env: TARI_OOTLE_EXPLORER_NETWORK; default: localnet)")
	postgresDSN := flag.String("postgres-dsn", "", "Postgres connection string (env: TARI_OOTLE_EXPLORER_POSTGRES_DSN)")
	metadataServerURL := flag.String("metadata-server-url", "", "Community template-metadata server base URL for the active network, already including its \"/community-templates\" path prefix (env: TARI_OOTLE_EXPLORER_METADATA_SERVER_URL) - REQUIRED, no hardcoded default (see AGENTS.md's standing rule against hardcoded infra assumptions)")
	mode := flag.String("mode", "sync", `Mode: "sync" (one-shot fetch+upsert of the current featured template list) or "follow" (keeps polling)`)
	pollInterval := flag.Duration("poll-interval", 5*time.Minute, "Follow mode: how often to re-sync the featured template list")
	dryRun := flag.Bool("dry-run", false, "Fetch the real featured template list and log what would be upserted, without connecting to or writing into Postgres - useful for live-fetch verification when no Postgres is reachable")
	flag.Parse()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	cfg, err := config.Load(config.Flags{
		ConfigFile:        *configFile,
		Network:           *network,
		PostgresDSN:       *postgresDSN,
		MetadataServerURL: *metadataServerURL,
	})
	if err != nil {
		log.Fatalf("registry: %v", err)
	}

	if cfg.Network.MetadataServerURL == "" {
		log.Fatalf("registry: no metadata server URL configured for network %q - set -metadata-server-url, TARI_OOTLE_EXPLORER_METADATA_SERVER_URL, or a config file's metadata_server_url", cfg.ActiveNetwork)
	}

	client := registrymetaclient.New(cfg.Network.MetadataServerURL)

	var store registry.Store
	if *dryRun {
		log.Printf("registry: -dry-run set - fetching from %s but not connecting to Postgres", cfg.Network.MetadataServerURL)
		store = dryRunStore{}
	} else {
		database, err := db.Connect(ctx, cfg.PostgresDSN)
		if err != nil {
			log.Fatalf("registry: %v", err)
		}
		defer database.Close()

		if err := database.Migrate(ctx); err != nil {
			log.Fatalf("registry: migrate: %v", err)
		}
		store = database
	}

	r := registry.New(client, store)

	switch *mode {
	case "sync":
		log.Printf("registry: syncing network %q's featured templates from %s", cfg.ActiveNetwork, cfg.Network.MetadataServerURL)
		n, err := r.Sync(ctx)
		if err != nil {
			log.Fatalf("registry: sync: %v", err)
		}
		log.Printf("registry: sync complete: %d featured template(s) upserted", n)
	case "follow":
		log.Printf("registry: following network %q's featured templates from %s, poll interval %s", cfg.ActiveNetwork, cfg.Network.MetadataServerURL, *pollInterval)
		if err := r.Follow(ctx, *pollInterval); err != nil && ctx.Err() == nil {
			log.Fatalf("registry: follow: %v", err)
		}
		log.Printf("registry: shutting down")
	default:
		log.Fatalf("registry: unknown -mode %q (want \"sync\" or \"follow\")", *mode)
	}
}

// dryRunStore is a registry.Store that logs what it would have upserted instead of
// actually writing anywhere - backs the -dry-run flag, matching cmd/explorer,
// cmd/vnhealth, and cmd/burnclaim's own -dry-run convention for live-fetch
// verification when no Postgres connection is available/desired.
type dryRunStore struct{}

func (dryRunStore) UpsertTemplateRegistryMetadata(_ context.Context, t db.TemplateRegistryMetadataEntry) error {
	log.Printf("registry: dry-run: would upsert %s (name=%v code_size=%v is_featured=%v metadata_len=%d definition_len=%d)",
		t.TemplateAddress, derefString(t.TemplateName), derefInt64(t.CodeSize), derefBool(t.IsFeatured), len(t.Metadata), len(t.Definition))
	return nil
}

func derefString(p *string) interface{} {
	if p == nil {
		return "?"
	}
	return *p
}

func derefInt64(p *int64) interface{} {
	if p == nil {
		return "?"
	}
	return *p
}

func derefBool(p *bool) interface{} {
	if p == nil {
		return "?"
	}
	return *p
}
