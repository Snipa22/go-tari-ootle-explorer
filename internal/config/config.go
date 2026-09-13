// Package config resolves this repo's process-wide configuration: the active network
// name, the Postgres DSN, the HTTP listen address, and — per active network — the
// tari_indexer REST base URL, the tari_validator_node JSON-RPC URL(s), and the L1
// base-node GRPC host(s).
//
// This is deliberately network-agnostic (see AGENTS.md's standing rule against
// hardcoded network/endpoint assumptions): every network-specific value is either read
// from a TOML config file's `[networks.<name>]` table (modeled on tari-cli's
// `tari.config.toml` shape, see crates/cli/src/cli/config.rs in tari-project/tari-cli)
// or, for the one documented local-dev fallback network ("localnet"), a default that
// points at the same 127.0.0.1 ports tari_indexer/tari_validator_node/minotari_node
// already bind by default (see the DefaultLocalnet* constants below for exact
// upstream-source citations) — never a real/public network endpoint.
//
// Resolution order for every field is CLI flag > env var > config file > default,
// following go-tari-explorer's internal/config env-var-with-sane-default pattern
// (TARI_EXPLORER_* there, TARI_OOTLE_EXPLORER_* here).
package config

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// DefaultNetworkName is the network selected when nothing else (flag, env var, config
// file) specifies one. It is the only network this package will ever configure without
// an explicit config file, and it always resolves to the local-dev defaults below.
const DefaultNetworkName = "localnet"

// DefaultPostgresDSN points at a local-dev Postgres instance. Override with
// TARI_OOTLE_EXPLORER_POSTGRES_DSN, the -postgres-dsn flag, or a config file's
// top-level postgres_dsn key in production.
const DefaultPostgresDSN = "postgres://tari_ootle_explorer:tari_ootle_explorer@localhost:5432/tari_ootle_explorer?sslmode=disable"

// DefaultHTTPListenAddr is the address cmd/server listens on absent an override.
const DefaultHTTPListenAddr = ":8080"

// DefaultLocalnetIndexerRESTBaseURL matches tari_indexer's own default
// api_listen_address ("127.0.0.1:18300", see IndexerConfig::default in
// applications/tari_indexer/src/config.rs, tari-project/tari-ootle commit d89dc92,
// 2026-09-11) — i.e. this is "point at a tari_indexer started with no config
// overrides on this machine", not an arbitrary made-up port.
const DefaultLocalnetIndexerRESTBaseURL = "http://127.0.0.1:18300"

// DefaultLocalnetValidatorJSONRPCURL matches tari_validator_node's own default
// json_rpc_listener_address ("127.0.0.1:18200", see ApplicationConfig::default in
// applications/tari_validator_node/src/config.rs, same tari-ootle commit as above)
// with the /json_rpc path appended.
const DefaultLocalnetValidatorJSONRPCURL = "http://127.0.0.1:18200/json_rpc"

// DefaultLocalnetL1BaseNodeGRPCHost matches minotari_node's documented default
// base_node_grpc_address ("127.0.0.1:18142" — see e.g.
// common/config/presets/i_indexer.toml and applications/minotari_mcp_common's
// health_monitor.rs test fixtures, tari-project/tari repo) — the same default port
// every other tari-ootle preset config assumes a local L1 base node is reachable on.
const DefaultLocalnetL1BaseNodeGRPCHost = "127.0.0.1:18142"

// Env var names, following go-tari-explorer's TARI_EXPLORER_* convention with this
// repo's own TARI_OOTLE_EXPLORER_ prefix.
const (
	envConfigFile           = "TARI_OOTLE_EXPLORER_CONFIG_FILE"
	envNetwork              = "TARI_OOTLE_EXPLORER_NETWORK"
	envPostgresDSN          = "TARI_OOTLE_EXPLORER_POSTGRES_DSN"
	envHTTPListenAddr       = "TARI_OOTLE_EXPLORER_HTTP_ADDR"
	envIndexerRESTBaseURL   = "TARI_OOTLE_EXPLORER_INDEXER_REST_BASE_URL"
	envValidatorJSONRPCURLs = "TARI_OOTLE_EXPLORER_VALIDATOR_JSONRPC_URLS"
	envL1BaseNodeGRPCHosts  = "TARI_OOTLE_EXPLORER_L1_BASE_NODE_GRPC_HOSTS"
	envMetadataServerURL    = "TARI_OOTLE_EXPLORER_METADATA_SERVER_URL"
)

