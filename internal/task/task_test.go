// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package task

import (
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/metux/starfleetctl/internal/comms"
	"github.com/metux/starfleetctl/internal/dashboard"
)

// runCmd builds an exec.Cmd running in the given dir (test helper).
func runCmd(dir, name string, args ...string) *exec.Cmd {
	c := exec.Command(name, args...)
	c.Dir = dir
	return c
}

// initGitWorkspace creates a temporary git repo with user config and an
// initial empty commit — the minimum requirement for Dashboard.New + commit.
func initGitWorkspace(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, args := range [][]string{
		{"git", "init"},
		{"git", "config", "user.email", "test@test.local"},
		{"git", "config", "user.name", "Test"},
		{"git", "commit", "--allow-empty", "-m", "init"},
	} {
		cmd := runCmd(root, args[0], args[1:]...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("init %v: %v\n%s", args, err, out)
		}
	}
	return root
}

// writeStatusFile writes a status JSON file for a ship directly into the bus
// StatusDir, with the given epoch and state. This bypasses DoStatus to allow
// crafting stale timestamps.
func writeStatusFile(t *testing.T, b *comms.Bus, agent, state string, epoch int64) {
	t.Helper()
	rec := map[string]interface{}{
		"epoch": epoch,
		"agent": agent,
		"state": state,
		"note":  "test",
	}
	data, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(b.StatusDir, agent+".json")
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeTopicFile writes a minimal topic file with the given frontmatter.
func writeTopicFile(t *testing.T, dir, slug, status, assignedTo string) {
	t.Helper()
	path := filepath.Join(dir, slug+".md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	frontmatter := "---\ntitle: \"Task " + slug + "\"\ncategory: active\nkind: task\nstatus: \"" + status + "\"\nassigned-to: \"" + assignedTo + "\"\n---\n\nBody content.\n"
	if err := os.WriteFile(path, []byte(frontmatter), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestRunSweepStaleMarksDeadShipTasks verifies the core sweep-stale behavior:
// tasks assigned to a ship whose heartbeat has expired (stale) are marked
// interrupted, while tasks assigned to a fresh ship are left untouched.
func TestRunSweepStaleMarksDeadShipTasks(t *testing.T) {
	root := initGitWorkspace(t)

	// Set up env: a TTL short enough to craft stale timestamps easily, but long
	// enough that the "alive" ship can't age past it while the test runs.
	os.Setenv("STARFLEET_SHIP_ID", "TestRunner")
	os.Setenv("STARFLEET_BUS_TTL", "60") // 60 seconds
	os.Setenv("PROJECT", "")
	os.Setenv("STARFLEET_AGENT_HANDLE", "")
	defer func() {
		os.Unsetenv("STARFLEET_SHIP_ID")
		os.Unsetenv("STARFLEET_BUS_TTL")
		os.Unsetenv("PROJECT")
		os.Unsetenv("STARFLEET_AGENT_HANDLE")
	}()

	// Create the bus (needed to access StatusDir for writing status files).
	b, err := comms.New(root)
	if err != nil {
		t.Fatal(err)
	}

	// Craft a DEAD ship: state "working" but epoch 2×TTL ago (well past it).
	staleEpoch := time.Now().Unix() - 120
	writeStatusFile(t, b, "DeadShip", "working", staleEpoch)

	// Craft an ALIVE ship: state "working" with current epoch (not stale).
	writeStatusFile(t, b, "AliveShip", "working", time.Now().Unix())

	// Set up dashboard topics.
	topicsDir := filepath.Join(root, ".starfleet-ai", "dashboard", "topics")
	writeTopicFile(t, topicsDir, "starfleet/task-dead", "assigned", "DeadShip")
	writeTopicFile(t, topicsDir, "starfleet/task-alive", "assigned", "AliveShip")
	writeTopicFile(t, topicsDir, "starfleet/task-open", "open", "—") // not assigned

	// Commit topic files so DoTopicCommitAll can stage + commit changes.
	for _, args := range [][]string{
		{"git", "add", "--all"},
		{"git", "commit", "-m", "init topics"},
	} {
		cmd := runCmd(root, args[0], args[1:]...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	// Run sweep-stale.
	code := runSweepStale(root, []string{"--no-push"})
	if code != 0 {
		t.Fatalf("runSweepStale returned %d, want 0", code)
	}

	// Verify: dead ship's task should now be "interrupted" AND carry the
	// work-log append (ShipSignal room: Schiff nicht mehr präsent).
	d, err := dashboard.New(root)
	if err != nil {
		t.Fatal(err)
	}
	m, body, err := d.DoTopicLoad("starfleet/task-dead")
	if err != nil {
		t.Fatal(err)
	}
	if m.Status != "interrupted" {
		t.Errorf("dead ship task status = %q, want %q", m.Status, "interrupted")
	}
	if !strings.Contains(body, "Schiff nicht mehr präsent") {
		t.Errorf("expected sweep log entry in task body, got:\n%s", body)
	}

	// Verify: alive ship's task is still "assigned" (not touched).
	m2, _, err := d.DoTopicLoad("starfleet/task-alive")
	if err != nil {
		t.Fatal(err)
	}
	if m2.Status != "assigned" {
		t.Errorf("alive ship task status = %q, want %q", m2.Status, "assigned")
	}

	// Verify: unassigned task is still "open".
	m3, _, err := d.DoTopicLoad("starfleet/task-open")
	if err != nil {
		t.Fatal(err)
	}
	if m3.Status != "open" {
		t.Errorf("open task status = %q, want %q", m3.Status, "open")
	}
}

// TestRunSweepStaleNoStaleShips verifies that when no ships are stale,
// sweep-stale returns 0 and marks zero tasks.
func TestRunSweepStaleNoStaleShips(t *testing.T) {
	root := initGitWorkspace(t)

	os.Setenv("STARFLEET_SHIP_ID", "TestRunner")
	os.Setenv("STARFLEET_BUS_TTL", "900")
	os.Setenv("PROJECT", "")
	os.Setenv("STARFLEET_AGENT_HANDLE", "")
	defer func() {
		os.Unsetenv("STARFLEET_SHIP_ID")
		os.Unsetenv("STARFLEET_BUS_TTL")
		os.Unsetenv("PROJECT")
		os.Unsetenv("STARFLEET_AGENT_HANDLE")
	}()

	b, err := comms.New(root)
	if err != nil {
		t.Fatal(err)
	}

	// Only an ALIVE ship (fresh heartbeat, not stale).
	writeStatusFile(t, b, "AliveShip", "working", time.Now().Unix())

	topicsDir := filepath.Join(root, ".starfleet-ai", "dashboard", "topics")
	writeTopicFile(t, topicsDir, "starfleet/task-alive", "assigned", "AliveShip")
	for _, args := range [][]string{
		{"git", "add", "--all"},
		{"git", "commit", "-m", "init topics"},
	} {
		cmd := runCmd(root, args[0], args[1:]...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	code := runSweepStale(root, []string{"--no-push"})
	if code != 0 {
		t.Fatalf("runSweepStale returned %d, want 0", code)
	}

	// Topic unchanged.
	d, err := dashboard.New(root)
	if err != nil {
		t.Fatal(err)
	}
	m, _, err := d.DoTopicLoad("starfleet/task-alive")
	if err != nil {
		t.Fatal(err)
	}
	if m.Status != "assigned" {
		t.Errorf("alive ship task status = %q, want %q", m.Status, "assigned")
	}
}

// TestRunSweepStaleStaleShipNoTasks verifies that a stale ship with NO open
// tasks assigned to it results in zero interrupts (no crash, no error).
func TestRunSweepStaleStaleShipNoTasks(t *testing.T) {
	root := initGitWorkspace(t)

	os.Setenv("STARFLEET_SHIP_ID", "TestRunner")
	os.Setenv("STARFLEET_BUS_TTL", "1")
	os.Setenv("PROJECT", "")
	os.Setenv("STARFLEET_AGENT_HANDLE", "")
	defer func() {
		os.Unsetenv("STARFLEET_SHIP_ID")
		os.Unsetenv("STARFLEET_BUS_TTL")
		os.Unsetenv("PROJECT")
		os.Unsetenv("STARFLEET_AGENT_HANDLE")
	}()

	b, err := comms.New(root)
	if err != nil {
		t.Fatal(err)
	}

	// Stale ship exists, but there are no tasks assigned to it.
	writeStatusFile(t, b, "DeadShip", "working", time.Now().Unix()-10)

	topicsDir := filepath.Join(root, ".starfleet-ai", "dashboard", "topics")
	// Only a task assigned to a DIFFERENT ship.
	writeTopicFile(t, topicsDir, "starfleet/task-other", "assigned", "OtherShip")
	for _, args := range [][]string{
		{"git", "add", "--all"},
		{"git", "commit", "-m", "init topics"},
	} {
		cmd := runCmd(root, args[0], args[1:]...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	code := runSweepStale(root, []string{"--no-push"})
	if code != 0 {
		t.Fatalf("runSweepStale returned %d, want 0", code)
	}

	// The one task is unchanged.
	d, err := dashboard.New(root)
	if err != nil {
		t.Fatal(err)
	}
	m, _, err := d.DoTopicLoad("starfleet/task-other")
	if err != nil {
		t.Fatal(err)
	}
	if m.Status != "assigned" {
		t.Errorf("other ship task status = %q, want %q", m.Status, "assigned")
	}
}

// TestRunSweepStaleHelpFlag verifies that --help prints usage and returns 0.
func TestRunSweepStaleHelpFlag(t *testing.T) {
	root := initGitWorkspace(t)
	os.Setenv("STARFLEET_SHIP_ID", "TestRunner")
	defer os.Unsetenv("STARFLEET_SHIP_ID")

	code := runSweepStale(root, []string{"--help"})
	if code != 0 {
		t.Fatalf("runSweepStale --help returned %d, want 0", code)
	}
}

// TestRunSweepStaleUnknownArg verifies that an unknown argument returns exit 2.
func TestRunSweepStaleUnknownArg(t *testing.T) {
	root := initGitWorkspace(t)
	os.Setenv("STARFLEET_SHIP_ID", "TestRunner")
	defer os.Unsetenv("STARFLEET_SHIP_ID")

	code := runSweepStale(root, []string{"--bogus"})
	if code != 2 {
		t.Fatalf("runSweepStale --bogus returned %d, want 2", code)
	}
}

// TestUnattachedStatusHint verifies that a working/building status without
// --task or --note flips the Unattached flag AND prints the loud follow-up
// hint (with the exact `task begin` command) to stderr.
func TestUnattachedStatusHint(t *testing.T) {
	root := initGitWorkspace(t)

	os.Setenv("STARFLEET_SHIP_ID", "TestRunner")
	os.Setenv("PROJECT", "")
	os.Setenv("STARFLEET_AGENT_HANDLE", "")
	defer func() {
		os.Unsetenv("STARFLEET_SHIP_ID")
		os.Unsetenv("PROJECT")
		os.Unsetenv("STARFLEET_AGENT_HANDLE")
	}()

	b, err := comms.New(root)
	if err != nil {
		t.Fatal(err)
	}

	// Capture stderr so we can assert the hint text.
	origStderr := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w

	// working without --task and without --note → unattached + hint
	doErr := b.DoStatus("working", "", comms.StatusPatch{})
	w.Close()
	os.Stderr = origStderr
	if doErr != nil {
		t.Fatalf("DoStatus unattached: %v", doErr)
	}
	hint, _ := io.ReadAll(r)
	hintStr := string(hint)
	if !strings.Contains(hintStr, "unattached") || !strings.Contains(hintStr, "task begin") {
		t.Errorf("expected loud unattached hint with 'task begin', got stderr:\n%s", hintStr)
	}

	// Verify the status record was written with Unattached=true.
	rec, ok := parseStatusFile(b.StatusDir + "/TestRunner.json")
	if !ok {
		t.Fatal("expected a status file after DoStatus")
	}
	if !rec.Unattached {
		t.Error("expected Unattached=true in status record")
	}

	// idle should NOT be unattached, and no hint should be printed.
	origStderr2 := os.Stderr
	r2, w2, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w2
	if err := b.DoStatus("idle", "", comms.StatusPatch{}); err != nil {
		t.Fatal(err)
	}
	w2.Close()
	os.Stderr = origStderr2
	hint2, _ := io.ReadAll(r2)
	if strings.Contains(string(hint2), "unattached") {
		t.Errorf("unexpected unattached hint for idle status:\n%s", string(hint2))
	}
	rec2, ok := parseStatusFile(b.StatusDir + "/TestRunner.json")
	if !ok {
		t.Fatal("expected a status file after DoStatus idle")
	}
	if rec2.Unattached {
		t.Error("expected Unattached=false for idle status")
	}
}

// parseStatusFile is a test-only helper to read a status JSON file (same
// logic as comms.parseStatusFile, but avoids exporting it).
func parseStatusFile(path string) (comms.StatusRecord, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return comms.StatusRecord{}, false
	}
	s := strings.TrimSpace(string(data))
	var rec comms.StatusRecord
	if err := json.Unmarshal([]byte(s), &rec); err != nil {
		return comms.StatusRecord{}, false
	}
	return rec, true
}
