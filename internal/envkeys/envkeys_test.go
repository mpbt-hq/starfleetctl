// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package envkeys

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

var origHome string

func setup(t *testing.T) {
	t.Helper()
	origHome = os.Getenv("HOME")
	home := t.TempDir()
	os.Setenv("HOME", home)
	// reset the lazy cache so each test re-reads the profile
	profileOnce = sync.Once{}
	profileVals = map[string]string{}
	t.Cleanup(func() {
		os.Setenv("HOME", origHome)
		profileOnce = sync.Once{}
		profileVals = map[string]string{}
	})
}

func writeProfile(t *testing.T, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(os.Getenv("HOME"), ".profile"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestResolveFromProcessEnv(t *testing.T) {
	setup(t)
	writeProfile(t, "export PROFILE_KEY=from-profile\n")
	os.Setenv("PROC_KEY", "from-process")
	if v, ok := Resolve("PROC_KEY"); !ok || v != "from-process" {
		t.Fatalf("expected process env to win, got %q ok=%v", v, ok)
	}
	if v, ok := Resolve("PROFILE_KEY"); !ok || v != "from-profile" {
		t.Fatalf("expected profile fallback, got %q ok=%v", v, ok)
	}
}

func TestResolveFromProfile(t *testing.T) {
	setup(t)
	writeProfile(t, "export NIM_API_KEY=\"key-1\"\n")
	os.Unsetenv("NIM_API_KEY")
	if v, ok := Resolve("NIM_API_KEY"); !ok || v != "key-1" {
		t.Fatalf("expected profile value key-1, got %q ok=%v", v, ok)
	}
}

func TestResolveMissingSkipsInherited(t *testing.T) {
	setup(t)
	writeProfile(t, "export SP_KEY=profile-val\n")
	os.Setenv("SP_KEY", "inherited")
	if _, ok := ResolveMissing("SP_KEY"); ok {
		t.Fatal("ResolveMissing must not return an already-set var")
	}
	os.Unsetenv("SP_KEY")
	if v, ok := ResolveMissing("SP_KEY"); !ok || v != "profile-val" {
		t.Fatalf("expected profile fallback, got %q ok=%v", v, ok)
	}
}

func TestProfileParsing(t *testing.T) {
	setup(t)
	writeProfile(t, `# comment
export SINGLE='quoted'
export DOUBLE="dquoted"
PLAIN=plain
export SPACED="has spaces"
export VAR_CMD=$(echo hi)
export VAR_BACKTICK=`+"`"+`echo hi`+"`"+`
x  y=skipme
== not an assignment
`)
	expect := map[string]string{
		"SINGLE": "quoted",
		"DOUBLE": "dquoted",
		"PLAIN":  "plain",
		"SPACED": "has spaces",
	}
	loadProfile()
	for k, want := range expect {
		if got, ok := profileVals[k]; !ok || got != want {
			t.Errorf("key %s: expected %q, got %q ok=%v", k, want, got, ok)
		}
	}
	for _, bad := range []string{"VAR_CMD", "VAR_BACKTICK"} {
		if _, ok := profileVals[bad]; ok {
			t.Errorf("key %s must be skipped (command substitution)", bad)
		}
	}
}

func TestBashProfileTakesPrecedenceOverProfile(t *testing.T) {
	setup(t)
	writeProfile(t, "export DUPE=from-profile\n")
	os.WriteFile(filepath.Join(os.Getenv("HOME"), ".bash_profile"), []byte("export DUPE=from-bashprofile\n"), 0o644)
	loadProfile()
	if got := profileVals["DUPE"]; got != "from-bashprofile" {
		t.Fatalf("expected bash_profile to win, got %q", got)
	}
}