// NetworkConfig is the set of fields a single network needs: where to reach that
// network's tari_indexer REST API, its tari_validator_node JSON-RPC endpoint(s), and
// its L1 base-node GRPC host(s).
type NetworkConfig struct {
	IndexerRESTBaseURL   string   `toml:"indexer_rest_base_url"`
	ValidatorJSONRPCURLs []string `toml:"validator_jsonrpc_urls"`
	L1BaseNodeGRPCHosts  []string `toml:"l1_base_node_grpc_hosts"`

	// MetadataServerURL is the community template-metadata server's base URL for
	// this network (e.g. "https://ootle-templates-esme.tari.com" for esmeralda,
	// confirmed live/reachable this session - see DISPATCH_BRIEF.md). Per that
	// brief and tari-cli's own config (crates/cli/src/project/config.rs's
	// DEFAULT_METADATA_SERVER_URL_ESMERALDA), the configured value already
	// includes the server's own "/community-templates" path prefix -
	// internal/registrymetaclient appends only the "/api/templates/..." route
	// suffix on top of exactly this value, never assumes or re-adds the prefix
	// itself. Empty means "no metadata server configured for this network" - this
	// repo's standing rule is no hardcoded non-local-dev endpoint default, so
	// cmd/registry (the only consumer of this field) fails loudly at startup
	// rather than silently defaulting to a real public endpoint.
	MetadataServerURL string `toml:"metadata_server_url"`
}

// Config is the fully-resolved, ready-to-use configuration: the active network's
// NetworkConfig plus the global (not per-network) fields.
type Config struct {
	ActiveNetwork  string
	PostgresDSN    string
	HTTPListenAddr string
	Network        NetworkConfig
}

// Flags holds every CLI-flag-shaped override this package accepts. Callers (cmd/*
// binaries) populate this from their own flag.FlagSet after parsing; this package
// deliberately doesn't own flag registration, so it stays testable without any actual
// command-line plumbing. ValidatorJSONRPCURLs/L1BaseNodeGRPCHosts are comma-separated
// strings (mirroring go-tari-explorer's ParseHostList convention for the same kind of
// field) rather than []string, since that's the natural shape for a single CLI flag
// value or env var.
type Flags struct {
	ConfigFile           string
	Network              string
	PostgresDSN          string
	HTTPListenAddr       string
	IndexerRESTBaseURL   string
	ValidatorJSONRPCURLs string
	L1BaseNodeGRPCHosts  string
	MetadataServerURL    string
}

// fileConfig is the raw shape decoded from a TOML config file, modeled on tari-cli's
// tari.config.toml ([networks.<name>] blocks, see crates/cli/src/cli/config.rs in
// tari-project/tari-cli) — global fields at the top level, per-network fields nested
// under a `networks` table keyed by network name.
type fileConfig struct {
	ActiveNetwork  string                   `toml:"active_network"`
	PostgresDSN    string                   `toml:"postgres_dsn"`
	HTTPListenAddr string                   `toml:"http_listen_addr"`
	Networks       map[string]NetworkConfig `toml:"networks"`
}

