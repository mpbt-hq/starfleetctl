// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package task

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// newTestRoot returns a temp workspace root with a git repo, the dashboard
// topics dir and a comms status dir, so both dashboard.New and comms.New
// resolve against it exactly like the CLI does.
func newTestRoot(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("git", "init", "-q")
	run("git", "config", "user.email", "test@test")
	run("git", "config", "user.name", "test")

	if err := os.MkdirAll(filepath.Join(dir, ".starfleet-ai", "dashboard", "topics"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// writeTopic drops a topic file directly (simulating task capture's output),
// returning its slug.
func writeTopic(t *testing.T, root, slug, title, status, assigned string) {
	t.Helper()
	body := `---
title: "` + title + `"
category: active
kind: task
status: ` + status + `
assigned-to: "` + assigned + `"
created-by: "Enterprise"
created: "2026-09-07T00:00:00Z"
doc-ref: "—"
---

Body of ` + slug + `
`
	path := filepath.Join(root, ".starfleet-ai", "dashboard", "topics", slug+".md")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeStatusRecord simulates a live ship on the board (a status/<ship>.json
// heartbeat entry).
func writeStatusRecord(t *testing.T, root, ship string) {
	t.Helper()
	path := filepath.Join(root, ".starfleet-ai", "var", "comms", "status", ship+".json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"agent":"`+ship+`","state":"idle","epoch":0}`), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestFindOrphans verifies orphan detection: a task is an orphan when it is
// assigned to a ship with no board entry. Done tasks and the __auto__
// sentinel are not orphans.
func TestFindOrphans(t *testing.T) {
	root := newTestRoot(t)
	writeTopic(t, root, "task-live", "assigned to live ship", "assigned", "Galactica")
	writeTopic(t, root, "task-vanished", "assigned to vanished ship", "assigned", "Stargazer")
	writeTopic(t, root, "task-done", "done task", "done", "Stargazer")
	writeTopic(t, root, "task-auto", "auto sentinel", "assigned", "__auto__")
	writeTopic(t, root, "task-unassigned", "no assignment", "open", "—")
	writeTopic(t, root, "task-inprogress", "in-progress on vanished ship", "in-progress", "Miranda")
	// Galactica is on the board; Stargazer/Miranda are gone.
	writeStatusRecord(t, root, "Galactica")

	orphans, err := FindOrphans(root)
	if err != nil {
		t.Fatalf("FindOrphans: %v", err)
	}

	if len(orphans) != 2 {
		t.Fatalf("expected 2 orphans, got %d: %+v", len(orphans), orphans)
	}
	if orphans[0].Slug != "task-inprogress" || orphans[0].AssignedTo != "Miranda" {
		t.Errorf("first orphan wrong: %+v", orphans[0])
	}
	if orphans[1].Slug != "task-vanished" || orphans[1].AssignedTo != "Stargazer" {
		t.Errorf("second orphan wrong: %+v", orphans[1])
	}
}

// TestRunOrphansCLI exercises the JSON path of the orphans subcommand via the
// in-process dispatcher.
func TestRunOrphansCLI(t *testing.T) {
	root := newTestRoot(t)
	writeTopic(t, root, "task-vanished", "assigned to vanished ship", "assigned", "Stargazer")
	writeTopic(t, root, "task-live", "assigned to live ship", "assigned", "Galactica")
	writeStatusRecord(t, root, "Galactica")

	if code := Run(root, []string{"orphans", "--json"}); code != 0 {
		t.Fatalf("task orphans --json exited with code %d", code)
	}
	// A plain (table) run must also exit 0.
	if code := Run(root, []string{"orphans"}); code != 0 {
		t.Fatalf("task orphans exited with code %d", code)
	}
}
