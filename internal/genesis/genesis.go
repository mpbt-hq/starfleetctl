// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
//
// Package genesis is the "stand up the entire fleet from nothing" entry
// point (praetor end goal, 2026-07-06, relayed via Enterprise directives
// m0089/m0098): once *any* copy of the starfleetctl binary exists — however
// it got built — `genesis-init` writes the small, project-independent set
// of files a consuming workspace needs to fetch/build/reference starfleetctl
// itself through the normal mpbt-solution path (matching go-x11proto's and
// flyingtux's own pattern), then hands off to `bootstrap --fix` for
// everything else (AGENTS.md/agents.d, DASHBOARD.md, allowlist entries,
// _WORK_ dirs, the self-documenting fragment). None of the embedded
// templates below reference any project-specific detail (no XLibre/xserver
// literal anywhere) — that's what makes this genuinely copy-once,
// run-anywhere, unlike mpbt-workspace's own hand-authored cf/starfleetctl/*
// files this package's templates are kept in sync with.
package genesis

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/metux/starfleetctl/internal/bootstrap"
)

//go:embed all:templates
var templates embed.FS

const templatesRoot = "templates"

// execTemplates are the embedded paths (relative to templatesRoot) that must
// land on disk as executable — everything else is written 0o644.
var execTemplates = map[string]bool{
	"run-fetch.starfleetctl": true,
	"run-build.starfleetctl": true,
	"scripts/starfleetctl":   true,
}

// Init writes every template file into root that isn't already present
// (never overwrites — a genesis-init re-run against a partially-set-up
// checkout must be a safe no-op, same idempotence contract as `bootstrap`
// itself), then runs the equivalent of `bootstrap --fix` to handle
// everything else. Returns the list of files it actually created, for the
// caller to report.
func Init(root string) (created []string, err error) {
	err = fs.WalkDir(templates, templatesRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(templatesRoot, path)
		if err != nil {
			return err
		}
		dest := filepath.Join(root, rel)
		if _, statErr := os.Stat(dest); statErr == nil {
			return nil // already present — never clobber
		}
		data, err := templates.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		mode := os.FileMode(0o644)
		if execTemplates[filepath.ToSlash(rel)] {
			mode = 0o755
		}
		if err := os.WriteFile(dest, data, mode); err != nil {
			return err
		}
		created = append(created, rel)
		return nil
	})
	if err != nil {
		return created, err
	}

	b := bootstrap.New(root)
	for _, c := range bootstrap.Checks() {
		ok, _ := c.Verify(b)
		if ok || c.Fix == nil {
			continue
		}
		if err := c.Fix(b); err != nil {
			return created, fmt.Errorf("bootstrap fix %q: %w", c.Name, err)
		}
	}
	return created, nil
}
