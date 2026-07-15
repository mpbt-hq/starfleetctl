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

// DoTopicList prints every topic's slug/title/status (or, with jsonOut, a
// JSON array) — the "what's tracked" overview, same spirit as `pr-claim
// --list`/`ship-names list`.
func (d *Dashboard) DoTopicList(jsonOut bool) error {
	metas, err := d.loadAllTopics()
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

// DoTopicShow prints one topic file's full content (frontmatter + body),
// pulling first like DoShow.
func (d *Dashboard) DoTopicShow(slug string) error {
	if err := d.sync(runQuiet); err != nil {
		return err
	}
	data, err := os.ReadFile(d.topicPath(slug))
	if err != nil {
		return err
	}
	_, err = os.Stdout.Write(data)
	return err
}

// DoTopicWrite replaces one topic file's content (raw, frontmatter and all)
// from src ("-" for stdin). Does NOT commit.
func (d *Dashboard) DoTopicWrite(slug, src string) error {
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
	if err := os.MkdirAll(d.TopicsDir(), 0o755); err != nil {
		return err
	}
	return os.WriteFile(d.topicPath(slug), data, 0o644)
}

// DoTopicNew scaffolds a new topic file with frontmatter, refusing to
// clobber an existing one.
func (d *Dashboard) DoTopicNew(slug, title, status, category string) error {
	if category == "" {
		category = "active"
	}
	path := d.topicPath(slug)
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("topic already exists: %s", path)
	}
	if _, err := d.EnsureBootstrapped(); err != nil {
		return err
	}
	m := TopicMeta{Slug: slug, Title: title, Category: category, Status: status, DocRef: "—"}
	if category == "parked" {
		m.NotedBy = status
		m.Since = ""
	}
	if err := writeTopicFile(path, m, "(fill in)\n"); err != nil {
		return err
	}
	fmt.Println(path)
	return nil
}

// DoTopicCommit stages, commits, and (unless noPush) pushes ONE topic file —
// the concurrency win over whole-file DoCommit: two ships committing two
// different topic files never collide on this path. Same shared clone lock
// as DoCommit, scoped to one file's `git add`.
func (d *Dashboard) DoTopicCommit(slug, msg string, push bool) error {
	lh, err := d.lock()
	if err != nil {
		return err
	}
	defer lh.Close()

	path := d.topicPath(slug)
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
	if err := run(d.Root, "git", "pull", "--rebase", "--autostash"); err != nil {
		return fmt.Errorf("dashboard: pull --rebase failed, NOT pushing (local state may be stale): %w", err)
	}
	return run(d.Root, "git", "push", "origin", branch)
}

var (
	reActiveHeading = regexp.MustCompile(`(?m)^## Active Topics\s*$`)
	reParkedHeading = regexp.MustCompile(`(?m)^## Parked\s*$`)
)

// DoReindex regenerates DASHBOARD.md's "Active Topics"/"Parked" thin
// index tables from every dashboard/topics/*.md file's frontmatter, leaving
// everything else in the file (preamble prose, the trailing "Not tracked
// here" footer) untouched. Pure function of the current file set: two
// ships racing a reindex converge to the same byte-identical output.
func (d *Dashboard) DoReindex() error {
	if _, err := d.EnsureBootstrapped(); err != nil {
		return err
	}
	metas, err := d.loadAllTopics()
	if err != nil {
		return err
	}
	data, err := os.ReadFile(d.File)
	if err != nil {
		return err
	}
	content := string(data)

	locActive := reActiveHeading.FindStringIndex(content)
	locParked := reParkedHeading.FindStringIndex(content)
	if locActive == nil || locParked == nil {
		return fmt.Errorf("dashboard reindex: could not find '## Active Topics'/'## Parked' headings in %s", d.File)
	}
	preamble := content[:locActive[0]]
	// footer: everything from the next "\n---\n" after Parked's heading onward
	tail := content[locParked[0]:]
	footerIdx := strings.Index(tail, "\n---\n")
	var footer string
	if footerIdx >= 0 {
		footer = tail[footerIdx+1:] // keep the leading "---"
	} else {
		footer = ""
	}

	var b strings.Builder
	b.WriteString(preamble)
	b.WriteString("## Active Topics\n\n")
	b.WriteString("| Topic | Status | File |\n|---|---|---|\n")
	for _, m := range metas {
		if m.Category != "active" {
			continue
		}
		b.WriteString(fmt.Sprintf("| %s | %s | [`dashboard/topics/%s.md`](dashboard/topics/%s.md) |\n",
			stripMD(m.Title), oneLiner(m.Status, 140), m.Slug, m.Slug))
	}
	b.WriteString("\n## Parked\n\n")
	b.WriteString("Started/noticed, but (yet) not pursued further — a short note instead of losing it.\n\n")
	b.WriteString("| Topic | Noted by | Since | File |\n|---|---|---|---|\n")
	for _, m := range metas {
		if m.Category != "parked" {
			continue
		}
		b.WriteString(fmt.Sprintf("| %s | %s | %s | [`dashboard/topics/%s.md`](dashboard/topics/%s.md) |\n",
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