// Load resolves a Config from flags, env vars, an optional TOML config file, and this
// package's local-dev defaults, in that precedence order for every field.
//
// A config file is only read if flags.ConfigFile or TARI_OOTLE_EXPLORER_CONFIG_FILE is
// set — there is no implicit default config file path to look up, per AGENTS.md's rule
// against hardcoded/implicit infra assumptions. If a config file IS loaded and defines
// a non-empty `networks` table, the resolved active network must be one of its keys —
// referencing an unknown network (e.g. via -network) fails loudly rather than silently
// falling back to a default, except that DefaultNetworkName ("localnet") always
// resolves via the hardcoded local-dev defaults above when no config file is loaded at
// all.
func Load(flags Flags) (*Config, error) {
	var file fileConfig
	fileLoaded := false

	configPath := firstNonEmpty(flags.ConfigFile, os.Getenv(envConfigFile))
	if configPath != "" {
		raw, err := os.ReadFile(configPath)
		if err != nil {
			return nil, fmt.Errorf("config: read %s: %w", configPath, err)
		}
		if err := toml.Unmarshal(raw, &file); err != nil {
			return nil, fmt.Errorf("config: parse %s: %w", configPath, err)
		}
		fileLoaded = true
	}

	activeNetwork := firstNonEmpty(flags.Network, os.Getenv(envNetwork), file.ActiveNetwork, DefaultNetworkName)
	postgresDSN := firstNonEmpty(flags.PostgresDSN, os.Getenv(envPostgresDSN), file.PostgresDSN, DefaultPostgresDSN)
	httpAddr := firstNonEmpty(flags.HTTPListenAddr, os.Getenv(envHTTPListenAddr), file.HTTPListenAddr, DefaultHTTPListenAddr)

	var base NetworkConfig
	switch {
	case fileLoaded && len(file.Networks) > 0:
		nc, ok := file.Networks[activeNetwork]
		if !ok {
			return nil, fmt.Errorf(
				"config: network %q not found in config file %s (known networks: %s)",
				activeNetwork, configPath, strings.Join(sortedKeys(file.Networks), ", "),
			)
		}
		base = nc
	case activeNetwork == DefaultNetworkName:
		base = NetworkConfig{
			IndexerRESTBaseURL:   DefaultLocalnetIndexerRESTBaseURL,
			ValidatorJSONRPCURLs: []string{DefaultLocalnetValidatorJSONRPCURL},
			L1BaseNodeGRPCHosts:  []string{DefaultLocalnetL1BaseNodeGRPCHost},
		}
	default:
		// No config file was loaded at all, and the active network isn't the
		// local-dev default: leave base zero-valued. This isn't an error (there is
		// nothing to "not find" without a config file), but the resulting
		// NetworkConfig will be empty unless flags/env fill it in below — that's
		// the caller's responsibility, consistent with never hardcoding a
		// non-local-dev endpoint here.
	}

	indexerURL := firstNonEmpty(flags.IndexerRESTBaseURL, os.Getenv(envIndexerRESTBaseURL), base.IndexerRESTBaseURL)
	validatorURLs := firstNonEmptyList(parseList(flags.ValidatorJSONRPCURLs), parseList(os.Getenv(envValidatorJSONRPCURLs)), base.ValidatorJSONRPCURLs)
	l1Hosts := firstNonEmptyList(parseList(flags.L1BaseNodeGRPCHosts), parseList(os.Getenv(envL1BaseNodeGRPCHosts)), base.L1BaseNodeGRPCHosts)
	metadataServerURL := firstNonEmpty(flags.MetadataServerURL, os.Getenv(envMetadataServerURL), base.MetadataServerURL)

	return &Config{
		ActiveNetwork:  activeNetwork,
		PostgresDSN:    postgresDSN,
		HTTPListenAddr: httpAddr,
		Network: NetworkConfig{
			IndexerRESTBaseURL:   indexerURL,
			ValidatorJSONRPCURLs: validatorURLs,
			L1BaseNodeGRPCHosts:  l1Hosts,
			MetadataServerURL:    metadataServerURL,
		},
	}, nil
}

// firstNonEmpty returns the first non-empty string in vals, or "" if all are empty.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// firstNonEmptyList returns the first non-empty (non-nil, len > 0) slice in vals, or
// nil if all are empty.
func firstNonEmptyList(vals ...[]string) []string {
	for _, v := range vals {
		if len(v) > 0 {
			return v
		}
	}
	return nil
}

// parseList splits a comma-separated list, trimming whitespace and dropping empty
// entries — same convention as go-tari-explorer's internal/config.ParseHostList.
// Returns nil for an empty input, so it composes with firstNonEmptyList above.
func parseList(raw string) []string {
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// sortedKeys returns the sorted keys of a networks map, for a deterministic and
// readable "known networks: ..." error message.
func sortedKeys(m map[string]NetworkConfig) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
