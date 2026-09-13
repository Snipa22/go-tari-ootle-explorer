// Command server serves the v1 read-only HTTP UI over this repo's Postgres store.
//
// IMPORTANT: this binary is READ-ONLY over the database - it never polls
// tari_indexer, tari_validator_node, an L1 base node, or the community metadata
// server itself. cmd/explorer, cmd/vnhealth, cmd/burnclaim, and cmd/registry are the
// separate, independent writer processes; run them alongside this one (each on its
// own schedule/mode) to actually populate the data this binary displays. See
// internal/server's package doc comment for the full reasoning.
package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Snipa22/go-tari-ootle-explorer/internal/config"
	"github.com/Snipa22/go-tari-ootle-explorer/internal/db"
	"github.com/Snipa22/go-tari-ootle-explorer/internal/server"
)

func main() {
	configFile := flag.String("config", "", "Path to a TOML config file (env: TARI_OOTLE_EXPLORER_CONFIG_FILE)")
	network := flag.String("network", "", "Network name to run against, e.g. esmeralda (env: TARI_OOTLE_EXPLORER_NETWORK; default: localnet) - only affects display/labeling, this binary doesn't dial any network endpoint itself")
	postgresDSN := flag.String("postgres-dsn", "", "Postgres connection string (env: TARI_OOTLE_EXPLORER_POSTGRES_DSN)")
	httpAddr := flag.String("http-addr", "", "HTTP listen address (env: TARI_OOTLE_EXPLORER_HTTP_ADDR; default: :8080)")
	flag.Parse()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	cfg, err := config.Load(config.Flags{
		ConfigFile:     *configFile,
		Network:        *network,
		PostgresDSN:    *postgresDSN,
		HTTPListenAddr: *httpAddr,
	})
	if err != nil {
		log.Fatalf("server: %v", err)
	}

	database, err := db.Connect(ctx, cfg.PostgresDSN)
	if err != nil {
		log.Fatalf("server: %v", err)
	}
	defer database.Close()

	// Migrate here too (same convention as every other cmd/* binary in this repo):
	// harmless/idempotent if a writer process already applied every migration, but
	// means this binary can also be the first one to stand up a brand-new database
	// (e.g. spinning up just the UI against an empty DB before any poller has run).
	if err := database.Migrate(ctx); err != nil {
		log.Fatalf("server: migrate: %v", err)
	}

	srv, err := server.New(database)
	if err != nil {
		log.Fatalf("server: %v", err)
	}

	log.Printf("server: serving network %q's data on %s (read-only - run cmd/explorer/cmd/vnhealth/cmd/burnclaim/cmd/registry separately to populate data)", cfg.ActiveNetwork, cfg.HTTPListenAddr)
	httpServer := &http.Server{
		Addr:    cfg.HTTPListenAddr,
		Handler: srv.Handler(),
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		_ = httpServer.Shutdown(shutdownCtx)
	}()

	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("server: %v", err)
	}
	log.Printf("server: shutting down")
}
