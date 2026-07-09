// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
//
// DoInstallSelf is the mechanism behind the "starfleetctl carries its own
// instructions" design (praetor directive m0089, 2026-07-06): a consuming
// workspace's AGENTS.md should only need to know how to fetch/build
// starfleetctl and how to pull the actual usage instructions FROM it — not
// hand-duplicate and separately maintain a copy of them. See the root
// package doc comment (doc.go) for the embedding mechanism.
package agents

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	starfleetctl "github.com/metux/starfleetctl"
)

const SelfSlug = "starfleet/starfleetctl"

func selfFragmentMeta(order int) FragmentMeta {
	return FragmentMeta{
		Slug:  SelfSlug,
		Title: "starfleetctl — fleet-management CLI (auto-installed by `agents install-self`, do not hand-edit)",
		Order: order,
		Owner: "starfleetctl",
	}
}

// RenderSelfFragment returns exactly the bytes DoInstallSelf would write,
// without touching disk — lets a caller (bootstrap's verifySelfFragment)
// check whether an existing agents.d/starfleet/starfleetctl.md is stale relative to
// the currently-running binary before deciding whether a fix is needed.
func RenderSelfFragment(order int) ([]byte, error) {
	return renderFragmentFile(selfFragmentMeta(order), starfleetctl.Readme), nil
}

// DoInstallSelf writes agents.d/starfleet/starfleetctl.md from this binary's own
// embedded README.md, then reindexes. Unlike DoNew, this ALWAYS overwrites
// — the fragment is tool-owned (Owner: "starfleetctl"), meant to always
// mirror whatever starfleetctl commit is actually checked out; hand-editing
// it is not supported (it would just be clobbered on the next
// install-self, e.g. from `bootstrap --fix` after an update).
func (a *Agents) DoInstallSelf(order int) error {
	path := a.fragmentPath(SelfSlug)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := writeFragmentFile(path, selfFragmentMeta(order), starfleetctl.Readme); err != nil {
		return err
	}
	return a.DoReindex()
}

// StarfleetSubdir is the subdirectory inside fragments/ that holds the
// generic starfleet-wide fragment files.
const StarfleetSubdir = "starfleet"

// ParseEmbeddedFragment reads a single embedded fragment file from the
// starfleetctl binary's embedded FS, parses its frontmatter, and returns
// the meta and body. slug is derived from the embedded file's relative path
// within the subdirectory.
func ParseEmbeddedFragment(fsys fs.FS, subdir, name string) (FragmentMeta, string, error) {
	data, err := fs.ReadFile(fsys, filepath.Join(subdir, name))
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
// fragments/<subdir>/ directory into agents.d/<slug>.md, always
// overwriting existing files (they are tool-owned). Then reindexes.
// Used by both the CLI command and genesis-init.
func (a *Agents) DoInstallStarfleet(subdir string) error {
	entries, err := fs.ReadDir(starfleetctl.Fragments, subdir)
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
	return a.DoReindex()
}
