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

	"github.com/metux/starfleetctl/internal/config"
)

// TopicMeta is one topic file's frontmatter.
// Slug is derived from the filename and NOT stored in the frontmatter.
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
	Tags         string // comma-separated area tags, e.g. "starfleet, ci"
	Resolved     string // parked only: resolution note
}

func (d *Dashboard) TopicsDir() string {
	return filepath.Join(d.Root, ".starfleet-ai", "dashboard", "topics")
}

func (d *Dashboard) topicPath(slug string) string {
	return filepath.Join(d.TopicsDir(), slug+".md")
}

// topicPathWithCategory returns the topic path with category as subdirectory.
func (d *Dashboard) topicPathWithCategory(slug, category string) string {
	if category != "" && category != "active" {
		// If slug already starts with category/, don't prepend again
		if strings.HasPrefix(slug, category+"/") {
			return filepath.Join(d.TopicsDir(), slug+".md")
		}
		return filepath.Join(d.TopicsDir(), category, slug+".md")
	}
	return filepath.Join(d.TopicsDir(), slug+".md")
}

// SlugCategory returns the category (path prefix) of a slug — the part before
// the first "/", or "" for toplevel topics: "starfleet/task-x" -> "starfleet",
// "task-x" -> "".
func SlugCategory(slug string) string {
	if i := strings.Index(slug, "/"); i >= 0 {
		return slug[:i]
	}
	return ""
}

// SlugBase returns the slug without its category prefix:
// "starfleet/task-x" -> "task-x", "task-x" -> "task-x".
func SlugBase(slug string) string {
	if i := strings.Index(slug, "/"); i >= 0 {
		return slug[i+1:]
	}
	return slug
}

// SlugForCategory returns the slug a topic would have in the given category:
// "" or "active" (toplevel) keeps the bare base, any other category is
// prefixed: ("task-x", "starfleet") -> "starfleet/task-x".
func SlugForCategory(base, category string) string {
	if category == "" || category == "active" {
		return base
	}
	return category + "/" + base
}

// TopicPath returns the absolute filesystem path for a topic slug — exported
// so task rm etc. can access it without duplicating the path logic.
func (d *Dashboard) TopicPath(slug string) string {
	return d.topicPath(slug)
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
// Supports two formats:
// 1. RFC2822-style: simple Key: Value headers ending with a blank line (preferred)
// 2. YAML frontmatter: --- delimited block (legacy, for backward compat)
func parseTopicFile(data []byte) (TopicMeta, string, error) {
	s := string(data)

	// If file starts with "---", treat as YAML frontmatter (legacy)
	if strings.HasPrefix(s, "---\n") {
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
			case "tags":
				m.Tags = val
			case "resolved":
				m.Resolved = val
			}
		}
		if m.Category == "" {
			m.Category = "active"
		}
		return m, body, nil
	}

	// Otherwise, try RFC2822-style: headers until blank line
	idx := strings.Index(s, "\n\n")
	if idx >= 0 {
		headers := s[:idx]
		body := strings.TrimPrefix(s[idx+len("\n\n"):], "\n")
		var m TopicMeta
		hasHeaders := false
		for _, line := range strings.Split(headers, "\n") {
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
			hasHeaders = true
			switch strings.ToLower(key) {
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
			case "doc_ref", "doc-ref":
				m.DocRef = val
			case "noted_by":
				m.NotedBy = val
			case "since":
				m.Since = val
			case "migrated_from":
				m.MigratedFrom = val
			case "tags":
				m.Tags = val
			case "resolved":
				m.Resolved = val
			}
		}
		if hasHeaders {
			if m.Category == "" {
				m.Category = "active"
			}
			return m, body, nil
		}
	}

	return TopicMeta{}, "", fmt.Errorf("missing frontmatter (no YAML '---' and no RFC2822 headers)")
}

