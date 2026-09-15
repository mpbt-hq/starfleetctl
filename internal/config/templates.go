// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// ShipTemplate represents a ship class template.
type ShipTemplate struct {
	Name          string   `yaml:"name"`
	Description   string   `yaml:"description"`
	Model         string   `yaml:"model"`
	SessionType   string   `yaml:"session_type"`
	NamePrefix    string   `yaml:"name_prefix"`
	SkillsAlways  []string `yaml:"skills_always"`
	SkillOptional string   `yaml:"skill_optional"`
	Timeout       string   `yaml:"timeout"`
	AutoCleanup   bool     `yaml:"auto_cleanup"`
}

// ShipTemplatesConfig is the root config for ship templates.
type ShipTemplatesConfig struct {
	Templates []ShipTemplate `yaml:"templates"`
}

// LoadShipTemplates loads ship templates from the workspace config directory.
func LoadShipTemplates(root string) (*ShipTemplatesConfig, error) {
	configPath := filepath.Join(root, ".starfleet-ai", "conf", "templates", "templates.yaml")
	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			// Return empty config if file doesn't exist
			return &ShipTemplatesConfig{Templates: []ShipTemplate{}}, nil
		}
		return nil, fmt.Errorf("read templates config: %w", err)
	}

	var cfg ShipTemplatesConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse templates config: %w", err)
	}
	return &cfg, nil
}

// GetTemplate returns a template by name.
func (c *ShipTemplatesConfig) GetTemplate(name string) (*ShipTemplate, error) {
	for _, t := range c.Templates {
		if t.Name == name {
			return &t, nil
		}
	}
	return nil, fmt.Errorf("template not found: %s", name)
}

// ListTemplateNames returns all template names.
func (c *ShipTemplatesConfig) ListTemplateNames() []string {
	names := make([]string, 0, len(c.Templates))
	for _, t := range c.Templates {
		names = append(names, t.Name)
	}
	return names
}

// ApplyToLaunchOpts applies template values to LaunchShipOpts, filling in missing values.
func (t *ShipTemplate) ApplyToLaunchOpts(opts *LaunchShipOpts) {
	if opts.Model == "" && t.Model != "" {
		opts.Model = t.Model
	}
	if opts.LaunchType == "" && t.SessionType != "" {
		opts.LaunchType = t.SessionType
	}
	if opts.Class == "" {
		opts.Class = t.Name
	}
	// Note: Name, Parent, Unrestricted, ExtraArgs, Mode are not set from template
	// as they are typically specified per-launch.
}

// LaunchShipOpts from session package (redefined here to avoid import cycle)
type LaunchShipOpts struct {
	Name         string
	Model        string
	Class        string
	Provider     string
	Parent       string
	LaunchType   string
	Unrestricted bool
	ExtraArgs    []string
	Mode         string
}
