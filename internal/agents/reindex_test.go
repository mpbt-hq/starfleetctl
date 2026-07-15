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

	if err := a.DoReindex(false); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(a.IndexFile())
	if err != nil {
		t.Fatal(err)
	}
	// re-running must produce byte-identical output
	if err := a.DoReindex(false); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(a.IndexFile())
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Errorf("reindex not idempotent:\n--- first ---\n%s\n--- second ---\n%s", first, second)
	}
	if !strings.Contains(string(first), "@agents.d/my-topic.md") {
		t.Errorf("reindex missing import line:\n%s", first)
	}
}

func TestReindexInlineEmitsContent(t *testing.T) {
	a := newTestAgents(t)
	writeFragment(t, a, "my-topic", "My Topic")

	if err := a.SetInline(true); err != nil {
		t.Fatal(err)
	}
	if err := a.DoReindex(true); err != nil {
		t.Fatal(err)
	}
	out, err := os.ReadFile(a.IndexFile())
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	// inline mode must embed the fragment body, NOT an @-import
	if strings.Contains(s, "@agents.d/my-topic.md") {
		t.Errorf("inline reindex still emitted an @-import:\n%s", s)
	}
	if !strings.Contains(s, "(fill in)") {
		t.Errorf("inline reindex did not embed fragment content:\n%s", s)
	}
	if !strings.Contains(s, "begin inlined fragment: my-topic") {
		t.Errorf("inline reindex missing inlined-fragment markers:\n%s", s)
	}
}

func TestReindexInlineMarkerDrivesDefault(t *testing.T) {
	a := newTestAgents(t)
	writeFragment(t, a, "my-topic", "My Topic")

	if err := a.SetInline(true); err != nil {
		t.Fatal(err)
	}
	if err := a.DoReindex(a.Inline()); err != nil {
		t.Fatal(err)
	}
	out, _ := os.ReadFile(a.IndexFile())
	if !strings.Contains(string(out), "(fill in)") {
		t.Errorf("Inline() did not select inline mode:\n%s", out)
	}
}
