// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
//
// DoInstallSelf is the mechanism behind the "starfleetctl carries its own
// instructions" design (praetor directive m0089, 2026-07-06): a consuming
// workspace's AGENTS.md should only need to know how to fetch/build
// starfleetctl and how to pull the actual usage instructions FROM it — not
// hand-duplicate and separately maintain a copy of them. See the root
// package doc comment (doc.go) for the embedding mechanism.
//
// Since the skills restructuring, install-self installs the starfleetctl
// skill to .claude/skills/starfleetctl/ (via DoInstallStarfleetSkills)
// instead of writing an always-loaded fragment to agents.d/. This keeps
// the base context lean — the skill is loaded on-demand when needed.
package agents

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	starfleetctl "github.com/metux/starfleetctl"
)

// SelfSlug is the legacy agents.d slug for the starfleetctl fragment.
// Kept for backward-compatible cleanup (removing stale agents.d files).
const SelfSlug = "starfleet/starfleetctl"

// DoInstallSelf installs the starfleetctl skill to .claude/skills/starfleetctl/
// and cleans up the legacy agents.d/starfleet/starfleetctl.md fragment if present.
// This is now equivalent to DoInstallStarfleetSkills — kept as a separate entry
// point for backward compatibility with existing bootstrap scripts and CLI usage.
func (a *Agents) DoInstallSelf(order int) error {
	// Install skills (the new home for starfleetctl instructions)
	if err := a.DoInstallStarfleetSkills(); err != nil {
		return fmt.Errorf("install starfleet skills: %w", err)
	}
	// Clean up legacy agents.d fragment if present
	legacyPath := filepath.Join(a.FragmentsDir(), SelfSlug+".md")
	if _, err := os.Stat(legacyPath); err == nil {
		os.Remove(legacyPath)
		return a.DoReindex(a.Inline())
	}
	return nil
}

// StarfleetSubdir is the subdirectory inside fragments/ that holds the
// generic starfleet-wide fragment files.
const StarfleetSubdir = "starfleet"

// ParseEmbeddedFragment reads a single embedded fragment file from the
// starfleetctl binary's embedded FS, parses its frontmatter, and returns
// the meta and body. slug is derived from the embedded file's relative path
// within the subdirectory.
func ParseEmbeddedFragment(fsys fs.FS, subdir, name string) (FragmentMeta, string, error) {
	data, err := fs.ReadFile(fsys, filepath.Join(starfleetctl.FragmentsRoot, subdir, name))
	if err != nil {
		return FragmentMeta{}, "", err
	}
	m, body, err := parseFragmentFile(data)
	if err != nil {
		return FragmentMeta{}, "", fmt.Errorf("%s: %w", name, err)
	}
	if m.Slug == "" {
		m.Slug = subdir + "/" + strings.TrimSuffix(name, ".md")
	}
	return m, body, nil
}

// RenderStarfleetFragment returns exactly the bytes DoInstallStarfleet would
// write for a given embedded fragment, without touching disk — lets bootstrap
// verify fragments without I/O.
func RenderStarfleetFragment(subdir, name string) ([]byte, error) {
	m, body, err := ParseEmbeddedFragment(starfleetctl.Fragments, subdir, name)
	if err != nil {
		return nil, err
	}
	return renderFragmentFile(m, body), nil
}

// DoInstallStarfleet installs every .md file from the embedded
// fragments/<subdir>/ directory into .starfleet-ai/agents.d/<slug>.md, always
// overwriting existing files (they are tool-owned). Then reindexes.
// Used by both the CLI command and genesis-init.
func (a *Agents) DoInstallStarfleet(subdir string) error {
	entries, err := fs.ReadDir(starfleetctl.Fragments, filepath.Join(starfleetctl.FragmentsRoot, subdir))
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		meta, body, err := ParseEmbeddedFragment(starfleetctl.Fragments, subdir, e.Name())
		if err != nil {
			return err
		}
		path := a.fragmentPath(meta.Slug)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := writeFragmentFile(path, meta, body); err != nil {
			return err
		}
	}
	return a.DoReindex(a.Inline())
}

// StarfleetSkillsSubdir is the subdirectory inside fragments/ that holds
// the starfleet-wide skill files (SKILL.md + reference.md per skill).
const StarfleetSkillsSubdir = "starfleet-skills"

// SkillsDir returns the absolute path to .claude/skills/ in the workspace.
func (a *Agents) SkillsDir() string {
	return filepath.Join(a.Root, ".claude", "skills")
}

// DoInstallStarfleetSkills installs every subdirectory under the embedded
// fragments/starfleet-skills/ as a skill in .claude/skills/<name>/,
// always overwriting (tool-owned). Each subdirectory must contain at
// least a SKILL.md; reference.md is optional.
func (a *Agents) DoInstallStarfleetSkills() error {
	skillsDir := filepath.Join(starfleetctl.FragmentsRoot, StarfleetSkillsSubdir)
	entries, err := fs.ReadDir(starfleetctl.Fragments, skillsDir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		skillName := e.Name()
		skillDir := filepath.Join(skillsDir, skillName)
		skillEntries, err := fs.ReadDir(starfleetctl.Fragments, skillDir)
		if err != nil {
			return err
		}
		destDir := filepath.Join(a.SkillsDir(), skillName)
		if err := os.MkdirAll(destDir, 0o755); err != nil {
			return err
		}
		for _, f := range skillEntries {
			if f.IsDir() || !strings.HasSuffix(f.Name(), ".md") {
				continue
			}
			data, err := fs.ReadFile(starfleetctl.Fragments, filepath.Join(skillDir, f.Name()))
			if err != nil {
				return err
			}
			destPath := filepath.Join(destDir, f.Name())
			if err := os.WriteFile(destPath, data, 0o644); err != nil {
				return err
			}
		}
	}
	return nil
}