func writeTopicFile(path string, m TopicMeta, body string) error {
	var b strings.Builder
	fmt.Fprintf(&b, "Title: %s\n", quoteYAML(m.Title))
	fmt.Fprintf(&b, "Category: %s\n", m.Category)
	if m.Category == "parked" {
		fmt.Fprintf(&b, "Noted-By: %s\n", quoteYAML(m.NotedBy))
		fmt.Fprintf(&b, "Since: %s\n", quoteYAML(m.Since))
		if m.Resolved != "" {
			fmt.Fprintf(&b, "Resolved: %s\n", quoteYAML(m.Resolved))
		}
	} else {
		if m.Kind != "" {
			fmt.Fprintf(&b, "Kind: %s\n", quoteYAML(m.Kind))
		}
		fmt.Fprintf(&b, "Status: %s\n", quoteYAML(m.Status))
		fmt.Fprintf(&b, "Assigned-To: %s\n", quoteYAML(m.AssignedTo))
		fmt.Fprintf(&b, "Created-By: %s\n", quoteYAML(m.CreatedBy))
		fmt.Fprintf(&b, "Created: %s\n", quoteYAML(m.Created))
		fmt.Fprintf(&b, "Doc-Ref: %s\n", quoteYAML(m.DocRef))
	}
	if m.MigratedFrom != "" {
		fmt.Fprintf(&b, "Migrated-From: %s\n", m.MigratedFrom)
	}
	if m.Tags != "" {
		fmt.Fprintf(&b, "Tags: %s\n", quoteYAML(m.Tags))
	}
	b.WriteString("\n")
	b.WriteString(strings.TrimRight(body, "\n"))
	b.WriteString("\n")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

// loadAllTopics reads every dashboard/topics/*.md file's frontmatter,
// recursing into subdirectories for better organization (e.g. projects/,
// areas/). The slug is derived from the relative path under topicsDir:
//
//	topics/foo.md              → slug = "foo"
//	topics/project/x86emu.md   → slug = "project/x86emu"
//
// The slug is NOT stored in the frontmatter; it's derived from the filename.
//
// When strict=false (the recommended mode for listing/UI paths), a single
// broken or unreadable file is logged to stderr and skipped instead of
// failing the whole walk — otherwise one corrupted topic would blank the
// entire task board. strict=true (fail-fast) is for callers that must not
// silently lose topics.
func (d *Dashboard) loadAllTopics(strict bool) ([]TopicMeta, error) {
	topicsDir := d.TopicsDir()
	if _, err := os.Stat(topicsDir); os.IsNotExist(err) {
		return nil, nil
	}
	var metas []TopicMeta
	err := filepath.WalkDir(topicsDir, func(path string, e os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == topicsDir {
			return nil
		}
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			return nil
		}
		// Editor lock files (emacs "#.#name#" / ".#name") are symlinks to the
		// file being edited and are not topics — skip them (they may dangle).
		if strings.HasPrefix(e.Name(), ".#") || strings.HasSuffix(e.Name(), "#") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			if strict {
				return err
			}
			fmt.Fprintf(os.Stderr, "dashboard: skipping unreadable topic %s: %v\n", path, err)
			return nil
		}
		m, _, err := parseTopicFile(data)
		if err != nil {
			if strict {
				return fmt.Errorf("%s: %w", path, err)
			}
			fmt.Fprintf(os.Stderr, "dashboard: skipping broken topic %s: %v\n", path, err)
			return nil
		}
		rel, err := filepath.Rel(topicsDir, path)
		if err != nil {
			return err
		}
		m.Slug = strings.TrimSuffix(rel, ".md")
		metas = append(metas, m)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(metas, func(i, j int) bool { return metas[i].Slug < metas[j].Slug })
	return metas, nil
}

// TopicJSON is the JSON-facing view of a topic's frontmatter — the shape the
// web UI consumes (see internal/web). Field names mirror TopicMeta.
type TopicJSON struct {
	Slug       string   `json:"slug"`
	Title      string   `json:"title"`
	Category   string   `json:"category"`
	Kind       string   `json:"kind"`
	Status     string   `json:"status"`
	AssignedTo string   `json:"assigned_to"`
	CreatedBy  string   `json:"created_by"`
	Created    string   `json:"created"`
	NotedBy    string   `json:"noted_by"`
	Since      string   `json:"since"`
	Tags       []string `json:"tags,omitempty"`
}

// LoadAllTopics returns every dashboard/topics/*.md file's frontmatter, sorted
// by slug. Broken/unreadable files are skipped with a stderr warning (see
// loadAllTopics). Exposed for task purge and other callers that need the full set.
func (d *Dashboard) LoadAllTopics() ([]TopicMeta, error) {
	return d.loadAllTopics(false)
}

// LoadAllTopicsJSON returns every dashboard/topics/*.md file's frontmatter as a
// JSON-shaped slice, sorted by slug — for the web UI's task board.
// If strict=true, any parse error fails the whole call. If strict=false
// (default), invalid files are skipped with a warning and the rest are returned.
func (d *Dashboard) LoadAllTopicsJSON(strict bool) ([]TopicJSON, error) {
	metas, err := d.loadAllTopics(strict)
	if err != nil {
		return nil, err
	}
	out := make([]TopicJSON, 0, len(metas))
	for _, m := range metas {
		var tags []string
		if m.Tags != "" {
			for _, t := range strings.Split(m.Tags, ",") {
				t = strings.TrimSpace(t)
				if t != "" {
					tags = append(tags, t)
				}
			}
		}
		out = append(out, TopicJSON{
			Slug: m.Slug, Title: m.Title, Category: m.Category, Kind: m.Kind,
			Status: m.Status, AssignedTo: m.AssignedTo, CreatedBy: m.CreatedBy,
			Created: m.Created, NotedBy: m.NotedBy, Since: m.Since, Tags: tags,
		})
	}
	return out, nil
}

// DoTopicLoad reads an existing topic file, returning its parsed frontmatter
// and body. It is the read counterpart to DoTopicUpdate / DoTopicWrite and the
// sanctioned way to inspect a topic without touching the file as a raw path.
func (d *Dashboard) DoTopicLoad(slug string) (TopicMeta, string, error) {
	data, err := os.ReadFile(d.topicPath(slug))
	if err != nil {
		return TopicMeta{}, "", err
	}
	m, body, err := parseTopicFile(data)
	if err != nil {
		return TopicMeta{}, "", err
	}
	m.Slug = slug
	return m, body, nil
}

// DoTopicMove relocates a topic to a different category (subdirectory),
// changing its slug prefix accordingly (e.g. "starfleet/task-x" ->
// "xlibre/task-x", or "task-x" for toplevel). The frontmatter Category is
// updated to match (mirroring task capture), and the file is moved on disk —
// both the new path write and the old path removal happen here. Committing is
// left to the caller via DoTopicCommitMove, which stages both paths.
func (d *Dashboard) DoTopicMove(slug, newCategory string, m TopicMeta, body string) (string, error) {
	newSlug := SlugForCategory(SlugBase(slug), newCategory)
	if newSlug == slug {
		return newSlug, nil
	}
	if newCategory == "" || newCategory == "active" {
		m.Category = "active"
	} else {
		m.Category = newCategory
	}
	if err := writeTopicFile(d.topicPath(newSlug), m, body); err != nil {
		return "", err
	}
	if err := os.Remove(d.topicPath(slug)); err != nil {
		return "", err
	}
	return newSlug, nil
}

// DoTopicUpdate rewrites an existing topic file with the given frontmatter
// and body, via the sanctioned dashboard path (never hand-edits the file).
// It is the in-place counterpart to DoTopicWrite (which takes a source file).
func (d *Dashboard) DoTopicUpdate(slug string, m TopicMeta, body string) error {
	tmpDir := filepath.Join(config.WorkDir(d.Root), "tmp")
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
