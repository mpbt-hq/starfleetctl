// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package sop

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// FragmentMeta is one sop.d/<slug>.md file's frontmatter.
type FragmentMeta struct {
	Slug        string
	Title       string
	Order       int    // controls .starfleet-ai/var/sop.d/index.md's import order
	Owner       string // optional: which tool/component maintains this fragment
	IsStarfleet bool   // true if fragment lives under .starfleet-ai/var/sop.d/starfleet-instructions/
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

// errFragmentNoFrontmatter / errFragmentUnterminated are the recoverable
// parse conditions: the fragment's body is still usable (raw content), only
// its frontmatter fields are missing/broken. loadAllFragments keeps such
// fragments (indexed with a slug derived from the file path) and surfaces a
// warning; callers that require well-formed fragments (e.g. the embedded
// starfleet set) treat them as fatal.
var (
	errFragmentNoFrontmatter = errors.New("missing frontmatter (no leading '---')")
	errFragmentUnterminated  = errors.New("unterminated frontmatter (no closing '---')")
)

// parseFragmentFile splits a fragment file into its frontmatter (parsed)
// and body. It is lenient: a missing or malformed frontmatter block is not
// fatal — the whole content is returned as body together with a non-nil
// error identifying the problem. Strict callers may fail on that error;
// lenient callers keep the body and continue.
func parseFragmentFile(data []byte) (FragmentMeta, string, error) {
	s := string(data)
	if !strings.HasPrefix(s, "---\n") {
		return FragmentMeta{}, s, errFragmentNoFrontmatter
	}
	rest := s[len("---\n"):]
	idx := strings.Index(rest, "\n---\n")
	if idx < 0 {
		return FragmentMeta{}, s, errFragmentUnterminated
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

// loadAllFragments reads every fragment file from the user-maintained
// (sop.d/) and auto-rolled starfleet (.starfleet-ai/var/sop.d/starfleet-instructions/)
// directories, sorted by (Order, Slug). Walks subdirectories recursively.
// The slug is the relative path from the respective root with .md stripped.
// The legacy agents.d/ directory is still read too (kept as legacy-supported),
// but a fragment with the same slug in sop.d/ takes precedence.
// Fragments without a usable frontmatter block are kept (raw body, slug
// derived from the path) and reported in the returned warnings slice instead
// of failing the whole load.
func (s *SOP) loadAllFragments() ([]FragmentMeta, []string, error) {
	var metas []FragmentMeta
	var warnings []string
	seen := map[string]bool{}

	dirs := []struct {
		dir         string
		prefix      string // added to slug (e.g. "starfleet-instructions/")
		isStarfleet bool
	}{
		{s.FragmentsDir(), "", false},
		{s.StarfleetFragmentsDir(), "starfleet-instructions/", true},
		{filepath.Join(s.Root, "agents.d"), "", false}, // legacy
	}

	for _, d := range dirs {
		err := filepath.Walk(d.dir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				// Skip sop.d/starfleet/ — legacy directory, auto-installed
				// fragments now live under .starfleet-ai/var/sop.d/starfleet-instructions/
				if d.dir == s.FragmentsDir() && info.Name() == "starfleet" {
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
			slug := d.prefix + strings.TrimSuffix(rel, ".md")
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			m, _, perr := parseFragmentFile(data)
			if perr != nil {
				if errors.Is(perr, errFragmentNoFrontmatter) || errors.Is(perr, errFragmentUnterminated) {
					// Recoverable: index the raw body under the slug derived
					// from the path, warn — one bad fragment must not break
					// the whole index.
					warnings = append(warnings, fmt.Sprintf("%s: %v (indexed as %q, raw body)", rel, perr, slug))
					m = FragmentMeta{}
				} else {
					return fmt.Errorf("%s: %w", rel, perr)
				}
			}
			if m.Slug == "" {
				m.Slug = slug
			}
			// sop.d/ wins over the legacy agents.d/ for the same slug.
			if seen[m.Slug] {
				return nil
			}
			m.IsStarfleet = d.isStarfleet
			seen[m.Slug] = true
			metas = append(metas, m)
			return nil
		})
		if err != nil && !os.IsNotExist(err) {
			return nil, nil, err
		}
	}

	sort.Slice(metas, func(i, j int) bool {
		if metas[i].Order != metas[j].Order {
			return metas[i].Order < metas[j].Order
		}
		return metas[i].Slug < metas[j].Slug
	})
	return metas, warnings, nil
}
