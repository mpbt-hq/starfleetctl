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

// EnsureBootstrapped ensures the auto-generated index file
// (.starfleet-ai/var/agents.d/index.md) exists. Creates it with a bare
// header if absent.
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
	return a.DoReindex()
}

// DoNew scaffolds a new fragment file with frontmatter, refusing to clobber
// an existing one, then reindexes (and bootstraps the index first if this is
// the very first fragment ever created). Slugs may contain "/" to place the
// fragment in a subdirectory (e.g. "starfleet/my-topic").
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
	if err := a.DoReindex(); err != nil {
		return err
	}
	fmt.Println(path)
	return nil
}

// DoReindex regenerates the fragment index (.starfleet-ai/agents.d/index.md)
// and CLAUDE.md with every fragment's body inlined (frontmatter stripped).
// Pure function of the current fragment set — two ships racing a reindex
// converge to the same byte-identical output. Bootstraps the index first
// if it doesn't exist yet.
func (a *Agents) DoReindex() error {
	if _, err := a.EnsureBootstrapped(); err != nil {
		return err
	}
	metas, err := a.loadAllFragments()
	if err != nil {
		return err
	}

	var b strings.Builder
	b.WriteString(indexHeader)

	for _, m := range metas {
		data, rerr := os.ReadFile(a.fragmentPath(m.Slug))
		if rerr != nil {
			return rerr
		}
		fmt.Fprintf(&b, "\n<!-- begin inlined fragment: %s -->\n", m.Slug)
		if _, body, err := parseFragmentFile(data); err != nil {
			fmt.Fprintf(os.Stderr, "warning: %s: no frontmatter, inlining raw content\n", m.Slug)
			b.WriteString(string(data))
		} else {
			b.WriteString(body)
		}
		fmt.Fprintf(&b, "\n<!-- end inlined fragment: %s -->\n", m.Slug)
	}
	indexContent := b.String()

	if err := os.WriteFile(a.IndexFile(), []byte(indexContent), 0o644); err != nil {
		return err
	}

	// CLAUDE.md is the entry point for agents that don't resolve @-imports
	// (e.g. opencode). Write the full inlined content directly into it.
	claudePath := filepath.Join(a.Root, "CLAUDE.md")
	claude := claudeHeader + indexContent
	if err := os.WriteFile(claudePath, []byte(claude), 0o644); err != nil {
		return err
	}

	return nil
}

// DoCommit stages, commits, and (unless push is false) pushes ONE fragment
// file (or, with slug == "", both CLAUDE.md and the index file).
// Same shared clone lock as every other Go git-mutating command here.
func (a *Agents) DoCommit(slug, msg string, push bool) error {
	lh, err := a.lock()
	if err != nil {
		return err
	}
	defer lh.Close()

	var paths []string
	if slug == "" {
		paths = []string{"CLAUDE.md", a.IndexFile()}
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
