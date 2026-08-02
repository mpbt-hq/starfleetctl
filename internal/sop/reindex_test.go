// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package sop

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func newTestSOP(t *testing.T) *SOP {
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

	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func writeFragment(t *testing.T, s *SOP, slug, title string) {
	t.Helper()
	// DoNew scaffolds the fragment file (creating its dir) and reindexes.
	if err := s.DoNew(slug, title, 10, ""); err != nil {
		t.Fatal(err)
	}
}

func TestReindexIdempotent(t *testing.T) {
	s := newTestSOP(t)
	writeFragment(t, s, "my-topic", "My Topic")

	if err := s.DoReindex(); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(s.IndexFile())
	if err != nil {
		t.Fatal(err)
	}
	// re-running must produce byte-identical output
	if err := s.DoReindex(); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(s.IndexFile())
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Errorf("reindex not idempotent:\n--- first ---\n%s\n--- second ---\n%s", first, second)
	}
	if strings.Contains(string(first), "@sop.d/my-topic.md") {
		t.Errorf("reindex still emitted an @-import:\n%s", first)
	}
	if !strings.Contains(string(first), "begin inlined fragment: my-topic") {
		t.Errorf("reindex missing inlined fragment marker:\n%s", first)
	}
}

func TestReindexStripsFrontmatter(t *testing.T) {
	s := newTestSOP(t)
	writeFragment(t, s, "my-topic", "My Topic")

	if err := s.DoReindex(); err != nil {
		t.Fatal(err)
	}
	out, err := os.ReadFile(s.IndexFile())
	if err != nil {
		t.Fatal(err)
	}
	s2 := string(out)
	// frontmatter must be stripped — slug/title/order lines must not appear
	if strings.Contains(s2, "slug: my-topic") {
		t.Errorf("frontmatter not stripped — found 'slug:' line:\n%s", s2)
	}
	if strings.Contains(s2, "title: My Topic") {
		t.Errorf("frontmatter not stripped — found 'title:' line:\n%s", s2)
	}
	if strings.Contains(s2, "order: 10") {
		t.Errorf("frontmatter not stripped — found 'order:' line:\n%s", s2)
	}
	// body content must be present
	if !strings.Contains(s2, "(fill in)") {
		t.Errorf("inline reindex did not embed fragment body:\n%s", s2)
	}
	// inlined-fragment markers must be present
	if !strings.Contains(s2, "begin inlined fragment: my-topic") {
		t.Errorf("inline reindex missing inlined-fragment markers:\n%s", s2)
	}
}

func TestInstallSelfCleansLegacyFragments(t *testing.T) {
	s := newTestSOP(t)
	// Simulate a workspace still carrying the old agents.d/ fragment layout.
	legacyPath := filepath.Join(s.Root, "agents.d", "starfleet", "starfleetctl.md")
	if err := os.MkdirAll(filepath.Dir(legacyPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyPath, []byte("legacy"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := s.DoInstallSelf(10); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(legacyPath); !os.IsNotExist(err) {
		t.Errorf("legacy agents.d fragment not removed: %v", err)
	}
	// The cleanup must have triggered a reindex so the index no longer
	// references the removed fragment.
	idx, err := os.ReadFile(s.IndexFile())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(idx), "starfleet/starfleetctl") {
		t.Errorf("index still references removed legacy fragment:\n%s", idx)
	}
}
