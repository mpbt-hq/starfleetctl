// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
//
// Per-topic file support (DASHBOARD.md restructuring, directive m0048/m0073):
// DASHBOARD.md itself is a thin, mechanically-regenerated index; the actual
// content for each topic lives in its own file under dashboard/topics/,
// Markdown body with a small YAML-ish frontmatter block (mirroring Claude
// Code's own per-user memory-file format — see DASHBOARD-RESTRUCTURE.md for
// the full design rationale). This keeps concurrent ships from colliding on
// one shared file: two ships editing two different topics touch two
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

// TopicMeta is one topic file's frontmatter.
type TopicMeta struct {
	Slug         string
	Title        string
	Category     string // "active" or "parked"
	Kind         string // active only, e.g. "task"
	Status       string // active only
	AssignedTo   string // active only, "—" when unassigned
	DocRef       string // active only
	CreatedBy    string // active only
	Created      string // active only
	NotedBy      string // parked only
	Since        string // parked only
	MigratedFrom string
}

func (d *Dashboard) TopicsDir() string {
	return filepath.Join(d.Root, "dashboard", "topics")
}

func (d *Dashboard) topicPath(slug string) string {
	return filepath.Join(d.TopicsDir(), slug+".md")
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

// parseTopicFile splits a topic file into its frontmatter (parsed) and body.
func parseTopicFile(data []byte) (TopicMeta, string, error) {
	s := string(data)
	if !strings.HasPrefix(s, "---\n") {
		return TopicMeta{}, "", fmt.Errorf("missing frontmatter (no leading '---')")
	}
	rest := s[len("---\n"):]
	idx := strings.Index(rest, "\n---\n")
	if idx < 0 {
		return TopicMeta{}, "", fmt.Errorf("unterminated frontmatter (no closing '---')")
	}
	fm := rest[:idx]
	body := strings.TrimPrefix(rest[idx+len("\n---\n"):], "\n")

	var m TopicMeta
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
		case "kind":
			m.Kind = val
		case "status":
			m.Status = val
		case "assigned-to":
			m.AssignedTo = val
		case "created-by":
			m.CreatedBy = val
		case "created":
			m.Created = val
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

func writeTopicFile(path string, m TopicMeta, body string) error {
	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b, "slug: %s\n", m.Slug)
	fmt.Fprintf(&b, "title: %s\n", quoteYAML(m.Title))
	fmt.Fprintf(&b, "category: %s\n", m.Category)
	if m.Category == "parked" {
		fmt.Fprintf(&b, "noted_by: %s\n", quoteYAML(m.NotedBy))
		fmt.Fprintf(&b, "since: %s\n", quoteYAML(m.Since))
	} else {
		if m.Kind != "" {
			fmt.Fprintf(&b, "kind: %s\n", quoteYAML(m.Kind))
		}
		fmt.Fprintf(&b, "status: %s\n", quoteYAML(m.Status))
		fmt.Fprintf(&b, "assigned-to: %s\n", quoteYAML(m.AssignedTo))
		fmt.Fprintf(&b, "created-by: %s\n", quoteYAML(m.CreatedBy))
		fmt.Fprintf(&b, "created: %s\n", quoteYAML(m.Created))
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

// loadAllTopics reads every dashboard/topics/*.md file's frontmatter.
func (d *Dashboard) loadAllTopics() ([]TopicMeta, error) {
	entries, err := os.ReadDir(d.TopicsDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var metas []TopicMeta
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(d.TopicsDir(), e.Name()))
		if err != nil {
			return nil, err
		}
		m, _, err := parseTopicFile(data)
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

// DoTopicLoad reads an existing topic file, returning its parsed frontmatter
// and body. It is the read counterpart to DoTopicUpdate / DoTopicWrite and the
// sanctioned way to inspect a topic without touching the file as a raw path.
func (d *Dashboard) DoTopicLoad(slug string) (TopicMeta, string, error) {
	data, err := os.ReadFile(d.topicPath(slug))
	if err != nil {
		return TopicMeta{}, "", err
	}
	return parseTopicFile(data)
}

// DoTopicUpdate rewrites an existing topic file with the given frontmatter
// and body, via the sanctioned dashboard path (never hand-edits the file).
// It is the in-place counterpart to DoTopicWrite (which takes a source file).
func (d *Dashboard) DoTopicUpdate(slug string, m TopicMeta, body string) error {
	tmpDir := filepath.Join(d.Root, "_WORK_", ".tmp")
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(tmpDir, "topic.*.md")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := writeTopicFile(tmpName, m, body); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return d.DoTopicWrite(slug, tmpName)
}
