// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package projectconfig

import (
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// ProjectConfig holds project-specific configuration for starfleetctl
type ProjectConfig struct {
	// Name is a human-readable project name
	Name string `yaml:"name"`

	// WorktreeLayout describes how release worktrees are organized
	WorktreeLayout WorktreeLayout `yaml:"worktree_layout"`

	// PathRemapping describes path transformations between branches
	PathRemapping PathRemapping `yaml:"path_remapping"`

	// ReleaseLines are the maintained release lines for backporting
	ReleaseLines []string `yaml:"release_lines"`

	// Solutions maps release line names to solution-specific paths
	Solutions map[string]SolutionConfig `yaml:"solutions"`
}

// WorktreeLayout describes the directory structure for release worktrees
type WorktreeLayout struct {
	// BaseDir is the base directory under _WORK_ (e.g., "xserver-{rel}")
	BaseDir string `yaml:"base_dir"`

	// SourcesSubdir is the path to the source tree within the worktree
	SourcesSubdir string `yaml:"sources_subdir"`

	// AgentSubdir is the path to agent clones within the worktree
	AgentSubdir string `yaml:"agent_subdir"`
}

// PathRemapping describes how paths transform between branches
type PathRemapping struct {
	// Prefix is the prefix to toggle (e.g., "Xext/")
	Prefix string `yaml:"prefix"`

	// Enabled whether path remapping is enabled
	Enabled bool `yaml:"enabled"`
}

// SolutionConfig holds solution-specific configuration
type SolutionConfig struct {
	// ConfigPath is the path to the solution's config.sh
	ConfigPath string `yaml:"config_path"`

	// ReleasePrefix is the prefix for release names (e.g., "xserver-")
	ReleasePrefix string `yaml:"release_prefix"`

	// EnvVar is the environment variable that holds the release (e.g., "MY_PROJECT_RELEASE")
	EnvVar string `yaml:"env_var"`
}

// DefaultProjectConfig returns the default project configuration (generic, no project-specifics)
func DefaultProjectConfig() *ProjectConfig {
	return &ProjectConfig{
		Name: "generic",
		WorktreeLayout: WorktreeLayout{
			BaseDir:       "{project}-{rel}",
			SourcesSubdir: "sources/{project}",
			AgentSubdir:   "agent/{name}/{project}",
		},
		PathRemapping: PathRemapping{
			Prefix:  "Xext/",
			Enabled: false,
		},
		ReleaseLines: []string{},
		Solutions:    map[string]SolutionConfig{},
	}
}

// Load loads project configuration from .starfleet-ai/conf/project.yaml
func Load(root string) (*ProjectConfig, error) {
	path := filepath.Join(root, ".starfleet-ai", "conf", "project.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return DefaultProjectConfig(), nil
		}
		return nil, err
	}

	var cfg ProjectConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}

	// Merge with defaults for any missing fields
	def := DefaultProjectConfig()
	if cfg.WorktreeLayout.BaseDir == "" {
		cfg.WorktreeLayout.BaseDir = def.WorktreeLayout.BaseDir
	}
	if cfg.WorktreeLayout.SourcesSubdir == "" {
		cfg.WorktreeLayout.SourcesSubdir = def.WorktreeLayout.SourcesSubdir
	}
	if cfg.WorktreeLayout.AgentSubdir == "" {
		cfg.WorktreeLayout.AgentSubdir = def.WorktreeLayout.AgentSubdir
	}
	if cfg.PathRemapping.Prefix == "" {
		cfg.PathRemapping.Prefix = def.PathRemapping.Prefix
	}

	return &cfg, nil
}

// RefDir returns the reference clone directory for a release
func (c *ProjectConfig) RefDir(root, rel string) string {
	baseDir := c.WorktreeLayout.BaseDir
	baseDir = expandTemplate(baseDir, map[string]string{
		"project": c.projectName(),
		"rel":     rel,
	})
	return filepath.Join(root, "_WORK_", baseDir, c.WorktreeLayout.SourcesSubdir)
}

// AgentDir returns the agent clone directory for a release and agent name
func (c *ProjectConfig) AgentDir(root, rel, name string) string {
	baseDir := c.WorktreeLayout.BaseDir
	baseDir = expandTemplate(baseDir, map[string]string{
		"project": c.projectName(),
		"rel":     rel,
	})
	agentSubdir := c.WorktreeLayout.AgentSubdir
	agentSubdir = expandTemplate(agentSubdir, map[string]string{
		"project": c.projectName(),
		"name":    name,
	})
	return filepath.Join(root, "_WORK_", baseDir, agentSubdir)
}

// projectName returns a normalized project name for path templates
func (c *ProjectConfig) projectName() string {
	// Default to "xserver" for backwards compatibility with xserver projects
	if c.Name == "xlibre-xserver" || c.Name == "xserver" {
		return "xserver"
	}
	return c.Name
}

// RemapPath applies path remapping if enabled
func (c *ProjectConfig) RemapPath(path string) string {
	if !c.PathRemapping.Enabled || c.PathRemapping.Prefix == "" {
		return path
	}
	prefix := c.PathRemapping.Prefix
	if len(path) >= len(prefix) && path[:len(prefix)] == prefix {
		return path[len(prefix):]
	}
	return prefix + path
}

// GetRemapCandidates returns both path variants for path remapping
func (c *ProjectConfig) GetRemapCandidates(pathIn string) []string {
	if !c.PathRemapping.Enabled || c.PathRemapping.Prefix == "" {
		return []string{pathIn}
	}
	candidates := []string{pathIn}
	prefix := c.PathRemapping.Prefix
	if len(pathIn) >= len(prefix) && pathIn[:len(prefix)] == prefix {
		candidates = append(candidates, pathIn[len(prefix):])
	} else {
		candidates = append(candidates, prefix+pathIn)
	}
	return candidates
}

// GetReleaseLines returns the release lines, with defaults if not configured
func (c *ProjectConfig) GetReleaseLines() []string {
	if len(c.ReleaseLines) > 0 {
		return c.ReleaseLines
	}
	// Default release lines (xserver-specific, kept for backwards compat)
	return []string{"25.2", "25.1", "25.0"}
}

// GetSolution returns the solution config for a release, if any
func (c *ProjectConfig) GetSolution(rel string) (SolutionConfig, bool) {
	// Try exact match first
	if sol, ok := c.Solutions[rel]; ok {
		return sol, true
	}
	// Try with project prefix
	prefixed := c.projectName() + "-" + rel
	if sol, ok := c.Solutions[prefixed]; ok {
		return sol, true
	}
	return SolutionConfig{}, false
}

// expandTemplate expands a template string with the given variables
func expandTemplate(tmpl string, vars map[string]string) string {
	result := tmpl
	for k, v := range vars {
		result = filepath.Join(filepath.SplitList(result)...) // normalize
		// Simple template replacement
		result = expandVar(result, "{"+k+"}", v)
	}
	return result
}

func expandVar(str, placeholder, value string) string {
	// Simple string replacement for template variables
	for i := 0; i < len(str); i++ {
		if i+len(placeholder) <= len(str) && str[i:i+len(placeholder)] == placeholder {
			return str[:i] + value + str[i+len(placeholder):]
		}
	}
	return str
}
