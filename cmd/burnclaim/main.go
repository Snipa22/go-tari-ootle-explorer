// Command burnclaim tracks L1 Minotari burns and their L2 Ootle claims: an L1
// base-node GRPC scan for burn kernels, an L2 indexer substate lookup for claims, and
// stuck-detection against the network's real base_layer_confirmations lag. See
// internal/burnclaim's package doc for the full real-source citations and this
// dispatch's live-verification status.
//
// Two modes, matching internal/burnclaim's Scan/Follow split:
//   - "scan": one-shot. Requires -from-height/-to-height, walks exactly that L1
//     height range once, then runs a single claim-check + stuck-detection pass.
//   - "follow": keeps polling - see internal/burnclaim.Tracker.Follow's doc comment
//     for why a fresh Follow starts near the current tip rather than backfilling
//     history (use "scan" first for that).
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Snipa22/go-tari-ootle-explorer/internal/burnclaim"
	"github.com/Snipa22/go-tari-ootle-explorer/internal/config"
	"github.com/Snipa22/go-tari-ootle-explorer/internal/db"
	"github.com/Snipa22/go-tari-ootle-explorer/internal/indexerclient"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

func main() {
	configFile := flag.String("config", "", "Path to a TOML config file (env: TARI_OOTLE_EXPLORER_CONFIG_FILE)")
	network := flag.String("network", "", "Network name to run against, e.g. esmeralda (env: TARI_OOTLE_EXPLORER_NETWORK; default: localnet)")
	postgresDSN := flag.String("postgres-dsn", "", "Postgres connection string (env: TARI_OOTLE_EXPLORER_POSTGRES_DSN)")
	indexerURL := flag.String("indexer-rest-base-url", "", "tari_indexer REST base URL for the active network (env: TARI_OOTLE_EXPLORER_INDEXER_REST_BASE_URL)")
	l1Hosts := flag.String("l1-base-node-grpc-hosts", "", "Comma-separated L1 base-node GRPC host:port(s) for the active network (env: TARI_OOTLE_EXPLORER_L1_BASE_NODE_GRPC_HOSTS)")
	l1TLS := flag.Bool("l1-tls", false, "Dial the L1 base-node GRPC host(s) over TLS - required for public TLS-terminated endpoints (e.g. grpc.esmeralda.tari.com:443, confirmed live during this dispatch), NOT for a typical local-dev base node's plaintext GRPC")
	confirmationLag := flag.Uint64("confirmation-lag", 0, "Override the network's real base_layer_confirmations consensus constant (0 = look it up from -network via internal/burnclaim.KnownBaseLayerConfirmations; REQUIRED if -network isn't one of that map's known names - see AGENTS.md's standing rule against silently guessing a consensus constant)")
	extraStuckBlocks := flag.Uint64("extra-stuck-blocks", burnclaim.DefaultExtraStuckBlocks, "L1 blocks a pending burn may sit past the real confirmation lag before it's flagged 'stuck'")
	mode := flag.String("mode", "follow", `Mode: "scan" (one-shot walk of -from-height..-to-height) or "follow" (keeps polling)`)
	fromHeight := flag.Uint64("from-height", 0, `-mode=scan: first L1 height to scan (inclusive, required)`)
	toHeight := flag.Uint64("to-height", 0, `-mode=scan: last L1 height to scan (inclusive, required)`)
	startHeight := flag.Uint64("start-height", 0, `-mode=follow: L1 height to start following from (0 = start near the current tip - see internal/burnclaim.Tracker.Follow's doc comment)`)
	pollInterval := flag.Duration("poll-interval", 30*time.Second, "Follow mode: how often to re-scan/re-check/re-detect-stuck")
	dryRun := flag.Bool("dry-run", false, "Scan/check/detect against the real L1+indexer but log what would be upserted, without connecting to or writing into Postgres")
	flag.Parse()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	cfg, err := config.Load(config.Flags{
		ConfigFile:          *configFile,
		Network:             *network,
		PostgresDSN:         *postgresDSN,
		IndexerRESTBaseURL:  *indexerURL,
		L1BaseNodeGRPCHosts: *l1Hosts,
	})
	if err != nil {
		log.Fatalf("burnclaim: %v", err)
	}

	lag := *confirmationLag
	if lag == 0 {
		known, ok := burnclaim.KnownBaseLayerConfirmations[cfg.ActiveNetwork]
		if !ok {
			log.Fatalf("burnclaim: network %q has no known real base_layer_confirmations constant and -confirmation-lag was not set - see internal/burnclaim.KnownBaseLayerConfirmations and AGENTS.md's standing rule against guessing consensus constants", cfg.ActiveNetwork)
		}
		lag = known
	}

	var dialOpts []grpc.DialOption
	if *l1TLS {
		dialOpts = append(dialOpts, grpc.WithTransportCredentials(credentials.NewTLS(nil)))
	}
	l1, err := burnclaim.NewGRPCClient(cfg.Network.L1BaseNodeGRPCHosts, dialOpts...)
	if err != nil {
		log.Fatalf("burnclaim: %v", err)
	}
	defer l1.Close()

	indexer := indexerclient.New(cfg.Network.IndexerRESTBaseURL)

	var store burnclaim.Store
	if *dryRun {
		log.Printf("burnclaim: -dry-run set - scanning L1 hosts %v / indexer %s but not connecting to Postgres", cfg.Network.L1BaseNodeGRPCHosts, cfg.Network.IndexerRESTBaseURL)
		store = dryRunStore{}
	} else {
		database, err := db.Connect(ctx, cfg.PostgresDSN)
		if err != nil {
			log.Fatalf("burnclaim: %v", err)
		}
		defer database.Close()

		if err := database.Migrate(ctx); err != nil {
			log.Fatalf("burnclaim: migrate: %v", err)
		}
		store = database
	}

	tracker := burnclaim.New(l1, indexer, store, lag, *extraStuckBlocks)

	switch *mode {
	case "scan":
		if *fromHeight == 0 || *toHeight == 0 {
			log.Fatalf("burnclaim: -mode=scan requires both -from-height and -to-height")
		}
		log.Printf("burnclaim: scanning network %q L1 height range [%d,%d] (confirmation lag %d + extra %d = stuck threshold %d)",
			cfg.ActiveNetwork, *fromHeight, *toHeight, lag, *extraStuckBlocks, burnclaim.StuckThreshold(lag, *extraStuckBlocks))
		found, err := tracker.Scan(ctx, *fromHeight, *toHeight)
		if err != nil {
			log.Fatalf("burnclaim: scan: %v", err)
		}
		log.Printf("burnclaim: scan complete: %d burn(s) found in range", found)
	case "follow":
		log.Printf("burnclaim: following network %q, poll interval %s (confirmation lag %d + extra %d = stuck threshold %d)",
			cfg.ActiveNetwork, *pollInterval, lag, *extraStuckBlocks, burnclaim.StuckThreshold(lag, *extraStuckBlocks))
		if err := tracker.Follow(ctx, *pollInterval, *startHeight); err != nil && ctx.Err() == nil {
			log.Fatalf("burnclaim: follow: %v", err)
		}
		log.Printf("burnclaim: shutting down")
	default:
		log.Fatalf("burnclaim: unknown -mode %q (want \"scan\" or \"follow\")", *mode)
	}
}

