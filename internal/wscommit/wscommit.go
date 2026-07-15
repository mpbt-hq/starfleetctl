// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
//
// Package wscommit is the Go port of scripts/ws-commit — commit (and push)
// to the workspace repo atomically under the shared clone mutex, so
// concurrent agent sessions mutating this single working tree don't race on
// the index/HEAD. It shares the SAME <gitdir>/mpbt-clone.lock file as
// scripts/with-clone-lock / scripts/ws-commit and internal/dashboard, so a
// Go and a bash actor on this clone serialize against each other instead of
// both mutating the index/HEAD at once (see CLAUDE.md "Concurrency /
// isolation"). ADVISORY ONLY, like the bash original: a raw `git commit`
// still bypasses it.
package wscommit

import (
	"fmt"
	"strings"
)

// WsCommit holds one invocation's resolved locations.
type WsCommit struct {
	Root   string // workspace root (toplevel of the git checkout)
	GitDir string // absolute .git dir, for the shared clone lock file
}

// New resolves a WsCommit rooted at the given workspace root.
func New(root string) (*WsCommit, error) {
	out, err := runCapture(root, "git", "rev-parse", "--absolute-git-dir")
	if err != nil {
		return nil, fmt.Errorf("ws-commit: not inside a git working tree: %w", err)
	}
	return &WsCommit{Root: root, GitDir: strings.TrimSpace(out)}, nil
}

func (w *WsCommit) branch() (string, error) {
	out, err := runCapture(w.Root, "git", "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}
