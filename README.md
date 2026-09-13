# go-tari-ootle-explorer

v1 of a Tari **Ootle** (L2 smart-contract layer) explorer platform — the L2 counterpart to
[`go-tari-explorer`](https://github.com/Snipa22/go-tari-explorer) (L1). Part of the
[`go-tari-*`](https://github.com/Snipa22?tab=repositories&q=go-tari) ecosystem maintained by
[Snipa22](https://github.com/Snipa22).

One service, one Postgres store, four data surfaces:

1. **Indexer explorer** — blocks, substates, transactions, UTXOs, epoch checkpoints, polled
   from a [`tari_indexer`](https://github.com/tari-project/tari-ootle) REST API.
2. **Validator/committee health** — committee membership, consensus status, connection counts,
   polled from `tari_validator_node` JSON-RPC.
3. **Burn-claim tracking** — joins L1 burn UTXOs (via `go-tari-grpc-lib`) against L2 claim
   transactions, flags stuck/pending claims.
4. **Template registry mirror** — indexes the community template-metadata server's catalogue
   (name, tags, category, metadata hash, publish history) for browsable discovery.

This is a **v1 foundation** — real (not stubbed) core pieces for each surface, not the full
eventual feature set. See `AGENTS.md` for the detailed design brief, build order, and the
real upstream API surface this repo talks to (verified against `tari-project/tari-ootle`
source, not guessed).

## Status

Scaffolding — see `AGENTS.md` for the v1 build order. Each subsystem
(`internal/explorer`, `internal/vnhealth`, `internal/burnclaim`, `internal/registry`) is being
built independently and incrementally.

## Configuration

Network-agnostic by design — network name, indexer REST base URL, validator JSON-RPC URL(s),
and L1 base-node GRPC host(s) are all config-driven (TOML, per-network sections, following
`tari-cli`'s `tari.config.toml` convention), never hardcoded to a single network.

## License

MIT — see `LICENSE`.
