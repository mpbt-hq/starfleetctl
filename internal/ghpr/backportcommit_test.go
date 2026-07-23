// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package ghpr

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/metux/starfleetctl/internal/projectconfig"
)

func TestSuffixSegmentMatch(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"Xext/dpms/dpms.c", "Xext/dpms/dpms.c", 3},
		{"dpms/dpms.c", "Xext/dpms/dpms.c", 2},
		{"other/dpms.c", "Xext/dpms/dpms.c", 1},
		{"totally/unrelated.c", "Xext/dpms/dpms.c", 0},
	}
	for _, c := range cases {
		if got := suffixSegmentMatch(c.a, c.b); got != c.want {
			t.Errorf("suffixSegmentMatch(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestPickBestSuffixMatch(t *testing.T) {
	t.Run("unique winner", func(t *testing.T) {
		candidates := "dpms/dpms.c\nunrelated/dpms.c"
		// "dpms/dpms.c" shares 2 trailing segments with "Xext/dpms/dpms.c",
		// "unrelated/dpms.c" shares only 1 ("dpms.c").
		got := pickBestSuffixMatch(candidates, "Xext/dpms/dpms.c")
		if got != "dpms/dpms.c" {
			t.Errorf("got %q, want dpms/dpms.c", got)
		}
	})
	t.Run("ambiguous tie -> empty", func(t *testing.T) {
		candidates := "a/dpms.c\nb/dpms.c"
		got := pickBestSuffixMatch(candidates, "Xext/dpms.c")
		if got != "" {
			t.Errorf("got %q, want \"\" (ambiguous)", got)
		}
	})
	t.Run("no candidates -> empty", func(t *testing.T) {
		got := pickBestSuffixMatch("", "Xext/dpms/dpms.c")
		if got != "" {
			t.Errorf("got %q, want \"\"", got)
		}
	})
}

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"glamor: clamp CopyArea source boxes to the source pixmap (workaround #3136)": "glamor-clamp-copyarea-source-boxes-to-the-source-pixmap-workaround-3136-",
		"Simple Subject":       "simple-subject",
		"trailing---dashes--!": "trailing-dashes-",
	}
	for in, want := range cases {
		if got := slugify(in); got != want {
			t.Errorf("slugify(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestApplyViaPathRemap exercises the real cherry-pick-fails -> path-remap
// fallback against a scratch git repo simulating the Xext/<ext>/ <-> <ext>/
// directory reorg between master and an older release line: a "master"
// branch modifies Xext/dpms/dpms.c, while the release branch already moved
// that file to dpms/dpms.c before the modification was made — a plain
// cherry-pick must fail (path doesn't exist), and applyViaPathRemap must
// recognize the pure rename and reconstruct the same change at the new
// path.
func TestApplyViaPathRemap(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	run("init", "-q")
	run("config", "user.email", "test@test.com")
	run("config", "user.name", "Test")

	if err := os.MkdirAll(filepath.Join(dir, "Xext", "dpms"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Xext", "dpms", "dpms.c"), []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-q", "-m", "base")

	// The "master"-side commit we want to backport: modifies the file at
	// its master-tree path.
	if err := os.WriteFile(filepath.Join(dir, "Xext", "dpms", "dpms.c"), []byte("v1\nv2 fix\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-q", "-m", "dpms: fix something")
	sha, err := gitCapture(dir, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}

	// Reset to base and simulate the release line's reorg (file moved
	// before this fix was ever written there) — this is the "current
	// branch" applyViaPathRemap must reconcile against.
	run("reset", "-q", "--hard", "HEAD~1")
	run("mv", filepath.Join("Xext", "dpms", "dpms.c"), "placeholder")
	if err := os.MkdirAll(filepath.Join(dir, "dpms"), 0o755); err != nil {
		t.Fatal(err)
	}
	run("mv", "placeholder", filepath.Join("dpms", "dpms.c"))
	run("add", "-A")
	run("commit", "-q", "-m", "reorg: move dpms.c")

	rc := applyViaPathRemap(dir, sha, projectconfig.DefaultProjectConfig())
	if rc != 0 {
		t.Fatalf("applyViaPathRemap returned %d, want 0", rc)
	}

	got, err := os.ReadFile(filepath.Join(dir, "dpms", "dpms.c"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "v1\nv2 fix\n" {
		t.Errorf("dpms/dpms.c content = %q, want the fix applied at the new path", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "Xext", "dpms", "dpms.c")); !os.IsNotExist(err) {
		t.Errorf("Xext/dpms/dpms.c should not have been recreated at the old path")
	}

	msg, err := gitCapture(dir, "log", "-1", "--format=%B")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg, "dpms: fix something") || !strings.Contains(msg, "(cherry picked from commit "+sha+")") {
		t.Errorf("reconstructed commit message = %q, missing subject or cherry-pick provenance", msg)
	}
}

// TestApplyViaPathRemap_NotAReorg verifies the "genuine content conflict,
// not a reorg" case bails with exit code 3 rather than guessing.
func TestApplyViaPathRemap_NotAReorg(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "test@test.com")
	run("config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(dir, "same.c"), []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-q", "-m", "base")

	if err := os.WriteFile(filepath.Join(dir, "same.c"), []byte("v1\nchanged upstream\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-q", "-m", "modify same.c")
	sha, err := gitCapture(dir, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}

	// No reorg on this branch at all — same.c still exists at the same
	// path, so applyViaPathRemap must NOT treat this as a remap case.
	run("reset", "-q", "--hard", "HEAD~1")

	rc := applyViaPathRemap(dir, sha, projectconfig.DefaultProjectConfig())
	if rc != 3 {
		t.Fatalf("applyViaPathRemap returned %d, want 3 (not a reorg)", rc)
	}
}
