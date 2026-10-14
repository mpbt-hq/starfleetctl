// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package modelproxy

import (
	"os"
	"path/filepath"
	"testing"
)

// TestProcWorkspaceRootNested guards killProcsForRoot: a workspace nested
// inside another one (e.g. an isolated testbed under _WORK_/) must resolve to
// its OWN root, not the outer workspace — otherwise the PID-file fallback
// would kill the proxy of an unrelated instance.
func TestProcWorkspaceRootNested(t *testing.T) {
	base := t.TempDir()

	live := filepath.Join(base, "workspace")
	nested := filepath.Join(live, "_WORK_", "starfleet-testbed")
	plain := filepath.Join(base, "elsewhere")

	mustMkdir(t, filepath.Join(live, ".starfleet-ai"))
	mustMkdir(t, filepath.Join(nested, ".starfleet-ai"))
	mustMkdir(t, plain)

	if got := procWorkspaceRoot(live); got != live {
		t.Errorf("live root: got %q want %q", got, live)
	}
	if got := procWorkspaceRoot(nested); got != nested {
		t.Errorf("nested root: got %q want %q", got, nested)
	}
	if got := procWorkspaceRoot(filepath.Join(live, "_WORK_")); got != live {
		t.Errorf("subdir of live: got %q want %q", got, live)
	}
	if got := procWorkspaceRoot(plain); got != "" {
		t.Errorf("plain dir: got %q want empty", got)
	}
}

func TestSamePath(t *testing.T) {
	base := t.TempDir()
	a := filepath.Join(base, "x")
	mustMkdir(t, a)
	if !samePath(a, filepath.Join(base, ".", "x")) {
		t.Errorf("expected samePath(%q, %q) == true", a, filepath.Join(base, ".", "x"))
	}
	if samePath(a, filepath.Join(base, "y")) {
		t.Errorf("expected distinct paths to differ")
	}
}

func mustMkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
}
