// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package agents

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
)

// FragmentMeta is one agents.d/<slug>.md file's frontmatter.
type FragmentMeta struct {
	Slug  string
	Title string
	Order int    // controls agents.d/index.md's import order — narrative order matters for AGENTS.md, unlike DASHBOARD.md's themes
	Owner string // optional: which tool/component maintains this fragment (e.g. "starfleetctl"); blank = human-maintained/project-specific
}

// unquoteYAML/quoteYAML: same minimal hand-rolled scheme as
// internal/dashboard's theme frontmatter — flat key: "quoted value" pairs
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

func writeFragmentFile(path string, m FragmentMeta, body string) error {
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
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

// loadAllFragments reads every agents.d/*.md file's frontmatter EXCEPT
// index.md itself (the generated index isn't a content fragment), sorted by
// (Order, Slug) — this sort order is exactly what reindex uses to generate
// the import list, so it directly controls AGENTS.md's effective section
// order.
func (a *Agents) loadAllFragments() ([]FragmentMeta, error) {
	entries, err := os.ReadDir(a.FragmentsDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var metas []FragmentMeta
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") || e.Name() == "index.md" {
			continue
		}
		data, err := os.ReadFile(a.FragmentsDir() + string(os.PathSeparator) + e.Name())
		if err != nil {
			return nil, err
		}
		m, _, err := parseFragmentFile(data)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name(), err)
		}
		if m.Slug == "" {
			m.Slug = strings.TrimSuffix(e.Name(), ".md")
		}
		metas = append(metas, m)
	}
	sort.Slice(metas, func(i, j int) bool {
		if metas[i].Order != metas[j].Order {
			return metas[i].Order < metas[j].Order
		}
		return metas[i].Slug < metas[j].Slug
	})
	return metas, nil
}
