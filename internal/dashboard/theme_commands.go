// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package dashboard

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
)

// DoThemeList prints every theme's slug/title/status (or, with jsonOut, a
// JSON array) — the "what's tracked" overview, same spirit as `pr-claim
// --list`/`ship-names list`.
func (d *Dashboard) DoThemeList(jsonOut bool) error {
	metas, err := d.loadAllThemes()
	if err != nil {
		return err
	}
	if jsonOut {
		type row struct {
			Slug     string `json:"slug"`
			Title    string `json:"title"`
			Category string `json:"category"`
			Status   string `json:"status"`
		}
		out := make([]row, 0, len(metas))
		for _, m := range metas {
			st := m.Status
			if m.Category == "parked" {
				st = m.NotedBy
			}
			out = append(out, row{m.Slug, m.Title, m.Category, st})
		}
		enc, err := json.Marshal(out)
		if err != nil {
			return err
		}
		fmt.Println(string(enc))
		return nil
	}
	for _, m := range metas {
		st := m.Status
		if m.Category == "parked" {
			st = m.NotedBy
		}
		fmt.Printf("%-8s %-50s %s\n", m.Category, m.Slug, st)
	}
	return nil
}

// DoThemeShow prints one theme file's full content (frontmatter + body),
// pulling first like DoShow.
func (d *Dashboard) DoThemeShow(slug string) error {
	if err := d.sync(runQuiet); err != nil {
		return err
	}
	data, err := os.ReadFile(d.themePath(slug))
	if err != nil {
		return err
	}
	_, err = os.Stdout.Write(data)
	return err
}

// DoThemeWrite replaces one theme file's content (raw, frontmatter and all)
// from src ("-" for stdin). Does NOT commit.
func (d *Dashboard) DoThemeWrite(slug, src string) error {
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
	if err := os.MkdirAll(d.ThemesDir(), 0o755); err != nil {
		return err
	}
	return os.WriteFile(d.themePath(slug), data, 0o644)
}

// DoThemeNew scaffolds a new theme file with frontmatter, refusing to
// clobber an existing one.
func (d *Dashboard) DoThemeNew(slug, title, status, category string) error {
	if category == "" {
		category = "active"
	}
	path := d.themePath(slug)
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("theme already exists: %s", path)
	}
	if _, err := d.EnsureBootstrapped(); err != nil {
		return err
	}
	m := ThemeMeta{Slug: slug, Title: title, Category: category, Status: status, DocRef: "—"}
	if category == "parked" {
		m.NotedBy = status
		m.Since = ""
	}
	if err := writeThemeFile(path, m, "(fill in)\n"); err != nil {
		return err
	}
	fmt.Println(path)
	return nil
}

// DoThemeCommit stages, commits, and (unless noPush) pushes ONE theme file —
// the concurrency win over whole-file DoCommit: two ships committing two
// different theme files never collide on this path. Same shared clone lock
// as DoCommit, scoped to one file's `git add`.
func (d *Dashboard) DoThemeCommit(slug, msg string, push bool) error {
	lh, err := d.lock()
	if err != nil {
		return err
	}
	defer lh.Close()

	path := d.themePath(slug)
	if err := run(d.Root, "git", "add", path); err != nil {
		return err
	}
	if err := run(d.Root, "git", "diff", "--cached", "--quiet", "--", path); err == nil {
		fmt.Println("dashboard: nothing staged — nothing to commit")
		return nil
	}
	if err := run(d.Root, "git", "commit", "-m", msg); err != nil {
		return err
	}
	if !push {
		return nil
	}
	branch, err := d.branch()
	if err != nil {
		return err
	}
	_ = run(d.Root, "git", "pull", "--rebase", "--autostash")
	return run(d.Root, "git", "push", "origin", branch)
}

