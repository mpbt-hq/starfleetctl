// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package dashboard

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// TopicListOpts holds filters for DoTopicList.
type TopicListOpts struct {
	JSON     bool   // output JSON instead of plain text
	Category string // filter: "active", "parked", or "" (all)
	Status   string // filter: substring match on status/notedBy field
	Tags     string // filter: substring match on tags field
}

// DoTopicList prints every topic's slug/title/status (or, with JSON output,
// a JSON array) — the "what's tracked" overview, same spirit as `pr-claim
// --list`/`ship-names list`.
func (d *Dashboard) DoTopicList(opts TopicListOpts) error {
	metas, err := d.loadAllTopics(false)
	if err != nil {
		return err
	}
	type row struct {
		Slug     string   `json:"slug"`
		Title    string   `json:"title"`
		Category string   `json:"category"`
		Status   string   `json:"status"`
		Tags     []string `json:"tags,omitempty"`
	}
	out := make([]row, 0, len(metas))
	for _, m := range metas {
		if opts.Category != "" && m.Category != opts.Category {
			continue
		}
		st := m.Status
		if m.Category == "parked" {
			st = m.NotedBy
		}
		if opts.Status != "" && !strings.Contains(strings.ToLower(st), strings.ToLower(opts.Status)) {
			continue
		}
		if opts.Tags != "" && !strings.Contains(strings.ToLower(m.Tags), strings.ToLower(opts.Tags)) {
			continue
		}
		var tags []string
		if m.Tags != "" {
			for _, t := range strings.Split(m.Tags, ",") {
				t = strings.TrimSpace(t)
				if t != "" {
					tags = append(tags, t)
				}
			}
		}
		out = append(out, row{m.Slug, m.Title, m.Category, st, tags})
	}
	if opts.JSON {
		enc, err := json.Marshal(out)
		if err != nil {
			return err
		}
		fmt.Println(string(enc))
		return nil
	}
	for _, r := range out {
		fmt.Printf("%-8s %-50s %s\n", r.Category, r.Slug, r.Status)
	}
	return nil
}

// DoTopicShow prints one topic file's full content (frontmatter + body),
// reading directly from the local file (no sync/pull).
func (d *Dashboard) DoTopicShow(slug string) error {
	data, err := os.ReadFile(d.topicPath(slug))
	if err != nil {
		return err
	}
	_, err = os.Stdout.Write(data)
	return err
}

// DoTopicVerify checks whether a topic exists, either on disk or in the git
// index (for cases where the file was already deleted but still tracked).
func (d *Dashboard) DoTopicVerify(slug string) error {
	path := d.topicPath(slug)
	if _, err := os.Stat(path); err == nil {
		return nil // exists on disk
	}
	// Check git index.
	if err := runQuiet(d.Root, "git", "ls-files", "--error-unmatch", path); err == nil {
		return nil // tracked by git
	}
	return fmt.Errorf("topic file not found on disk or in git index: %s", path)
}

// DoTopicDelete removes a topic file from disk. No error if the file doesn't
// exist — git staging is left to DoTopicCommit (which handles git rm for
// deleted files). Use DoTopicVerify for existence checks.
func (d *Dashboard) DoTopicDelete(slug string) error {
	path := d.topicPath(slug)
	if err := os.Remove(path); os.IsNotExist(err) {
		return nil
	} else {
		return err
	}
}

// DoTopicCommitAll stages all pending changes under dashboard/topics/ (both
// additions and deletions) and commits them in one shot. Useful for batch
// operations like task purge.
func (d *Dashboard) DoTopicCommitAll(msg string, push bool) error {
	lh, err := d.lock()
	if err != nil {
		return err
	}
	defer lh.Close()

	if err := run(d.Root, "git", "add", "--all", d.TopicsDir()); err != nil {
		return err
	}
	if err := run(d.Root, "git", "diff", "--cached", "--quiet"); err == nil {
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
		return fmt.Errorf("dashboard: pull --rebase failed, NOT pushing: %w", err)
	}
	return run(d.Root, "git", "push", "origin", branch)
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
	if err := os.MkdirAll(filepath.Dir(d.topicPath(slug)), 0o755); err != nil {
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
	path := d.topicPathWithCategory(slug, category)
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("topic already exists: %s", path)
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
// two ships committing two different topic files never collide on this path.
// Runs under the shared clone lock, scoped to one file's `git add`.
func (d *Dashboard) DoTopicCommit(slug, msg string, push bool) error {
	lh, err := d.lock()
	if err != nil {
		return err
	}
	defer lh.Close()

	path := d.topicPath(slug)
	// If file exists, git add; otherwise git rm to stage the deletion.
	if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
		if err := run(d.Root, "git", "rm", "--cached", path); err != nil {
			return nil // file not in git either — fine
		}
	} else if statErr != nil {
		return statErr
	} else if err := run(d.Root, "git", "add", path); err != nil {
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

// DoTopicCommitMove stages a topic relocation (new path added, old path
// deleted), commits, and (unless push=false) pushes — the commit counterpart
// to DoTopicMove, since a move touches two files rather than one.
func (d *Dashboard) DoTopicCommitMove(oldSlug, newSlug, msg string, push bool) error {
	lh, err := d.lock()
	if err != nil {
		return err
	}
	defer lh.Close()

	oldPath := d.topicPath(oldSlug)
	newPath := d.topicPath(newSlug)
	if err := run(d.Root, "git", "add", newPath); err != nil {
		return err
	}
	if err := run(d.Root, "git", "rm", "--cached", oldPath); err != nil {
		// old file not tracked — nothing to remove, the add below still counts
	}
	if err := run(d.Root, "git", "diff", "--cached", "--quiet", "--", oldPath, newPath); err == nil {
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
