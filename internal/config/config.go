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
	Web      WebConfig      `yaml:"web"`
	AgentBus AgentBusConfig `yaml:"agent_bus"`
	Fleet    FleetConfig    `yaml:"fleet"`
}

// FleetConfig holds fleet-wide identity settings.
type FleetConfig struct {
	// Flagship is the canonical name of the flagship/control session.
	// Defaults to "Enterprise" when unset.
	Flagship string `yaml:"flagship"`
	// ShipNames is the worker ship-name pool. When empty, the compiled-in
	// Star Trek ship roster is used. The flagship name is always excluded.
	ShipNames []string `yaml:"ship_names"`
}

// WebConfig holds web server configuration.
type WebConfig struct {
	ListenAddr       string `yaml:"listen_addr"`
	AutostartEnabled bool   `yaml:"autostart_enabled"`
	PIDFile          string `yaml:"pid_file"`
	LogFile          string `yaml:"log_file"`
	// ShipID is the fleet identity (ship name) under which the web frontend
	// appears on the agent bus. When empty, the bus identity is taken from the
	// environment (STARFLEET_SHIP_ID) like `agent-bus` does.
	ShipID string `yaml:"ship_id"`
	// ShipHandle is the optional human-readable handle shown alongside ShipID.
	ShipHandle string `yaml:"ship_handle"`
}

// AgentBusConfig holds agent-bus / opencode plugin tuning knobs.
type AgentBusConfig struct {
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
			ListenAddr:       "0.0.0.0:8080",
			AutostartEnabled: false,
			PIDFile:          ".starfleet-ai/var/web.pid",
			LogFile:          ".starfleet-ai/var/log/web.log",
		},
		AgentBus: AgentBusConfig{
			HeartbeatMS:     300_000,
			PollMS:          3_000,
			RetryPollMS:     2_000,
			RetryCooldownMS: 10_000,
			LogPollMS:       10_000,
			LogCooldownMS:   10_000,
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

// BusDir returns the agent-bus directory under WorkDir.
func BusDir(root string) string {
	return filepath.Join(WorkDir(root), "agent-bus")
}

// LogDir returns the centralised log directory under WorkDir.
func LogDir(root string) string {
	return filepath.Join(WorkDir(root), "log")
}

// Load reads configuration from .starfleet-ai/conf/web.yaml and
// .starfleet-ai/conf/agent-bus.yaml. Missing files are OK (defaults apply).
// Each YAML file wraps its content under a top-level key (web:, agent_bus:),
// so we unmarshal into a node map to extract the inner config.
func Load(root string) (*Config, error) {
	cfg := DefaultConfig()

	for _, f := range []struct {
		file string
		key  string
		dst  interface{}
	}{
		{"web.yaml", "web", &cfg.Web},
		{"agent-bus.yaml", "agent_bus", &cfg.AgentBus},
		{"fleet.yaml", "fleet", &cfg.Fleet},
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
