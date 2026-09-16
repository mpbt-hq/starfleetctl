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
	Services   ServicesConfig   `yaml:"services"`
}

// ServicesConfig holds daemon lifecycle configuration
// (.starfleet-ai/conf/services.yaml). It decides which companion daemons are
// pulled up automatically when the flagship (starfleetctl run --flagship)
// starts — replacing the need for hand-rolled cron autostart entries.
type ServicesConfig struct {
	// Autostart is the ordered list of service names to start automatically
	// when the flagship session boots. Supported names: web, model-proxy,
	// timer. Empty (default) means everything stays on-demand.
	Autostart []string `yaml:"autostart"`
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
	// Strategies defines meta-model routing strategies. Each strategy is
	// exposed to clients as a virtual model endpoint on the meta-model
	// provider, named by the strategy ID (no separate routing table needed).
	Strategies []ModelProxyStrategy `yaml:"strategies"`
}

// ModelProxyStrategy defines a meta-model routing strategy.
type ModelProxyStrategy struct {
	ID             string                       `yaml:"id"`
	Description    string                       `yaml:"description"`
	DefaultModel   string                       `yaml:"default_model"`
	Models         []ModelProxyStrategyModel    `yaml:"models"`
	Strategy       string                       `yaml:"strategy"`
	EffectiveLimit int                          `yaml:"-"` // computed: min(context_window)
	Triggers       ModelProxyStrategyTriggers   `yaml:"triggers"`
	Heuristics     ModelProxyStrategyHeuristics `yaml:"heuristics"`
}

// ModelProxyStrategyType defines the routing strategy type.
type ModelProxyStrategyType string

const (
	ModelProxyStrategyTypeSingle     ModelProxyStrategyType = "single"
	ModelProxyStrategyTypeFallback   ModelProxyStrategyType = "fallback"
	ModelProxyStrategyTypeRoundRobin ModelProxyStrategyType = "round-robin"
	ModelProxyStrategyTypeWeighted   ModelProxyStrategyType = "weighted"
)

// ModelProxyStrategyModel is a model entry within a strategy.
type ModelProxyStrategyModel struct {
	ID             string `yaml:"id"`
	Provider       string `yaml:"provider"`
	ContextWindow  int    `yaml:"context_window"`
	Priority       int    `yaml:"priority"`
	Weight         int    `yaml:"weight"`
	CooldownPeriod string `yaml:"cooldown_period"`
}

// ModelProxyStrategyTriggers defines trigger rules for model switching.
type ModelProxyStrategyTriggers struct {
	RateLimited struct {
		Action   string `yaml:"action"`
		Cooldown string `yaml:"cooldown"`
		After    int    `yaml:"after"`
	} `yaml:"rate-limited"`
	QuotaExhausted struct {
		Action      string `yaml:"action"`
		Cooldown    string `yaml:"cooldown"`
		AutoRecover bool   `yaml:"auto_recover"`
	} `yaml:"quota-exhausted"`
	Timeout struct {
		Action         string `yaml:"action"`
		Cooldown       string `yaml:"cooldown"`
		MaxConsecutive int    `yaml:"max_consecutive"`
	} `yaml:"timeout"`
	Error struct {
		Action   string `yaml:"action"`
		Cooldown string `yaml:"cooldown"`
	} `yaml:"error"`
}

// ModelProxyStrategyHeuristics defines heuristic thresholds for circuit breaker.
type ModelProxyStrategyHeuristics struct {
	LatencyP95Threshold          string `yaml:"latency_p95_threshold"`
	ErrorRateThreshold           string `yaml:"error_rate_threshold"`
	ErrorRateWindow              string `yaml:"error_rate_window"`
	TokenThroughputThreshold     string `yaml:"token_throughput_threshold"`
	ConsecutiveFailuresThreshold int    `yaml:"consecutive_failures_threshold"`
}

