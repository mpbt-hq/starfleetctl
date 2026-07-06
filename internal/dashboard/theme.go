// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
//
// Per-theme file support (DASHBOARD.md restructuring, directive m0048/m0073):
// DASHBOARD.md itself is a thin, mechanically-regenerated index; the actual
// content for each theme lives in its own file under dashboard/themes/,
// Markdown body with a small YAML-ish frontmatter block (mirroring Claude
// Code's own per-user memory-file format — see DASHBOARD-RESTRUCTURE.md for
// the full design rationale). This keeps concurrent ships from colliding on
// one shared file: two ships editing two different themes touch two
// different files, and only reindex (regenerating the thin index) touches
// DASHBOARD.md itself, which is a pure function of the current file set —
// two racing reindexes converge to the same byte-identical output.
//
// Frontmatter parsing is hand-rolled (stdlib only, no YAML dependency) since
// the schema is flat key: "quoted value" pairs, never nested structures —
// a full YAML library would be overkill for this.
package dashboard

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ThemeMeta is one theme file's frontmatter.
type ThemeMeta struct {
	Slug         string
	Title        string
	Category     string // "active" or "parked"
	Status       string // active only
	DocRef       string // active only
	NotedBy      string // parked only
	Since        string // parked only
	MigratedFrom string
}

func (d *Dashboard) ThemesDir() string {
	return filepath.Join(d.Root, "dashboard", "themes")
}

func (d *Dashboard) themePath(slug string) string {
	return filepath.Join(d.ThemesDir(), slug+".md")
}

// unquoteYAML strips a double-quoted YAML scalar's quoting/escaping; a bare
// (unquoted) value is returned unchanged.
func unquoteYAML(v string) string {
	if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
		inner := v[1 : len(v)-1]
		inner = strings.ReplaceAll(inner, `\"`, `"`)
		inner = strings.ReplaceAll(inner, `\\`, `\`)
		return inner
	}
	return v
}

// quoteYAML produces a double-quoted YAML scalar for an arbitrary string.
func quoteYAML(v string) string {
	v = strings.ReplaceAll(v, `\`, `\\`)
	v = strings.ReplaceAll(v, `"`, `\"`)
	return `"` + v + `"`
}

// parseThemeFile splits a theme file into its frontmatter (parsed) and body.
func parseThemeFile(data []byte) (ThemeMeta, string, error) {
	s := string(data)
	if !strings.HasPrefix(s, "---\n") {
		return ThemeMeta{}, "", fmt.Errorf("missing frontmatter (no leading '---')")
	}
	rest := s[len("---\n"):]
	idx := strings.Index(rest, "\n---\n")
	if idx < 0 {
		return ThemeMeta{}, "", fmt.Errorf("unterminated frontmatter (no closing '---')")
	}
	fm := rest[:idx]
	body := strings.TrimPrefix(rest[idx+len("\n---\n"):], "\n")

	var m ThemeMeta
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
		case "category":
			m.Category = val
		case "status":
			m.Status = val
		case "doc_ref":
			m.DocRef = val
		case "noted_by":
			m.NotedBy = val
		case "since":
			m.Since = val
		case "migrated_from":
			m.MigratedFrom = val
		}
	}
	return m, body, nil
}

func writeThemeFile(path string, m ThemeMeta, body string) error {
	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b, "slug: %s\n", m.Slug)
	fmt.Fprintf(&b, "title: %s\n", quoteYAML(m.Title))
	fmt.Fprintf(&b, "category: %s\n", m.Category)
	if m.Category == "parked" {
		fmt.Fprintf(&b, "noted_by: %s\n", quoteYAML(m.NotedBy))
		fmt.Fprintf(&b, "since: %s\n", quoteYAML(m.Since))
	} else {
		fmt.Fprintf(&b, "status: %s\n", quoteYAML(m.Status))
		fmt.Fprintf(&b, "doc_ref: %s\n", quoteYAML(m.DocRef))
	}
	if m.MigratedFrom != "" {
		fmt.Fprintf(&b, "migrated_from: %s\n", m.MigratedFrom)
	}
	b.WriteString("---\n\n")
	b.WriteString(strings.TrimRight(body, "\n"))
	b.WriteString("\n")
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

// loadAllThemes reads every dashboard/themes/*.md file's frontmatter.
func (d *Dashboard) loadAllThemes() ([]ThemeMeta, error) {
	entries, err := os.ReadDir(d.ThemesDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var metas []ThemeMeta
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(d.ThemesDir(), e.Name()))
		if err != nil {
			return nil, err
		}
		m, _, err := parseThemeFile(data)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name(), err)
		}
		if m.Slug == "" {
			m.Slug = strings.TrimSuffix(e.Name(), ".md")
		}
		metas = append(metas, m)
	}
	sort.Slice(metas, func(i, j int) bool { return metas[i].Slug < metas[j].Slug })
	return metas, nil
}
