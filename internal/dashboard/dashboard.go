// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
//
// Package dashboard is the Go port of scripts/dashboard — the safe
// read/write/commit/push cycle for DASHBOARD.md, the cross-session "what's
// in flight / what got parked" index (see mpbt-workspace's AGENTS.md
// "Working practices"). It shells out to git exactly like the bash original
// (pull --rebase --autostash, add, commit, push) and shares the SAME
// per-worktree lock file as scripts/with-clone-lock / scripts/ws-commit
// (<gitdir>/mpbt-clone.lock), so a Go and a bash actor mutating the same
// clone at once still serialize against each other instead of racing the
// index/HEAD.
package dashboard

import (
	"fmt"
	"path/filepath"
	"strings"
)

// Dashboard holds one invocation's resolved locations.
type Dashboard struct {
	Root   string // workspace root (toplevel of the git checkout)
	GitDir string // absolute .git dir, for the shared clone lock file
	File   string // absolute path to DASHBOARD.md
}

// New resolves a Dashboard rooted at the given workspace root.
func New(root string) (*Dashboard, error) {
	out, err := runCapture(root, "git", "rev-parse", "--absolute-git-dir")
	if err != nil {
		return nil, fmt.Errorf("dashboard: not inside a git working tree: %w", err)
	}
	return &Dashboard{
		Root:   root,
		GitDir: strings.TrimSpace(out),
		File:   filepath.Join(root, "DASHBOARD.md"),
	}, nil
}

func (d *Dashboard) branch() (string, error) {
	out, err := runCapture(d.Root, "git", "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}
