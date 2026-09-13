---
description: go-tari-* ecosystem repo — Tari Ootle (L2) explorer/health/registry tooling
---

# AGENTS.md

Instructions for AI coding agents (OpenCode, Claude Code, or any `agents.md`-compatible tool)
working in this repository. Read this before making changes.

## Project

- **What this repo is:** v1 of a Tari **Ootle** (L2 smart-contract layer) explorer platform —
  the L2 analog of `go-tari-explorer` (which covers L1). One service, one Postgres store,
  covering four surfaces: (1) indexer-backed block/substate/transaction explorer, (2)
  validator/committee health, (3) L1→L2 burn-claim tracking, (4) community template registry
  mirror. Foundational/v1 scope — a working skeleton with real (not stub) core pieces for each
  surface, not the full eventual feature set.
- **Module path:** `github.com/Snipa22/go-tari-ootle-explorer`
- **Primary real upstream sources this repo talks to** (verify method names/shapes against
  these live, don't guess — see "Primary source" below):
  - `tari_indexer`'s REST API (default port varies by network config) — this is the PRIMARY
    data source. Confirmed real routes as of tari-ootle commit `d89dc92` (2026-09-11):
    `/network`, `/network/economics`, `/network/stats`, `/network/connections`, `/validators`,
    `/substates/{fetch,watched,{substate_id}}`, `/transactions/{,dry-run,recent,{id},{id}/result,events,events/stream}`,
    `/templates/{cached,watched,catalogue,catalogue/{addr},{addr}}`, `/non-fungibles`,
    `/utxos/{,fetch,stream}`, `/transaction-receipts/{,{address}}`, `/resources/{xtr,tari,{addr}}`,
    `/epoch-checkpoints/{,latest}`, `/events` (SSE), `/health`, `/ready`, `/identity`, `/info`.
    Source: `applications/tari_indexer/src/rest_api/server.rs`.
  - `tari_validator_node`'s JSON-RPC (`/json_rpc`) — SECONDARY source, used specifically for
    committee/consensus health data the indexer's REST API doesn't surface: `get_committee`,
    `get_all_vns`, `get_shard_key`, `get_consensus_status`, `get_epoch_manager_stats`,
    `get_comms_stats`, `get_connections`, `get_identity`. Source:
    `applications/tari_validator_node/src/json_rpc/server.rs`.
  - L1 base node (via `go-tari-grpc-lib`, same as `go-tari-explorer`) — only needed for the
    burn-claim tracker's L1 side (watching burnt UTXOs). Reuse `go-tari-grpc-lib`, don't
    re-implement a GRPC client.
- **Depends on:** `go-tari-grpc-lib` (L1 GRPC types, for burn-claim's L1 side only).
- **Versioning note:** single-module repo — release-please `release-type: simple` (matches
  `go-tari-explorer`'s convention, not `go-tari-tools`' per-path monorepo mode).

## Primary source — read this before writing any indexer/validator client code

**Tari's Ootle codebase moves fast and doc comments can be stale.** Before implementing any
client call against the indexer REST API or validator JSON-RPC:

1. Clone fresh, shallow: `git clone --depth 1 https://github.com/tari-project/tari-ootle.git`
2. Confirm the route/method still exists and its exact request/response shape by reading
   `applications/tari_indexer/src/rest_api/handlers/*.rs` (REST) or
   `applications/tari_validator_node/src/json_rpc/handlers.rs` (JSON-RPC) directly — never
   infer a shape from a README example or this file's route list above, which will go stale.
3. Note the commit hash + date in any commit/PR touching this, so reviewers know how current
   the implementation is against a fast-moving upstream.
4. `tari-cli`'s `tari --skill` output (run `tari --skill` if the binary is available, or read
   `crates/*/README` in `tari-project/tari-cli`) documents the wallet-daemon JSON-RPC auth
   model (`Authorization: Bearer <api-key>`, minted with specific permission scopes) — reuse
   this exact auth pattern for anything in this repo that needs to talk to a wallet daemon
   (none of v1 does yet; the MCP gateway follow-up project will).

## Layout (target — build incrementally, don't front-load stub packages)

- `internal/config` — env var / CLI flag resolution, **network-agnostic by design**: network
  name (e.g. `esmeralda`, `localnet`) plus per-network indexer REST base URL, validator JSON-RPC
  URL(s), and L1 base-node GRPC host(s) are all config-driven, never hardcoded to one network.
  Follow `go-tari-explorer/internal/config`'s pattern (flag > env var > default) and
  `tari-cli`'s `tari.config.toml` network-table shape (`[networks.<name>]`) as prior art for
  the multi-network config shape — a TOML file with per-network sections beats one flat env
  var per field once more than one network is configured.
- `internal/indexerclient` — REST client for the `tari_indexer` API surface above. Model on
  `go-tari-explorer/internal/nodeclient`'s multi-host-with-failover pattern if multiple indexer
  instances are ever configured, but v1 can be single-endpoint per network.
- `internal/vnclient` — JSON-RPC client for validator-node health/committee endpoints.
- `internal/db` — Postgres access + embedded-SQL migration runner, same hand-rolled pattern as
  `go-tari-explorer/internal/db` (don't pull in golang-migrate for v1; the migration files are
  already named in its convention so it's a drop-in swap later if ever needed).
- `internal/explorer` — walks/polls the indexer for blocks, substates, transactions, template
  catalogue entries, UTXO updates; upserts into Postgres. One-shot backfill + polling follow
  modes, same shape as `go-tari-explorer/internal/indexer`.
- `internal/vnhealth` — polls validator-node JSON-RPC for committee membership, consensus
  status, connection counts; upserts into Postgres, keyed by validator identity + epoch.
- `internal/burnclaim` — L1 side: watches L1 base-node for burn UTXOs (special-flagged, per
  the tari-ootle README's manual burn/claim flow) via `go-tari-grpc-lib`. L2 side: watches the
  indexer for claim transactions referencing a burn proof. Joins the two; flags a burn as
  "stuck" once it's older than a configurable block-age threshold with no matching claim.
- `internal/registry` — mirrors the community template-metadata server's catalogue (see
  `tari-cli`'s `--metadata-server-url` config and `tari metadata publish`) into Postgres —
  name, tags, category, metadata hash, publish history. Confirm the metadata server's own API
  shape from `tari-project/tari-cli` source before implementing the client; it's a separate
  service from the indexer.
- `internal/server` — `net/http` + `html/template`, HTMX (CDN script, no build step), same
  convention as `go-tari-explorer/internal/server`. **Progressive disclosure**: default views
  (recent blocks/txs, live validator/shard status, recent template publishes) stay calm and
  liveness-focused; comprehensive/noisy data (all-time totals, unconfirmed/failed probes,
  stuck-claim history) goes in separate dedicated views or extra columns, never filtered away
  entirely — it's still fully collected, just not in the default view.
- `cmd/explorer` — CLI entrypoint for the indexer-polling subsystem (`-mode=backfill|follow`).
- `cmd/vnhealth` — CLI entrypoint for validator health polling.
- `cmd/burnclaim` — CLI entrypoint for burn-claim tracking.
- `cmd/registry` — CLI entrypoint for template registry mirroring.
- `cmd/server` — CLI entrypoint for the HTTP UI (reads from all of the above via `internal/db`).

## v1 scope — build these first, in this order

1. `internal/config` with real multi-network TOML support (config loader + tests, no
   hardcoded network assumptions per Alex's standing rule against static infra assumptions).
2. `internal/db` — Postgres + migration runner, minimal-viable schema for exactly what v1
   needs (blocks/transactions summary table, validator/committee table, burn/claim table,
   template registry table). Don't front-load speculative columns.
3. `internal/indexerclient` + `internal/explorer` — the indexer-polling subsystem, since
   everything else in this repo either depends on it or is independent of it. Get this real
   and working against a live testnet indexer before building the others.
4. `internal/vnclient` + `internal/vnhealth` — validator health, independent subsystem.
5. `internal/burnclaim` — depends on both L1 GRPC (`go-tari-grpc-lib`) and the indexer client
   from step 3.
6. `internal/registry` — independent subsystem, template metadata server client.
7. `internal/server` — ties all four data sources together into one HTMX UI with progressive
   disclosure.

Each subsystem should be independently buildable/testable — don't couple `internal/explorer`
to `internal/vnhealth` just because they'll eventually share a DB connection pool.

## Commands

- **Build:** `go build ./...`
- **Test:** `go test ./... -count=1`
- **Vet:** `go vet ./...`
- **Format:** `gofmt -l .` (should return nothing; `gofmt -w .` to fix)
- **Tidy:** `go mod tidy`

Run build + vet + gofmt + test before considering any change complete. CI will re-check all
four; catch failures locally first.

## Conventions

- **Conventional Commits** required — commit type (`feat`/`fix`/`chore`/etc.) drives automated
  SemVer via release-please. Don't guess the type; pick the one that matches the actual change.
- **Rebase, never merge.** No merge commits in PR branches. Rebase onto `main` before pushing
  updates.
- **No direct commits/pushes to `main`.** Always via PR — exception: this repo's own initial
  scaffold commit (this file + LICENSE + CONTRIBUTING + go.mod + empty package skeletons) may
  land directly on `main` under admin-bypass since the repo is brand new; branch protection is
  applied immediately after. Everything after that scaffold commit goes through PR review.
- Follow existing package structure and naming — don't introduce a new pattern without
  checking how `go-tari-explorer` (the L1 sibling) does the equivalent thing first.
- Pin dependency versions explicitly in `go.mod` — this ecosystem has a known history of
  version skew across repos on `go-tari-grpc-lib`; don't make it worse.
- **No static/hardcoded assumptions** — network names, RPC endpoints, indexer/validator host
  lists, ports: all config-driven, never a hardcoded default beyond a documented local-dev
  fallback. This is a hard standing rule for this ecosystem, not a style preference.
- Schema in `internal/db/migrations` is intentionally minimal-viable (v1) — extend it
  incrementally as real features need new columns/tables, don't front-load speculative schema.

## Don't

- Don't push directly to `main` (except the one scaffold-commit exception above) or
  force-push shared branches.
- Don't add merge commits — rebase instead.
- Don't touch generated/vendored code by hand.
- Don't silently change the licensing header or LICENSE file — that's a human decision, flag
  it instead.
- Don't skip tests because "there weren't any before" — add coverage for what you touch.
- Don't infer indexer REST or validator JSON-RPC request/response shapes from this file's route
  list or from web search — Tari's own docs are frequently absent/stale for these; always read
  the real `tari-project/tari-ootle` source (see "Primary source" above) before implementing a
  client call.
- Don't build the MCP gateway in this repo — it's a separate, later project that will read
  from this repo's Postgres store but isn't part of v1 scope here.

## Disclosure

If you (the agent) are making a substantial autonomous contribution, make sure the human
operator adds a disclosure note to the PR per `CONTRIBUTING.md`. Don't assume this happens
automatically — mention it if it's about to be skipped.
