// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package agents

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func newTestAgents(t *testing.T) *Agents {
	t.Helper()
	root := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "test@test")
	run("config", "user.name", "test")
	run("checkout", "-q", "-b", "main")

	a, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func writeFragment(t *testing.T, a *Agents, slug, title string) {
	t.Helper()
	// DoNew scaffolds the fragment file (creating its dir) and reindexes.
	if err := a.DoNew(slug, title, 10, ""); err != nil {
		t.Fatal(err)
	}
}

func TestReindexIdempotent(t *testing.T) {
	a := newTestAgents(t)
	writeFragment(t, a, "my-topic", "My Topic")

	if err := a.DoReindex(); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(a.IndexFile())
	if err != nil {
		t.Fatal(err)
	}
	// re-running must produce byte-identical output
	if err := a.DoReindex(); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(a.IndexFile())
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Errorf("reindex not idempotent:\n--- first ---\n%s\n--- second ---\n%s", first, second)
	}
	if strings.Contains(string(first), "@agents.d/my-topic.md") {
		t.Errorf("reindex still emitted an @-import:\n%s", first)
	}
	if !strings.Contains(string(first), "begin inlined fragment: my-topic") {
		t.Errorf("reindex missing inlined fragment marker:\n%s", first)
	}
}

func TestReindexStripsFrontmatter(t *testing.T) {
	a := newTestAgents(t)
	writeFragment(t, a, "my-topic", "My Topic")

	if err := a.DoReindex(); err != nil {
		t.Fatal(err)
	}
	out, err := os.ReadFile(a.IndexFile())
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	// frontmatter must be stripped — slug/title/order lines must not appear
	if strings.Contains(s, "slug: my-topic") {
		t.Errorf("frontmatter not stripped — found 'slug:' line:\n%s", s)
	}
	if strings.Contains(s, "title: My Topic") {
		t.Errorf("frontmatter not stripped — found 'title:' line:\n%s", s)
	}
	if strings.Contains(s, "order: 10") {
		t.Errorf("frontmatter not stripped — found 'order:' line:\n%s", s)
	}
	// body content must be present
	if !strings.Contains(s, "(fill in)") {
		t.Errorf("inline reindex did not embed fragment body:\n%s", s)
	}
	// inlined-fragment markers must be present
	if !strings.Contains(s, "begin inlined fragment: my-topic") {
		t.Errorf("inline reindex missing inlined-fragment markers:\n%s", s)
	}
}
