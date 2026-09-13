package registry

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/Snipa22/go-tari-ootle-explorer/internal/db"
	"github.com/Snipa22/go-tari-ootle-explorer/internal/registrymetaclient"
)

// TestSync_AgainstLiveMetadataServerAndRealPostgres is the real end-to-end
// integration test the v1 scope brief asks for: a genuine Sync run against the LIVE
// community template-metadata server (https://ootle-templates-esme.tari.com,
// esmeralda), persisting into a REAL Postgres database, then checking rows actually
// landed with the real metadata-server-only fields populated.
//
// Both preconditions are gated behind environment variables so this test SKIPS
// cleanly (rather than failing the whole suite, or - worse - fabricating a pass)
// when either dependency is unavailable, per DISPATCH_BRIEF.md's explicit
// instruction and matching internal/explorer's own
// TestBackfill_AgainstLiveIndexerAndRealPostgres convention:
//   - TEST_LIVE_METADATA_SERVER_URL: the metadata server base URL to hit (already
//     including the "/community-templates" prefix - see internal/registrymetaclient's
//     doc comment). Unset -> skip.
//   - TEST_POSTGRES_DSN: a reachable Postgres DSN. Unset or unreachable -> skip.
func TestSync_AgainstLiveMetadataServerAndRealPostgres(t *testing.T) {
	metadataURL := os.Getenv("TEST_LIVE_METADATA_SERVER_URL")
	if metadataURL == "" {
		t.Skip("SKIP: TEST_LIVE_METADATA_SERVER_URL not set - no live metadata server to test Sync against")
	}
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("SKIP: TEST_POSTGRES_DSN not set - no Postgres to test Sync against")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	database, err := db.Connect(ctx, dsn)
	if err != nil {
		t.Skipf("SKIP: could not connect to Postgres at %s: %v", dsn, err)
	}
	defer database.Close()

	// Clean slate so this test is repeatable against a persistent dev Postgres
	// instance, matching internal/db/internal/explorer's own live-Postgres test
	// convention.
	if _, err := database.Pool.Exec(ctx, `DROP TABLE IF EXISTS schema_migrations, ootle_blocks, validators, burn_claims, template_registry CASCADE`); err != nil {
		t.Fatalf("cleanup before test: %v", err)
	}
	if err := database.Migrate(ctx); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}

	client := registrymetaclient.New(metadataURL)

	// Sanity-check the live server is actually reachable and returns real data
	// before running Sync against it, so a network hiccup surfaces as a clear
	// skip/fail here rather than a confusing failure deep inside Sync.
	entries, err := client.ListFeatured(ctx)
	if err != nil {
		t.Skipf("SKIP: live metadata server at %s not reachable: %v", metadataURL, err)
	}
	if len(entries) == 0 {
		t.Skip("SKIP: live metadata server returned zero featured templates - nothing to verify Sync against")
	}
	t.Logf("live metadata server: %d featured template(s), first = %q (%s)", len(entries), entries[0].TemplateName, entries[0].TemplateAddress)

	r := New(client, database)
	n, err := r.Sync(ctx)
	if err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	if n != len(entries) {
		t.Errorf("Sync() = %d, want %d (one per live featured entry)", n, len(entries))
	}
	t.Logf("Sync() upserted %d row(s)", n)

	var templateCount int
	if err := database.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM template_registry`).Scan(&templateCount); err != nil {
		t.Fatalf("count template_registry: %v", err)
	}
	if templateCount == 0 {
		t.Errorf("expected at least one template_registry row after Sync against a live network, got 0")
	}
	t.Logf("template_registry table: %d rows", templateCount)

	var featuredCount int
	if err := database.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM template_registry WHERE is_featured = true`).Scan(&featuredCount); err != nil {
		t.Fatalf("count featured template_registry rows: %v", err)
	}
	if featuredCount == 0 {
		t.Errorf("expected at least one is_featured=true row after Sync against a live network, got 0")
	}
	t.Logf("template_registry table: %d row(s) with is_featured=true", featuredCount)
}
