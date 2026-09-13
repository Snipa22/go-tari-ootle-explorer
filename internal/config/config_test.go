package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestLoad_DefaultsToLocalDevLocalnet covers the "no flag, no env, no config file"
// path: everything falls back to this package's documented local-dev defaults.
func TestLoad_DefaultsToLocalDevLocalnet(t *testing.T) {
	cfg, err := Load(Flags{})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.ActiveNetwork != DefaultNetworkName {
		t.Errorf("ActiveNetwork = %q, want %q", cfg.ActiveNetwork, DefaultNetworkName)
	}
	if cfg.PostgresDSN != DefaultPostgresDSN {
		t.Errorf("PostgresDSN = %q, want %q", cfg.PostgresDSN, DefaultPostgresDSN)
	}
	if cfg.HTTPListenAddr != DefaultHTTPListenAddr {
		t.Errorf("HTTPListenAddr = %q, want %q", cfg.HTTPListenAddr, DefaultHTTPListenAddr)
	}
	if cfg.Network.IndexerRESTBaseURL != DefaultLocalnetIndexerRESTBaseURL {
		t.Errorf("IndexerRESTBaseURL = %q, want %q", cfg.Network.IndexerRESTBaseURL, DefaultLocalnetIndexerRESTBaseURL)
	}
	if want := []string{DefaultLocalnetValidatorJSONRPCURL}; !reflect.DeepEqual(cfg.Network.ValidatorJSONRPCURLs, want) {
		t.Errorf("ValidatorJSONRPCURLs = %v, want %v", cfg.Network.ValidatorJSONRPCURLs, want)
	}
	if want := []string{DefaultLocalnetL1BaseNodeGRPCHost}; !reflect.DeepEqual(cfg.Network.L1BaseNodeGRPCHosts, want) {
		t.Errorf("L1BaseNodeGRPCHosts = %v, want %v", cfg.Network.L1BaseNodeGRPCHosts, want)
	}
	if cfg.Network.MetadataServerURL != "" {
		t.Errorf("MetadataServerURL = %q, want \"\" (no hardcoded local-dev default - see AGENTS.md's standing rule)", cfg.Network.MetadataServerURL)
	}
}

// TestLoad_PrecedenceFlagOverEnvOverFileOverDefault is requirement (a): for a single
// field (PostgresDSN), verify each precedence step in isolation by adding one more
// override source at a time and checking the winner shifts each time.
func TestLoad_PrecedenceFlagOverEnvOverFileOverDefault(t *testing.T) {
	fileDSN := "postgres://file-user:file-pass@file-host:5432/file_db?sslmode=disable"
	envDSN := "postgres://env-user:env-pass@env-host:5432/env_db?sslmode=disable"
	flagDSN := "postgres://flag-user:flag-pass@flag-host:5432/flag_db?sslmode=disable"

	configFile := writeTempConfig(t, `
active_network = "localnet"
postgres_dsn = "`+fileDSN+`"

[networks.localnet]
indexer_rest_base_url = "http://127.0.0.1:18300"
`)

	// Step 1: nothing but the config file set -> file value wins over the default.
	cfg, err := Load(Flags{ConfigFile: configFile})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.PostgresDSN != fileDSN {
		t.Fatalf("step 1: PostgresDSN = %q, want file value %q", cfg.PostgresDSN, fileDSN)
	}

	// Step 2: env var set alongside the file -> env wins over file.
	t.Setenv(envPostgresDSN, envDSN)
	cfg, err = Load(Flags{ConfigFile: configFile})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.PostgresDSN != envDSN {
		t.Fatalf("step 2: PostgresDSN = %q, want env value %q", cfg.PostgresDSN, envDSN)
	}

	// Step 3: flag set alongside both -> flag wins over env (and file).
	cfg, err = Load(Flags{ConfigFile: configFile, PostgresDSN: flagDSN})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.PostgresDSN != flagDSN {
		t.Fatalf("step 3: PostgresDSN = %q, want flag value %q", cfg.PostgresDSN, flagDSN)
	}
}

