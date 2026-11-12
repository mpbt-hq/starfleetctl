// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package comms

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// TestHelperProcess is not a real test: the tests below re-execute the test
// binary with GO_WANT_HELPER_PROCESS=1 to obtain a live (blocking) or dead
// (exiting) helper process. This avoids any dependency on external binaries
// such as sleep/true, so the PID tests run on every platform the package
// builds on (the package itself needs syscall.Kill/Flock, i.e. Unix).
func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	if os.Getenv("GO_HELPER_MODE") == "live" {
		select {} // block until the parent kills us
	}
	os.Exit(0)
}

// spawnHelper starts the test binary re-executed as a helper process.
func spawnHelper(t *testing.T, mode string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$")
	cmd.Env = append(os.Environ(), "GO_WANT_HELPER_PROCESS=1", "GO_HELPER_MODE="+mode)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper process: %v", err)
	}
	return cmd
}

// livePID returns the PID of a still-running helper process. It stands in for
// the long-lived ship process (the opencode process), whose PID is the one
// that proves the ship is alive.
func livePID(t *testing.T) int {
	t.Helper()
	cmd := spawnHelper(t, "live")
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	return cmd.Process.Pid
}

// deadPID returns the PID of a helper process that has already exited.
func deadPID(t *testing.T) int {
	t.Helper()
	cmd := spawnHelper(t, "dead")
	if err := cmd.Wait(); err != nil {
		t.Fatalf("helper process did not exit cleanly: %v", err)
	}
	return cmd.Process.Pid
}

// writeStatusRecord writes a status record for agent verbatim.
func writeStatusRecord(t *testing.T, b *Bus, agent string, rec StatusRecord) {
	t.Helper()
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(b.StatusDir, agent+".json")
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestDoStatusKeepsLiveShipPID proves that a status update written by a
// short-lived helper process does not replace the PID of the still-running
// ship process: the helper exits right after the write, so installing its own
// PID would make the ship look dead.
func TestDoStatusKeepsLiveShipPID(t *testing.T) {
	b := newTestBus(t, "TestShip")
	shipPID := livePID(t)
	writeStatusRecord(t, b, "TestShip", StatusRecord{
		Epoch:         time.Now().Unix(),
		ISO:           time.Now().UTC().Format(time.RFC3339),
		Agent:         "TestShip",
		State:         "working",
		PID:           shipPID,
		PluginLastRun: "2026-01-01T00:00:00Z",
	})

	if err := b.DoStatus("working", "still working", StatusPatch{}); err != nil {
		t.Fatal(err)
	}
	rec, ok := parseStatusFile(b.sfile(b.ShipID))
	if !ok {
		t.Fatal("no status file after DoStatus")
	}
	if rec.PID != shipPID {
		t.Fatalf("short-lived writer replaced the live ship PID: got %d want %d", rec.PID, shipPID)
	}
	if rec.PluginLastRun == "" {
		t.Fatalf("plugin liveness field lost: %+v", rec)
	}
}

// TestDoInitKeepsLiveShipPIDAndFields proves that the ack path (comms init)
// keeps the PID of the live ship process and does not drop the existing
// plugin-liveness and status fields.
func TestDoInitKeepsLiveShipPIDAndFields(t *testing.T) {
	b := newTestBus(t, "TestShip")
	shipPID := livePID(t)
	writeStatusRecord(t, b, "TestShip", StatusRecord{
		Epoch:           time.Now().Unix(),
		ISO:             time.Now().UTC().Format(time.RFC3339),
		Agent:           "TestShip",
		State:           "working",
		PID:             shipPID,
		PluginLastRun:   "2026-01-01T00:00:00Z",
		ModelLastAction: "2026-01-01T00:00:01Z",
		Task:            "task-x",
		Progress:        42,
		PluginVersion:   "v1",
		Model:           "some/model",
	})

	if _, err := b.DoInit("acked"); err != nil {
		t.Fatal(err)
	}
	rec, ok := parseStatusFile(b.sfile(b.ShipID))
	if !ok {
		t.Fatal("no status file after DoInit")
	}
	if rec.PID != shipPID {
		t.Fatalf("ack path replaced the live ship PID: got %d want %d", rec.PID, shipPID)
	}
	if rec.PluginLastRun == "" || rec.ModelLastAction == "" || rec.PluginVersion != "v1" {
		t.Fatalf("ack path dropped plugin liveness fields: %+v", rec)
	}
	if rec.Task != "task-x" || rec.Progress != 42 || rec.Model != "some/model" {
		t.Fatalf("ack path dropped status fields: %+v", rec)
	}
	if rec.State != "idle" {
		t.Fatalf("ack path must still set the state to idle: %+v", rec)
	}
}

// TestDoHealthUpdateTakesExplicitPID proves that a PID transmitted explicitly
// by the ship process itself is authoritative and replaces the recorded one.
func TestDoHealthUpdateTakesExplicitPID(t *testing.T) {
	b := newTestBus(t, "TestShip")
	oldPID := livePID(t)
	newPID := livePID(t)
	writeStatusRecord(t, b, "TestShip", StatusRecord{
		Epoch: time.Now().Unix(),
		ISO:   time.Now().UTC().Format(time.RFC3339),
		Agent: "TestShip",
		State: "working",
		PID:   oldPID,
	})

	if err := b.DoHealthUpdate([]string{
		"--state", "working",
		"--pid", strconv.Itoa(newPID),
		"--plugin-ts", "2026-01-01T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	rec, ok := parseStatusFile(b.sfile(b.ShipID))
	if !ok {
		t.Fatal("no status file after DoHealthUpdate")
	}
	if rec.PID != newPID {
		t.Fatalf("explicitly transmitted PID not taken over: got %d want %d", rec.PID, newPID)
	}
	if rec.PluginLastRun != "2026-01-01T00:00:00Z" {
		t.Fatalf("plugin liveness field not stored: %+v", rec)
	}
}

// TestActiveStatusRecordsFiltersEndedShip proves that a ship whose recorded
// PID is no longer alive is filtered out of ActiveStatusRecords, while a ship
// with a live PID is kept.
func TestActiveStatusRecordsFiltersEndedShip(t *testing.T) {
	b := newTestBus(t, "TestShip")
	live := livePID(t)
	writeStatusRecord(t, b, "Live", StatusRecord{
		Epoch: time.Now().Unix(),
		ISO:   time.Now().UTC().Format(time.RFC3339),
		Agent: "Live",
		State: "idle",
		PID:   live,
	})
	writeStatusRecord(t, b, "Ended", StatusRecord{
		Epoch: time.Now().Unix(),
		ISO:   time.Now().UTC().Format(time.RFC3339),
		Agent: "Ended",
		State: "idle",
		PID:   deadPID(t),
	})

	active := make(map[string]bool)
	for _, r := range b.ActiveStatusRecords() {
		active[r.Agent] = true
	}
	if !active["Live"] {
		t.Fatalf("ship with a live PID must be active: %v", active)
	}
	if active["Ended"] {
		t.Fatalf("ship with a dead PID must not be active: %v", active)
	}
}