// dryRunStore is a burnclaim.Store that logs what it would have upserted instead of
// actually writing anywhere - backs the -dry-run flag, for live-fetch verification
// against real L1/indexer endpoints when no Postgres connection is available/desired,
// matching cmd/explorer and cmd/vnhealth's own -dry-run convention. ListBurnClaimsByStatus
// always returns empty here - dry-run mode never has any real 'pending' rows to check
// claims/stuck-detect against, since nothing was ever actually inserted; a dry run
// therefore only exercises the L1 scan itself, not CheckClaims/DetectStuck.
type dryRunStore struct{}

func (dryRunStore) InsertPendingBurnClaim(_ context.Context, b db.BurnClaim) error {
	log.Printf("burnclaim: dry-run: would insert pending burn %s (commitment=%s burn_height=%d)",
		b.L1BurnTxHash, b.Commitment, b.BurnHeight)
	return nil
}

func (dryRunStore) ListBurnClaimsByStatus(_ context.Context, _ string) ([]db.BurnClaim, error) {
	return nil, nil
}

func (dryRunStore) MarkBurnClaimed(_ context.Context, commitment string, claimedAt time.Time) error {
	log.Printf("burnclaim: dry-run: would mark claimed commitment=%s claimed_at=%s", commitment, claimedAt)
	return nil
}

func (dryRunStore) MarkBurnStuck(_ context.Context, l1BurnTxHash string) error {
	log.Printf("burnclaim: dry-run: would mark stuck %s", l1BurnTxHash)
	return nil
}
