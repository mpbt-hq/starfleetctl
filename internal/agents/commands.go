// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package agents

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// inlineMarker is the path of a marker file that, when present, selects
// inline mode for the generated .starfleet-ai/agents.d/index.md (see DoReindex). Inline mode drops
// the `@.starfleet-ai/agents.d/index.md` import entirely and writes the full fragment set
// straight into .starfleet-ai/agents.d/index.md — some agents (opencode) do not resolve `@`-imports,
// so a self-contained file is the only way they receive the instructions.
func (a *Agents) inlineMarker() string {
	return filepath.Join(a.Root, ".starfleet-ai", "agents-inline")
}

// Inline reports whether reindex should produce a self-contained (inline)
// .starfleet-ai/agents.d/index.md. Driven by the persistent .starfleet-ai/agents-inline marker, so
// `new`/`write` (which call DoReindex) keep the workspace's chosen mode.
func (a *Agents) Inline() bool {
	_, err := os.Stat(a.inlineMarker())
	return err == nil
}

// SetInline turns inline mode on (token != "") or off (token == ""), by
// creating or removing the marker file. Reindex must be run afterwards to
// rewrite .starfleet-ai/agents.d/index.md in the new mode.
func (a *Agents) SetInline(on bool) error {
	mk := a.inlineMarker()
	if on {
		if err := os.MkdirAll(filepath.Dir(mk), 0o755); err != nil {
			return err
		}
		return os.WriteFile(mk, []byte("1\n"), 0o644)
	}
	if err := os.Remove(mk); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// EnsureBootstrapped ensures the root .starfleet-ai/agents.d/index.md exists and contains the
// starfleet fragment pointer, and that .starfleet-ai/agents.d/index.md exists.
//
// Two cases:
//   - .starfleet-ai/agents.d/index.md exists: insert the @-pointer if missing (idempotent — never
//     overwrites existing content).
//   - .starfleet-ai/agents.d/index.md absent: create the auto-generated file and add .starfleet-ai/agents.d/index.md to
//     .gitignore (the file is a generated artifact, not user content).
func (a *Agents) EnsureBootstrapped() (created bool, err error) {
	if err := os.MkdirAll(filepath.Dir(a.IndexFile()), 0o755); err != nil {
		return false, err
	}
	if _, err := os.Stat(a.IndexFile()); err != nil {
		if err := os.WriteFile(a.IndexFile(), []byte(indexHeader), 0o644); err != nil {
			return false, err
		}
		return true, nil
	}
	return false, nil
}

// DoList prints every fragment's slug/title/order (or, with jsonOut, a JSON
// array).
func (a *Agents) DoList(jsonOut bool) error {
	metas, err := a.loadAllFragments()
	if err != nil {
		return err
	}
	if jsonOut {
		type row struct {
			Slug  string `json:"slug"`
			Title string `json:"title"`
			Order int    `json:"order"`
			Owner string `json:"owner,omitempty"`
		}
		out := make([]row, 0, len(metas))
		for _, m := range metas {
			out = append(out, row{m.Slug, m.Title, m.Order, m.Owner})
		}
		enc, err := json.Marshal(out)
		if err != nil {
			return err
		}
		fmt.Println(string(enc))
		return nil
	}
	for _, m := range metas {
		owner := m.Owner
		if owner == "" {
			owner = "-"
		}
		fmt.Printf("%-4d %-40s %-15s %s\n", m.Order, m.Slug, owner, m.Title)
	}
	return nil
}

// DoShow prints one fragment file's full content (frontmatter + body).
func (a *Agents) DoShow(slug string) error {
	data, err := os.ReadFile(a.fragmentPath(slug))
	if err != nil {
		return err
	}
	_, err = os.Stdout.Write(data)
	return err
}

// DoWrite replaces one fragment file's content (raw, frontmatter and all)
// from src ("-" for stdin), then reindexes. Does NOT commit.
func (a *Agents) DoWrite(slug, src string) error {
	var r io.Reader
	if src == "-" {
		r = os.Stdin
	} else {
		f, err := os.Open(src)
		if err != nil {
			return err
		}
		defer f.Close()
		r = f
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(a.FragmentsDir(), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(a.fragmentPath(slug), data, 0o644); err != nil {
		return err
	}
	return a.DoReindex(a.Inline())
}

// DoNew scaffolds a new fragment file with frontmatter, refusing to clobber
// an existing one, then reindexes (and bootstraps the root .starfleet-ai/agents.d/index.md/index
// first if this is the very first fragment ever created). Slugs may contain
// "/" to place the fragment in a subdirectory (e.g. "starfleet/my-topic").
func (a *Agents) DoNew(slug, title string, order int, owner string) error {
	path := a.fragmentPath(slug)
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("fragment already exists: %s", path)
	}
	if _, err := a.EnsureBootstrapped(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	m := FragmentMeta{Slug: slug, Title: title, Order: order, Owner: owner}
	if err := writeFragmentFile(path, m, "(fill in)\n"); err != nil {
		return err
	}
	if err := a.DoReindex(a.Inline()); err != nil {
		return err
	}
	fmt.Println(path)
	return nil
}

// DoReindex regenerates the fragment index (.starfleet-ai/agents.d/index.md).
// Pure function of the current fragment set — two ships racing a reindex
// converge to the same byte-identical output. Bootstraps the index first
// if it doesn't exist yet.
func (a *Agents) DoReindex(inline bool) error {
	if _, err := a.EnsureBootstrapped(); err != nil {
		return err
	}
	metas, err := a.loadAllFragments()
	if err != nil {
		return err
	}

	var b strings.Builder
	b.WriteString(indexHeader)

	if !inline {
		for _, m := range metas {
			fmt.Fprintf(&b, "@%s\n", a.fragmentImportPath(m))
		}
		if err := os.WriteFile(a.IndexFile(), []byte(b.String()), 0o644); err != nil {
			return err
		}
		return nil
	}

	// Inline mode: drop the @-imports and embed every fragment's full content
	// (frontmatter + body) into the index. Some agents (opencode) don't
	// resolve @-imports, so a self-contained file is the only way they
	// receive the instructions.
	for _, m := range metas {
		content, rerr := a.readFragmentContent(m)
		if rerr != nil {
			return rerr
		}
		fmt.Fprintf(&b, "\n<!-- begin inlined fragment: %s -->\n", m.Slug)
		b.WriteString(content)
		fmt.Fprintf(&b, "\n<!-- end inlined fragment: %s -->\n", m.Slug)
	}
	if err := os.WriteFile(a.IndexFile(), []byte(b.String()), 0o644); err != nil {
		return err
	}

	return nil
}

// readFragmentContent returns a fragment file's raw content (frontmatter +
// body) for inlining into the generated index.
func (a *Agents) readFragmentContent(m FragmentMeta) (string, error) {
	data, err := os.ReadFile(a.fragmentPath(m.Slug))
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// DoCommit stages, commits, and (unless push is false) pushes ONE fragment
// file (or, with slug == "", the .starfleet-ai/agents.d/index.md file).
// Same shared clone lock as every other Go git-mutating command here.
func (a *Agents) DoCommit(slug, msg string, push bool) error {
	lh, err := a.lock()
	if err != nil {
		return err
	}
	defer lh.Close()

	var paths []string
	if slug == "" {
		paths = []string{a.IndexFile()}
	} else {
		paths = []string{a.fragmentPath(slug)}
	}

	addArgs := append([]string{"add"}, paths...)
	if err := run(a.Root, "git", addArgs...); err != nil {
		return err
	}
	diffArgs := append([]string{"diff", "--cached", "--quiet", "--"}, paths...)
	if err := run(a.Root, "git", diffArgs...); err == nil {
		fmt.Println("agents: nothing staged — nothing to commit")
		return nil
	}
	if err := run(a.Root, "git", "commit", "-m", msg); err != nil {
		return err
	}
	if !push {
		return nil
	}
	branch, err := a.branch()
	if err != nil {
		return err
	}
	if err := run(a.Root, "git", "pull", "--rebase", "--autostash"); err != nil {
		return fmt.Errorf("agents: pull --rebase failed, NOT pushing (local state may be stale): %w", err)
	}
	return run(a.Root, "git", "push", "origin", branch)
}
