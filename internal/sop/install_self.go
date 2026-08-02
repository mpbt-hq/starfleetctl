// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
//
// DoInstallSelf is the mechanism behind the "starfleetctl carries its own
// instructions" design (praetor directive m0089, 2026-07-06): a consuming
// workspace's CLAUDE.md should only need to know how to fetch/build
// starfleetctl and how to pull the actual usage instructions FROM it — not
// hand-duplicate and separately maintain a copy of them. See the root
// package doc comment (doc.go) for the embedding mechanism.
//
// The consolidated starfleet skill lives at .claude/skills/starfleet/
// and is installed via DoInstallStarfleetSkills.
package sop

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

// DoInstallSelf installs the consolidated starfleet skill to
// .claude/skills/starfleet/ (via DoInstallStarfleetSkills) and cleans up
// the legacy starfleet/starfleetctl.md fragment if present (both the old
// agents.d/ and the current sop.d/ locations).
func (s *SOP) DoInstallSelf(order int) error {
	// Install skills (the new home for starfleetctl instructions)
	if err := s.DoInstallStarfleetSkills(); err != nil {
		return fmt.Errorf("install starfleet skills: %w", err)
	}
	// Clean up legacy fragments if present
	removed := false
	for _, dir := range []string{s.FragmentsDir(), filepath.Join(s.Root, "agents.d")} {
		legacyPath := filepath.Join(dir, SelfSlug+".md")
		if _, err := os.Stat(legacyPath); err == nil {
			os.Remove(legacyPath)
			removed = true
		}
	}
	if removed {
		return s.DoReindex()
	}
	return nil
}

// StarfleetSubdir is the subdirectory inside fragments/ that holds the
// generic starfleet-wide instruction fragments (always-loaded).
const StarfleetSubdir = "starfleet-instructions"

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
// fragments/<subdir>/ directory into .starfleet-ai/var/sop.d/<slug>.md, always
// overwriting existing files (they are tool-owned). Then reindexes.
// Used by both the CLI command and genesis-init.
func (s *SOP) DoInstallStarfleet(subdir string) error {
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
		path := s.fragmentPath(meta.Slug)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := writeFragmentFile(path, meta, body); err != nil {
			return err
		}
	}
	return s.DoReindex()
}

// StarfleetSkillsSubdir is the subdirectory inside fragments/ that holds
// the starfleet skill files (SKILL.md + reference.md).
const StarfleetSkillsSubdir = "starfleet-skills"

// SkillsDir returns the absolute path to .claude/skills/ in the workspace.
func (s *SOP) SkillsDir() string {
	return filepath.Join(s.Root, ".claude", "skills")
}

// oldStarfleetSkillDirs lists legacy skill directory names that should be
// cleaned up when installing the consolidated starfleet skill.
var oldStarfleetSkillDirs = []string{"concurrency", "starfleetctl", "task-capture"}

// DoInstallStarfleetSkills installs the single starfleet skill from the
// embedded fragments/starfleet-skills/starfleet/ to .claude/skills/starfleet/,
// always overwriting (tool-owned). Also cleans up legacy skill directories.
func (s *SOP) DoInstallStarfleetSkills() error {
	skillName := "starfleet"
	skillDir := filepath.Join(starfleetctl.FragmentsRoot, StarfleetSkillsSubdir, skillName)
	skillEntries, err := fs.ReadDir(starfleetctl.Fragments, skillDir)
	if err != nil {
		return err
	}
	destDir := filepath.Join(s.SkillsDir(), skillName)
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

	// Clean up legacy skill directories (concurrency, starfleetctl, task-capture)
	for _, old := range oldStarfleetSkillDirs {
		os.RemoveAll(filepath.Join(s.SkillsDir(), old))
	}

	return nil
}
