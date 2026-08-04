// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
//
// Package config loads starfleetctl configuration from .starfleet-ai/conf/*.yaml
package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Config holds all starfleetctl configuration.
type Config struct {
	Web        WebConfig        `yaml:"web"`
	Comms      CommsConfig      `yaml:"comms"`
	Fleet      FleetConfig      `yaml:"fleet"`
	ModelProxy ModelProxyConfig `yaml:"model_proxy"`
}

// ModelProxyConfig holds the model-proxy daemon configuration
// (.starfleet-ai/conf/model-proxy.yaml). The proxy front-ends the real
// model API backends (NVIDIA NIM, OpenCode Zen, ...) so ships talk to a
// single local OpenAI-compatible server that retries transient errors and
// catches streaming failures instead of leaking them into the agent.
type ModelProxyConfig struct {
	// ListenAddr is the proxy's local listen address (default 127.0.0.1:8443).
	ListenAddr string `yaml:"listen_addr"`
	PIDFile    string `yaml:"pid_file"`
	LogFile    string `yaml:"log_file"`
	// Providers is the ordered list of upstream backends to proxy.
	Providers []ModelProxyProvider `yaml:"providers"`
}

// ModelProxyProvider describes one upstream model API backend behind the
// proxy. ID is the opencode provider name exposed to ships (e.g. "nim-proxy",
// "zen-proxy"); BaseURL is the upstream OpenAI-compatible endpoint; APIKey
// supports env replacement ({env:VAR} or ${VAR}) since keys come via env.
type ModelProxyProvider struct {
	ID      string `yaml:"id"`
	Name    string `yaml:"name"`
	BaseURL string `yaml:"base_url"`
	APIKey  string `yaml:"api_key"`
	// MaxRetries retries a request when the upstream reports a transient
	// error (429/5xx/conn-reset). Default 3.
	MaxRetries int `yaml:"max_retries"`
	// RetryDelayMS sleeps between retries. Default 1000.
	RetryDelayMS int `yaml:"retry_delay_ms"`
}

// FleetConfig holds fleet-wide identity settings.
type FleetConfig struct {
	// Flagship is the canonical name of the flagship/control session.
	// Defaults to "Enterprise" when unset.
	Flagship string `yaml:"flagship"`
	// ShipNames is the worker ship-name pool. When empty, the compiled-in
	// Star Trek ship roster is used. The flagship name is always excluded.
	ShipNames []string `yaml:"ship_names"`
	// ProviderMode controls which providers are included in generated
	// per-ship opencode configs. "all" (default) copies user providers
	// from ~/.config/opencode/opencode.json AND injects model-proxy
	// providers. "model-proxy-only" skips user providers entirely AND pins
	// an `enabled_providers` allowlist of just the model-proxy backends
	// (nim-proxy, zen-proxy, etc.) — the allowlist is what actually keeps
	// the user's providers out, because opencode merges the global config
	// in regardless of what the per-ship file omits.
	ProviderMode string `yaml:"provider_mode"`
}

// WebConfig holds web server configuration.
type WebConfig struct {
	ListenAddr       string `yaml:"listen_addr"`
	AutostartEnabled bool   `yaml:"autostart_enabled"`
	PIDFile          string `yaml:"pid_file"`
	LogFile          string `yaml:"log_file"`
	// ShipID is the fleet identity (ship name) under which the web frontend
	// appears on the agent bus. When empty, the bus identity is taken from the
	// environment (STARFLEET_SHIP_ID) like `comms` does.
	ShipID string `yaml:"ship_id"`
	// ShipHandle is the optional human-readable handle shown alongside ShipID.
	ShipHandle string `yaml:"ship_handle"`

	// Terminal configuration for termctl terminals spawned by this server.
	TerminalRows       int `yaml:"terminal_rows"`
	TerminalCols       int `yaml:"terminal_cols"`
	TerminalScrollback int `yaml:"terminal_scrollback"`
}

// CommsConfig holds comms / opencode plugin tuning knobs.
type CommsConfig struct {
	HeartbeatMS     int    `yaml:"heartbeat_ms"`
	PollMS          int    `yaml:"poll_ms"`
	FallbackModel   string `yaml:"fallback_model"`
	RetryPollMS     int    `yaml:"retry_poll_ms"`
	RetryCooldownMS int    `yaml:"retry_cooldown_ms"`
	LogPollMS       int    `yaml:"log_poll_ms"`
	LogCooldownMS   int    `yaml:"log_cooldown_ms"`
}

// DefaultConfig returns defaults.
func DefaultConfig() *Config {
	return &Config{
		Web: WebConfig{
			ListenAddr:         "0.0.0.0:8080",
			AutostartEnabled:   false,
			PIDFile:            ".starfleet-ai/var/web.pid",
			LogFile:            ".starfleet-ai/var/log/web.log",
			TerminalRows:       60,
			TerminalCols:       120,
			TerminalScrollback: 10000,
		},
		Comms: CommsConfig{
			HeartbeatMS:     300_000,
			PollMS:          3_000,
			RetryPollMS:     2_000,
			RetryCooldownMS: 10_000,
			LogPollMS:       10_000,
			LogCooldownMS:   10_000,
		},
		Fleet: FleetConfig{
			ProviderMode: "all",
		},
		ModelProxy: ModelProxyConfig{
			ListenAddr: "127.0.0.1:8443",
			PIDFile:    ".starfleet-ai/var/model-proxy.pid",
			LogFile:    ".starfleet-ai/var/log/model-proxy.log",
		},
	}
}

// WorkDir returns the root of all ephemeral runtime state. Override via
// MPBT_WORK_DIR; default is .starfleet-ai/var/ under the workspace root.
func WorkDir(root string) string {
	if d := os.Getenv("MPBT_WORK_DIR"); d != "" {
		return d
	}
	return filepath.Join(root, ".starfleet-ai", "var")
}

// BusDir returns the comms directory under WorkDir.
func BusDir(root string) string {
	return filepath.Join(WorkDir(root), "comms")
}

// LogDir returns the centralised log directory under WorkDir.
func LogDir(root string) string {
	return filepath.Join(WorkDir(root), "log")
}

// Load reads configuration from .starfleet-ai/conf/web.yaml and
// .starfleet-ai/conf/comms.yaml. Missing files are OK (defaults apply).
// Each YAML file wraps its content under a top-level key (web:, comms:),
// so we unmarshal into a node map to extract the inner config.
func Load(root string) (*Config, error) {
	cfg := DefaultConfig()

	for _, f := range []struct {
		file string
		key  string
		dst  interface{}
	}{
		{"web.yaml", "web", &cfg.Web},
		{"comms.yaml", "comms", &cfg.Comms},
		{"fleet.yaml", "fleet", &cfg.Fleet},
		{"model-proxy.yaml", "model_proxy", &cfg.ModelProxy},
	} {
		path := filepath.Join(root, ".starfleet-ai", "conf", f.file)
		data, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		var raw map[string]yaml.Node
		if err := yaml.Unmarshal(data, &raw); err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
		if node, ok := raw[f.key]; ok {
			if err := node.Decode(f.dst); err != nil {
				return nil, fmt.Errorf("parse %s %s: %w", path, f.key, err)
			}
		}
	}

	return cfg, nil
}

// WebAddr returns the resolved web address (config + env override).
func WebAddr(root string) (string, error) {
	if env := os.Getenv("STARFLEET_WEB_ADDR"); env != "" {
		return env, nil
	}
	cfg, err := Load(root)
	if err != nil {
		return "", err
	}
	return cfg.Web.ListenAddr, nil
}