// TestLoad_MultiNetworkFixtureResolvesRequestedNetwork is requirement (b): loading a
// real multi-network TOML fixture resolves the right network's fields when
// -network=<name> is set, for each of the fixture's networks in turn.
func TestLoad_MultiNetworkFixtureResolvesRequestedNetwork(t *testing.T) {
	fixture := filepath.Join("testdata", "multi_network.toml")

	t.Run("esmeralda", func(t *testing.T) {
		cfg, err := Load(Flags{ConfigFile: fixture, Network: "esmeralda"})
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		if cfg.ActiveNetwork != "esmeralda" {
			t.Errorf("ActiveNetwork = %q, want %q", cfg.ActiveNetwork, "esmeralda")
		}
		if want := "https://esmeralda.indexer.example:18300"; cfg.Network.IndexerRESTBaseURL != want {
			t.Errorf("IndexerRESTBaseURL = %q, want %q", cfg.Network.IndexerRESTBaseURL, want)
		}
		wantVNs := []string{"https://esmeralda.vn1.example:18200/json_rpc", "https://esmeralda.vn2.example:18200/json_rpc"}
		if !reflect.DeepEqual(cfg.Network.ValidatorJSONRPCURLs, wantVNs) {
			t.Errorf("ValidatorJSONRPCURLs = %v, want %v", cfg.Network.ValidatorJSONRPCURLs, wantVNs)
		}
		wantHosts := []string{"esmeralda.basenode1.example:18142", "esmeralda.basenode2.example:18142"}
		if !reflect.DeepEqual(cfg.Network.L1BaseNodeGRPCHosts, wantHosts) {
			t.Errorf("L1BaseNodeGRPCHosts = %v, want %v", cfg.Network.L1BaseNodeGRPCHosts, wantHosts)
		}
		if want := "https://esmeralda.metadata.example/community-templates"; cfg.Network.MetadataServerURL != want {
			t.Errorf("MetadataServerURL = %q, want %q", cfg.Network.MetadataServerURL, want)
		}
		// Global fields still come from the file's top level, unaffected by which
		// network was requested.
		if want := ":9090"; cfg.HTTPListenAddr != want {
			t.Errorf("HTTPListenAddr = %q, want %q", cfg.HTTPListenAddr, want)
		}
	})

	t.Run("localnet", func(t *testing.T) {
		cfg, err := Load(Flags{ConfigFile: fixture, Network: "localnet"})
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		if cfg.ActiveNetwork != "localnet" {
			t.Errorf("ActiveNetwork = %q, want %q", cfg.ActiveNetwork, "localnet")
		}
		if want := "http://127.0.0.1:18300"; cfg.Network.IndexerRESTBaseURL != want {
			t.Errorf("IndexerRESTBaseURL = %q, want %q", cfg.Network.IndexerRESTBaseURL, want)
		}
		wantVNs := []string{"http://127.0.0.1:18200/json_rpc"}
		if !reflect.DeepEqual(cfg.Network.ValidatorJSONRPCURLs, wantVNs) {
			t.Errorf("ValidatorJSONRPCURLs = %v, want %v", cfg.Network.ValidatorJSONRPCURLs, wantVNs)
		}
		if want := "http://127.0.0.1:18500/community-templates"; cfg.Network.MetadataServerURL != want {
			t.Errorf("MetadataServerURL = %q, want %q", cfg.Network.MetadataServerURL, want)
		}
	})

	t.Run("file active_network used when no -network flag given", func(t *testing.T) {
		// The fixture's own active_network is "esmeralda" - confirm that's honored
		// absent an explicit override.
		cfg, err := Load(Flags{ConfigFile: fixture})
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		if cfg.ActiveNetwork != "esmeralda" {
			t.Errorf("ActiveNetwork = %q, want %q (from file's active_network)", cfg.ActiveNetwork, "esmeralda")
		}
	})
}

// TestLoad_UnknownNetworkFailsLoudly is requirement (c): a config file with an unknown
// network name referenced by -network fails loudly, not silently defaulting to
// localnet or to whatever the file's active_network says.
func TestLoad_UnknownNetworkFailsLoudly(t *testing.T) {
	fixture := filepath.Join("testdata", "multi_network.toml")

	_, err := Load(Flags{ConfigFile: fixture, Network: "does-not-exist"})
	if err == nil {
		t.Fatal("Load() error = nil, want an error for an unknown network name")
	}

	const wantSubstr = `network "does-not-exist" not found in config file`
	if got := err.Error(); !strings.Contains(got, wantSubstr) {
		t.Errorf("Load() error = %q, want it to contain %q", got, wantSubstr)
	}
}

// TestLoad_PerFieldOverridesOnTopOfFileNetwork checks that a single field override
// (flag or env) layers on top of an otherwise file-resolved NetworkConfig, rather than
// requiring every field to come from the same source.
func TestLoad_PerFieldOverridesOnTopOfFileNetwork(t *testing.T) {
	fixture := filepath.Join("testdata", "multi_network.toml")

	cfg, err := Load(Flags{
		ConfigFile:         fixture,
		Network:            "localnet",
		IndexerRESTBaseURL: "http://overridden.example:18300",
	})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if want := "http://overridden.example:18300"; cfg.Network.IndexerRESTBaseURL != want {
		t.Errorf("IndexerRESTBaseURL = %q, want %q", cfg.Network.IndexerRESTBaseURL, want)
	}
	// The rest of localnet's NetworkConfig should be untouched by the override.
	wantVNs := []string{"http://127.0.0.1:18200/json_rpc"}
	if !reflect.DeepEqual(cfg.Network.ValidatorJSONRPCURLs, wantVNs) {
		t.Errorf("ValidatorJSONRPCURLs = %v, want %v", cfg.Network.ValidatorJSONRPCURLs, wantVNs)
	}
}

// writeTempConfig writes contents to a temp TOML file and returns its path.
func writeTempConfig(t *testing.T, contents string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("writeTempConfig: %v", err)
	}
	return path
}