var (
	reAktiveHeading    = regexp.MustCompile(`(?m)^## Aktive Themen\s*$`)
	reParkplatzHeading = regexp.MustCompile(`(?m)^## Parkplatz\s*$`)
)

// DoReindex regenerates DASHBOARD.md's "Aktive Themen"/"Parkplatz" thin
// index tables from every dashboard/themes/*.md file's frontmatter, leaving
// everything else in the file (preamble prose, the trailing "Not tracked
// here" footer) untouched. Pure function of the current file set: two
// ships racing a reindex converge to the same byte-identical output.
func (d *Dashboard) DoReindex() error {
	if _, err := d.EnsureBootstrapped(); err != nil {
		return err
	}
	metas, err := d.loadAllThemes()
	if err != nil {
		return err
	}
	data, err := os.ReadFile(d.File)
	if err != nil {
		return err
	}
	content := string(data)

	locAktive := reAktiveHeading.FindStringIndex(content)
	locParkplatz := reParkplatzHeading.FindStringIndex(content)
	if locAktive == nil || locParkplatz == nil {
		return fmt.Errorf("dashboard reindex: could not find '## Aktive Themen'/'## Parkplatz' headings in %s", d.File)
	}
	preamble := content[:locAktive[0]]
	// footer: everything from the next "\n---\n" after Parkplatz's heading onward
	tail := content[locParkplatz[0]:]
	footerIdx := strings.Index(tail, "\n---\n")
	var footer string
	if footerIdx >= 0 {
		footer = tail[footerIdx+1:] // keep the leading "---"
	} else {
		footer = ""
	}

	var b strings.Builder
	b.WriteString(preamble)
	b.WriteString("## Aktive Themen\n\n")
	b.WriteString("Thin index — each row links to its own file under `dashboard/themes/`. Read/write it via\n")
	b.WriteString("`.bin/starfleetctl dashboard theme show|write|new|commit <slug>` (no direct file\n")
	b.WriteString("access — see AGENTS.md); this index itself is regenerated with\n")
	b.WriteString("`.bin/starfleetctl dashboard reindex` and should not normally be hand-edited.\n\n")
	b.WriteString("| Thema | Status | Datei |\n|---|---|---|\n")
	for _, m := range metas {
		if m.Category != "active" {
			continue
		}
		b.WriteString(fmt.Sprintf("| %s | %s | [`dashboard/themes/%s.md`](dashboard/themes/%s.md) |\n",
			stripMD(m.Title), oneLiner(m.Status, 140), m.Slug, m.Slug))
	}
	b.WriteString("\n## Parkplatz\n\n")
	b.WriteString("Angefangen/aufgefallen, aber (noch) nicht weiterverfolgt — kurze Notiz statt Verlust.\n\n")
	b.WriteString("| Thema | Notiert | Seit | Datei |\n|---|---|---|---|\n")
	for _, m := range metas {
		if m.Category != "parked" {
			continue
		}
		b.WriteString(fmt.Sprintf("| %s | %s | %s | [`dashboard/themes/%s.md`](dashboard/themes/%s.md) |\n",
			stripMD(m.Title), m.NotedBy, m.Since, m.Slug, m.Slug))
	}
	b.WriteString("\n")
	b.WriteString(footer)

	return os.WriteFile(d.File, []byte(b.String()), 0o644)
}

func stripMD(s string) string {
	r := strings.NewReplacer("`", "", "*", "")
	return r.Replace(s)
}

func oneLiner(s string, limit int) string {
	t := strings.TrimSpace(s)
	if m := regexp.MustCompile(`^\*\*(.+?)\*\*`).FindStringSubmatch(t); m != nil && len(m[1]) <= limit {
		t = m[1]
	}
	t = stripMD(t)
	if len(t) <= limit {
		return t
	}
	cut := t[:limit]
	if sp := strings.LastIndex(cut, " "); sp > int(float64(limit)*0.6) {
		cut = cut[:sp]
	}
	return cut + "…"
}