// ModelProxyProvider describes one upstream model API backend behind the
// proxy. ID is the opencode provider name exposed to ships (e.g. "nim-proxy",
// "zen-proxy"); BaseURL is the upstream OpenAI-compatible endpoint; APIKey
// supports env replacement ({env:VAR} or ${VAR}) since keys come via env.
// When Direct is true, the provider is used directly (bypassing the local
// proxy) — the generated opencode config will point to the upstream's actual
// BaseURL and use its APIKey, instead of routing through the local proxy.
type ModelProxyProvider struct {
	ID      string `yaml:"id"`
	Name    string `yaml:"name"`
	BaseURL string `yaml:"base_url"`
	APIKey  string `yaml:"api_key"`
	Direct  bool   `yaml:"direct"`
	// ModelFilter controls which models are exposed. Options:
	// - "" or "all" (default): no filtering
	// - "free-only": auto-filter free models (zen-proxy: "-free" suffix;
	//   nim-proxy: built-in known-free list)
	// - comma-separated list: explicit allowlist of model IDs
	ModelFilter string `yaml:"model_filter"`
	// MaxRetries retries a request when the upstream reports a transient
	// error (429/5xx/conn-reset). Default 3.
	MaxRetries int `yaml:"max_retries"`
	// RetryDelayMS sleeps between retries. Default 1000.
	RetryDelayMS int `yaml:"retry_delay_ms"`
	// HoldTimeoutMS is the maximum time (ms) to hold an SSE connection open
	// with keepalive comments after the retry budget is exhausted and a
	// saturation error is received. Default 15000 (15s). 0 disables keepalive.
	HoldTimeoutMS int `yaml:"hold_timeout_ms"`
	// Type selects the provider class, which drives upstream-specific
	// request handling (headers, body tweaks, ...). Empty = generic
	// OpenAI-compatible. Known types:
	//   - "opencode-zen": OpenCode Zen — gates anonymous/free capacity by
	//     validating the User-Agent, so the proxy stamps an opencode UA.
	Type string `yaml:"type"`
	// UserAgent, when set, is sent as the upstream request's User-Agent
	// header (overrides the type's default). Mainly relevant for
	// "opencode-zen", whose free tier requires an "opencode/<version>" UA.
	UserAgent string `yaml:"user_agent"`
	// Capabilities forces capability flags (toolcall, temperature,
	// reasoning, attachment, ...) onto EVERY model this provider serves, in
	// the generated per-ship opencode.json model entries — for upstreams
	// whose model catalog does not advertise them (e.g. Ollama serves a bare
	// /v1/models without any capability metadata, so opencode would treat
	// those models as tool-less). Merges with (and overrides) per-model
	// capabilities discovered from the catalog. When empty, a default set
	// for the provider's Type may apply (see the ollama type).
	Capabilities []string `yaml:"capabilities"`
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
	// DirectProviders lists providers that bypass the local model proxy
	// entirely, connecting straight to the upstream API. Used exceptionally
	// (proxy down, ZEN quota exhausted). These are merged with proxied
	// providers for ship opencode config generation and web frontend model list.
	DirectProviders []ModelProxyProvider `yaml:"direct_providers"`
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
		{"services.yaml", "services", &cfg.Services},
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

// DefaultStrategyTriggers returns default trigger configuration.
func DefaultStrategyTriggers() ModelProxyStrategyTriggers {
	var t ModelProxyStrategyTriggers
	t.RateLimited.Action = "skip"
	t.RateLimited.Cooldown = "60s"
	t.RateLimited.After = 3
	t.QuotaExhausted.Action = "skip"
	t.QuotaExhausted.Cooldown = "3600s"
	t.QuotaExhausted.AutoRecover = true
	t.Timeout.Action = "skip"
	t.Timeout.Cooldown = "30s"
	t.Timeout.MaxConsecutive = 3
	t.Error.Action = "skip"
	t.Error.Cooldown = "10s"
	return t
}

// DefaultStrategyHeuristics returns default heuristics configuration.
func DefaultStrategyHeuristics() ModelProxyStrategyHeuristics {
	var h ModelProxyStrategyHeuristics
	h.LatencyP95Threshold = "30s"
	h.ErrorRateThreshold = "20%"
	h.ErrorRateWindow = "5m"
	h.TokenThroughputThreshold = "10"
	h.ConsecutiveFailuresThreshold = 3
	return h
}

// ComputeEffectiveLimit computes the min context window across all models in the strategy.
func (s *ModelProxyStrategy) ComputeEffectiveLimit() int {
	if len(s.Models) == 0 {
		return 0
	}
	min := s.Models[0].ContextWindow
	for _, m := range s.Models[1:] {
		if m.ContextWindow > 0 && m.ContextWindow < min {
			min = m.ContextWindow
		}
	}
	return min
}

// GetModelByID returns a ModelProxyStrategyModel by its ID.
func (s *ModelProxyStrategy) GetModelByID(id string) *ModelProxyStrategyModel {
	for i := range s.Models {
		if s.Models[i].ID == id {
			return &s.Models[i]
		}
	}
	return nil
}

// GetProviderForModel returns the Provider for a given model ID in this strategy.
func (s *ModelProxyStrategy) GetProviderForModel(modelID string, providers []ModelProxyProvider) *ModelProxyProvider {
	sm := s.GetModelByID(modelID)
	if sm == nil {
		return nil
	}
	for i := range providers {
		if providers[i].ID == sm.Provider {
			return &providers[i]
		}
	}
	return nil
}
