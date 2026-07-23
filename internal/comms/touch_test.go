// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package comms

import (
	"os"
	"testing"
	"time"
)

func newTestBus(t *testing.T, agentID string) *Bus {
	t.Helper()
	root := t.TempDir()
	os.Setenv("STARFLEET_SHIP_ID", agentID)
	os.Unsetenv("STARFLEET_BUS_DIR")
	os.Unsetenv("STARFLEET_BUS_TTL")
	os.Unsetenv("PROJECT")
	os.Unsetenv("STARFLEET_AGENT_HANDLE")
	t.Cleanup(func() { os.Unsetenv("STARFLEET_SHIP_ID") })
	b, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestDoTouchNoopWithoutExistingHeartbeat(t *testing.T) {
	b := newTestBus(t, "TestShip")
	if err := b.DoTouch(); err != nil {
		t.Fatalf("DoTouch on a never-posted ship should be a silent no-op, got: %v", err)
	}
	if _, err := os.Stat(b.sfile(b.ShipID)); !os.IsNotExist(err) {
		t.Errorf("DoTouch should not create a heartbeat file out of nothing: err=%v", err)
	}
}

func TestDoTouchRefreshesTimestampOnly(t *testing.T) {
	b := newTestBus(t, "TestShip")

	if err := b.DoStatus("working", "on it", StatusPatch{}); err != nil {
		t.Fatal(err)
	}
	before, ok := parseStatusFile(b.sfile(b.ShipID))
	if !ok {
		t.Fatal("expected a status file after DoStatus")
	}

	time.Sleep(1100 * time.Millisecond) // now() has 1s resolution
	if err := b.DoTouch(); err != nil {
		t.Fatal(err)
	}
	after, ok := parseStatusFile(b.sfile(b.ShipID))
	if !ok {
		t.Fatal("expected a status file after DoTouch")
	}

	if after.Epoch <= before.Epoch {
		t.Errorf("DoTouch did not advance the timestamp: before=%d after=%d", before.Epoch, after.Epoch)
	}
	if after.State != before.State || after.Note != before.Note ||
		after.Project != before.Project || after.Handle != before.Handle || after.PID != before.PID {
		t.Errorf("DoTouch changed a field it shouldn't have: before=%+v after=%+v", before, after)
	}
}

// TestDoTouchPicksUpLatestRealStatusNotACache is the race-safety property
// the design explicitly rests on: DoTouch must never hold or reuse a
// remembered state+note value — it has to re-read the current on-disk
// record every time, so a real DoStatus call that happened since the last
// touch is what gets its timestamp refreshed, never something older.
func TestDoTouchPicksUpLatestRealStatusNotACache(t *testing.T) {
	b := newTestBus(t, "TestShip")

	if err := b.DoStatus("working", "first task", StatusPatch{}); err != nil {
		t.Fatal(err)
	}
	// Simulate a real status change happening between two touch cycles.
	if err := b.DoStatus("blocked", "waiting on review", StatusPatch{}); err != nil {
		t.Fatal(err)
	}
	if err := b.DoTouch(); err != nil {
		t.Fatal(err)
	}

	rec, ok := parseStatusFile(b.sfile(b.ShipID))
	if !ok {
		t.Fatal("expected a status file")
	}
	if rec.State != "blocked" || rec.Note != "waiting on review" {
		t.Errorf("DoTouch did not reflect the latest real status: got state=%q note=%q", rec.State, rec.Note)
	}
}
