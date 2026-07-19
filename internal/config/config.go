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
	Web WebConfig `yaml:"web"`
}

// WebConfig holds web server configuration.
type WebConfig struct {
	ListenAddr      string `yaml:"listen_addr"`
	AutostartEnabled bool   `yaml:"autostart_enabled"`
	PIDFile         string `yaml:"pid_file"`
	LogFile         string `yaml:"log_file"`
	// ShipID is the fleet identity (ship name) under which the web frontend
	// appears on the agent bus. When empty, the bus identity is taken from the
	// environment (STARFLEET_SHIP_ID / AGENT_ID) like `agent-bus` does.
	ShipID string `yaml:"ship_id"`
	// ShipHandle is the optional human-readable handle shown alongside ShipID.
	ShipHandle string `yaml:"ship_handle"`
}

// DefaultConfig returns defaults.
func DefaultConfig() *Config {
	return &Config{
		Web: WebConfig{
			ListenAddr:      "0.0.0.0:8080",
			AutostartEnabled: false,
			PIDFile:         ".starfleet-ai/var/web.pid",
			LogFile:         ".starfleet-ai/logs/web.log",
		},
	}
}

// Load reads configuration from .starfleet-ai/conf/web.yaml.
func Load(root string) (*Config, error) {
	cfg := DefaultConfig()

	path := filepath.Join(root, ".starfleet-ai", "conf", "web.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil // no config file, use defaults
		}
		return nil, err
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
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