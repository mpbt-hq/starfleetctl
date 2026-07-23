// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package agents

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// FragmentMeta is one agents.d/<slug>.md file's frontmatter.
type FragmentMeta struct {
	Slug        string
	Title       string
	Order       int    // controls .starfleet-ai/agents.d/index.md's import order
	Owner       string // optional: which tool/component maintains this fragment
	IsStarfleet bool   // true if fragment lives under .starfleet-ai/agents.d/starfleet/
}

// unquoteYAML/quoteYAML: same minimal hand-rolled scheme as
// internal/dashboard's topic frontmatter — flat key: "quoted value" pairs
// only, no nested structures, so a real YAML dependency would be overkill.
func unquoteYAML(v string) string {
	if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
		inner := v[1 : len(v)-1]
		inner = strings.ReplaceAll(inner, `\"`, `"`)
		inner = strings.ReplaceAll(inner, `\\`, `\`)
		return inner
	}
	return v
}

func quoteYAML(v string) string {
	v = strings.ReplaceAll(v, `\`, `\\`)
	v = strings.ReplaceAll(v, `"`, `\"`)
	return `"` + v + `"`
}

// parseFragmentFile splits a fragment file into its frontmatter (parsed)
// and body.
func parseFragmentFile(data []byte) (FragmentMeta, string, error) {
	s := string(data)
	if !strings.HasPrefix(s, "---\n") {
		return FragmentMeta{}, "", fmt.Errorf("missing frontmatter (no leading '---')")
	}
	rest := s[len("---\n"):]
	idx := strings.Index(rest, "\n---\n")
	if idx < 0 {
		return FragmentMeta{}, "", fmt.Errorf("unterminated frontmatter (no closing '---')")
	}
	fm := rest[:idx]
	body := strings.TrimPrefix(rest[idx+len("\n---\n"):], "\n")

	var m FragmentMeta
	for _, line := range strings.Split(fm, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		kv := strings.SplitN(line, ":", 2)
		if len(kv) != 2 {
			continue
		}
		key := strings.TrimSpace(kv[0])
		val := unquoteYAML(strings.TrimSpace(kv[1]))
		switch key {
		case "slug":
			m.Slug = val
		case "title":
			m.Title = val
		case "order":
			if n, err := strconv.Atoi(val); err == nil {
				m.Order = n
			}
		case "owner":
			m.Owner = val
		}
	}
	return m, body, nil
}

// renderFragmentFile is the pure part of writeFragmentFile — no I/O, so
// callers that only need to know what WOULD be written (e.g. bootstrap's
// verifySelfFragment, comparing against what's already on disk) don't need
// a throwaway temp file.
func renderFragmentFile(m FragmentMeta, body string) []byte {
	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b, "slug: %s\n", m.Slug)
	fmt.Fprintf(&b, "title: %s\n", quoteYAML(m.Title))
	fmt.Fprintf(&b, "order: %d\n", m.Order)
	if m.Owner != "" {
		fmt.Fprintf(&b, "owner: %s\n", quoteYAML(m.Owner))
	}
	b.WriteString("---\n\n")
	b.WriteString(strings.TrimRight(body, "\n"))
	b.WriteString("\n")
	return []byte(b.String())
}

func writeFragmentFile(path string, m FragmentMeta, body string) error {
	return os.WriteFile(path, renderFragmentFile(m, body), 0o644)
}

// loadAllFragments reads every fragment file from both user-maintained
// (agents.d/) and auto-rolled starfleet (.starfleet-ai/agents.d/starfleet/)
// directories, sorted by (Order, Slug). Walks subdirectories recursively.
// The slug is the relative path from the respective root with .md stripped.
func (a *Agents) loadAllFragments() ([]FragmentMeta, error) {
	var metas []FragmentMeta

	dirs := []struct {
		dir         string
		prefix      string // added to slug (e.g. "starfleet/")
		isStarfleet bool
	}{
		{a.FragmentsDir(), "", false},
		{a.StarfleetFragmentsDir(), "starfleet/", true},
	}

	for _, d := range dirs {
		err := filepath.Walk(d.dir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				// Skip agents.d/starfleet/ — those fragments are now managed
				// by starfleetctl under .starfleet-ai/agents.d/starfleet/
				if d.dir == a.FragmentsDir() && info.Name() == "starfleet" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(info.Name(), ".md") || info.Name() == "index.md" {
				return nil
			}
			rel, err := filepath.Rel(d.dir, path)
			if err != nil {
				return err
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			m, _, err := parseFragmentFile(data)
			if err != nil {
				return fmt.Errorf("%s: %w", rel, err)
			}
			slug := d.prefix + strings.TrimSuffix(rel, ".md")
			if m.Slug == "" {
				m.Slug = slug
			}
			m.IsStarfleet = d.isStarfleet
			metas = append(metas, m)
			return nil
		})
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
	}

	sort.Slice(metas, func(i, j int) bool {
		if metas[i].Order != metas[j].Order {
			return metas[i].Order < metas[j].Order
		}
		return metas[i].Slug < metas[j].Slug
	})
	return metas, nil
}
