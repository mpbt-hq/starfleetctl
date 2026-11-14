// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package comms

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// newCommsTestRoot returns a temp workspace root with a git repo so comms.New
// resolves against it exactly like the CLI does.
func newCommsTestRoot(t *testing.T) string {
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
	return dir
}

// readStatus reads the comms status record for the given agent.
func readStatus(t *testing.T, b *Bus, agent string) StatusRecord {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(b.StatusDir, agent+".json"))
	if err != nil {
		t.Fatalf("read status for %s: %v", agent, err)
	}
	var rec StatusRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		t.Fatalf("parse status for %s: %v", agent, err)
	}
	return rec
}

func TestDispatchTaskTerminalOutcomes(t *testing.T) {
	root := newCommsTestRoot(t)
	os.Setenv("STARFLEET_SHIP_ID", "test-ship")
	defer os.Unsetenv("STARFLEET_SHIP_ID")

	b, err := New(root)
	if err != nil {
		t.Fatalf("comms.New: %v", err)
	}

	cases := []struct {
		name      string
		req       dispatchRequest
		wantState string
		wantTask  string
		wantNote  string
	}{
		{"set task", dispatchRequest{Task: "task-xyz", TaskAction: "set"}, "working", "task-xyz", ""},
		{"done", dispatchRequest{Task: "task-xyz", TaskAction: "done"}, "idle", "", "task done: task-xyz"},
		{"blocked", dispatchRequest{Task: "task-xyz", TaskAction: "blocked"}, "blocked", "", "task blocked: task-xyz"},
		{"yielded", dispatchRequest{Task: "task-xyz", TaskAction: "yielded"}, "idle", "", "task yielded: task-xyz"},
		{"clear", dispatchRequest{TaskAction: "clear"}, "idle", "", ""},
		{"bare (no args)", dispatchRequest{}, "idle", "", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := b.dispatchTask(tc.req)
			if !resp.OK {
				t.Fatalf("dispatchTask(%+v) not ok: %s", tc.req, resp.Error)
			}
			rec := readStatus(t, b, "test-ship")
			if rec.State != tc.wantState {
				t.Errorf("state = %q, want %q", rec.State, tc.wantState)
			}
			if rec.Task != tc.wantTask {
				t.Errorf("task = %q, want %q", rec.Task, tc.wantTask)
			}
			if rec.Note != tc.wantNote {
				t.Errorf("note = %q, want %q", rec.Note, tc.wantNote)
			}
		})
	}
}

func TestDispatchTaskUnknownActionRejected(t *testing.T) {
	root := newCommsTestRoot(t)
	os.Setenv("STARFLEET_SHIP_ID", "test-ship")
	defer os.Unsetenv("STARFLEET_SHIP_ID")

	b, err := New(root)
	if err != nil {
		t.Fatalf("comms.New: %v", err)
	}

	resp := b.dispatchTask(dispatchRequest{TaskAction: "bogus"})
	if resp.OK {
		t.Error("dispatchTask with unknown action should be rejected")
	}
	if !strings.Contains(resp.Error, "unknown task action") {
		t.Errorf("error = %q, want unknown task action hint", resp.Error)
	}
}

func TestDecideAction_retryTransient(t *testing.T) {
	cases := []struct {
		tag         string
		hasFallback bool
	}{
		{"nim-overload", false},
		{"nim-overload", true},
		{"streaming-response-failed", false},
		{"streaming-response-failed", true},
		{"resource-exhausted", false},
		{"resource-exhausted", true},
		{"no-provider", false},
		{"no-provider", true},
	}
	for _, tc := range cases {
		name := tc.tag
		if tc.hasFallback {
			name += "+fallback"
		}
		t.Run(name, func(t *testing.T) {
			action, reason := decideAction(tc.tag, tc.hasFallback, "retry-poll", "")
			if action != "retry" {
				t.Errorf("decideAction(%q, %v, \"retry-poll\") = %q, want \"retry\"; reason=%q",
					tc.tag, tc.hasFallback, action, reason)
			}
			if reason == "" {
				t.Error("decideAction returned empty reason")
			}
		})
	}
}

func TestDecideAction_switchModel(t *testing.T) {
	// Create temp dir with fallback model configured
	tmpDir := t.TempDir()
	confDir := filepath.Join(tmpDir, ".starfleet-ai", "conf")
	if err := os.MkdirAll(confDir, 0755); err != nil {
		t.Fatal(err)
	}
	commsYaml := `comms:
  fallback_model: "opencode/test-fallback"
`
	if err := os.WriteFile(filepath.Join(confDir, "comms.yaml"), []byte(commsYaml), 0644); err != nil {
		t.Fatal(err)
	}

	action, reason := decideAction("zen-ratelimit", true, "retry-poll", tmpDir)
	if action != "switch-model" {
		t.Errorf("decideAction(\"zen-ratelimit\", true, \"retry-poll\", tmpDir) = %q, want \"switch-model\"; reason=%q",
			action, reason)
	}
	if reason == "" {
		t.Error("decideAction returned empty reason")
	}
}

func TestDecideAction_zenRatelimitNoFallback(t *testing.T) {
	action, reason := decideAction("zen-ratelimit", false, "retry-poll", "")
	if action != "retry" {
		t.Errorf("decideAction(\"zen-ratelimit\", false, \"retry-poll\") = %q, want \"retry\"; reason=%q",
			action, reason)
	}
	if reason == "" {
		t.Error("decideAction returned empty reason")
	}
}

func TestDecideAction_ignoreUnknown(t *testing.T) {
	action, reason := decideAction("", false, "session-error", "")
	if action != "ignore" {
		t.Errorf("decideAction(\"\", false, \"session-error\") = %q, want \"ignore\"; reason=%q",
			action, reason)
	}
	if reason == "" {
		t.Error("decideAction returned empty reason")
	}
}

func TestDecideAction_retryLogMonitorUnknown(t *testing.T) {
	action, reason := decideAction("", false, "log-monitor", "")
	if action != "retry" {
		t.Errorf("decideAction(\"\", false, \"log-monitor\") = %q, want \"retry\"; reason=%q",
			action, reason)
	}
	if reason == "" {
		t.Error("decideAction returned empty reason")
	}
}
