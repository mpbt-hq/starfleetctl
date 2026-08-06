// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package comms

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestPostBroadcastFansOut verifies that a broadcast (target "all") is fanned
// out at post time: one per-ship copy into every known ship's unseen/ dir,
// each carrying the concrete recipient in its Target field — no shared
// msgs/all/ pseudo-target anymore.
func TestPostBroadcastFansOut(t *testing.T) {
	b := newTestBus(t, "Enterprise")

	// Second known ship (a message dir exists).
	if err := os.MkdirAll(filepath.Join(b.MsgDir, "Defiant", "unseen"), 0o755); err != nil {
		t.Fatal(err)
	}

	id, err := b.post("all", "hello fleet", "", "", "", "ship")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(b.MsgDir, "all")); !os.IsNotExist(err) {
		t.Errorf("msgs/all must not exist in fan-out model: err=%v", err)
	}
	for _, ship := range []string{"Enterprise", "Defiant"} {
		p := filepath.Join(b.MsgDir, ship, "unseen", id+".json")
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("broadcast copy for %s missing: %v", ship, err)
		}
		if m, ok := parseMsgFile(id, p); !ok || m.Target != ship {
			t.Errorf("copy for %s should carry Target=%s, got %#v", ship, ship, m)
		}
	}
}

// TestDoAckBroadcast verifies that a fanned-out broadcast (now a per-ship copy
// in my own unseen/ dir) is acked by a plain move to my seen/ dir.
func TestDoAckBroadcast(t *testing.T) {
	b := newTestBus(t, "Enterprise")

	id, err := b.post("all", "hello fleet", "", "", "", "ship")
	if err != nil {
		t.Fatal(err)
	}
	myUnseen := filepath.Join(b.MsgDir, b.ShipID, "unseen", id+".json")
	if _, err := os.Stat(myUnseen); err != nil {
		t.Fatalf("broadcast copy should live in my unseen/: %v", err)
	}

	if err := b.DoAck(id, "test note"); err != nil {
		t.Fatalf("DoAck on a broadcast must succeed, got: %v", err)
	}

	if !b.acked(id, b.ShipID) {
		t.Error("message should be acked for this ship after DoAck")
	}
	if _, err := os.Stat(myUnseen); !os.IsNotExist(err) {
		t.Errorf("broadcast copy should be moved out of unseen/, still there: err=%v", err)
	}
	seenPath, err := b.mfileSeen(b.ShipID, id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(seenPath); err != nil {
		t.Errorf("expected seen/ copy after broadcast ack: %v", err)
	}
}

// TestDoAckTargeted verifies the pre-existing targeted path still works: the
// message is moved (not copied) from my unseen/ dir to my seen/ dir.
func TestDoAckTargeted(t *testing.T) {
	b := newTestBus(t, "Enterprise")

	id, err := b.post(b.ShipID, "hello me", "", "", "", "ship")
	if err != nil {
		t.Fatal(err)
	}
	myUnseen := filepath.Join(b.MsgDir, b.ShipID, "unseen", id+".json")
	if _, err := os.Stat(myUnseen); err != nil {
		t.Fatalf("targeted message should live in my unseen/: %v", err)
	}

	if err := b.DoAck(id, ""); err != nil {
		t.Fatalf("DoAck on a targeted message must succeed, got: %v", err)
	}

	if !b.acked(id, b.ShipID) {
		t.Error("message should be acked for this ship after DoAck")
	}
	if _, err := os.Stat(myUnseen); !os.IsNotExist(err) {
		t.Errorf("targeted message should be moved out of unseen/, still there: err=%v", err)
	}
	seenPath, err := b.mfileSeen(b.ShipID, id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(seenPath); err != nil {
		t.Errorf("expected seen/ copy after targeted ack: %v", err)
	}
}

// TestDoAckUnknown confirms a bogus id still yields the usage error.
func TestDoAckUnknown(t *testing.T) {
	b := newTestBus(t, "Enterprise")
	if err := b.DoAck("m999999", ""); err == nil {
		t.Fatal("expected error for unknown message id, got nil")
	}
}

// TestDoInitAcksFannedOutBroadcast verifies the startup path acking also
// handles fanned-out broadcasts: the per-ship copy is moved into seen/.
func TestDoInitAcksFannedOutBroadcast(t *testing.T) {
	b := newTestBus(t, "Enterprise")

	id, err := b.post("all", "hello fleet", "", "", "", "ship")
	if err != nil {
		t.Fatal(err)
	}
	myUnseen := filepath.Join(b.MsgDir, b.ShipID, "unseen", id+".json")

	if _, err := b.DoInit("startup"); err != nil {
		t.Fatal(err)
	}

	if !b.acked(id, b.ShipID) {
		t.Error("broadcast should be acked for this ship after DoInit")
	}
	if _, err := os.Stat(myUnseen); !os.IsNotExist(err) {
		t.Errorf("broadcast copy should be moved out of unseen/ after DoInit: err=%v", err)
	}
}

// TestMigrateBroadcasts verifies the one-off conversion of a legacy shared
// broadcast (msgs/all/unseen/<id>.json, Target "all") into per-ship copies,
// and that the msgs/all/ tree is removed. Idempotent on a second run.
func TestMigrateBroadcasts(t *testing.T) {
	b := newTestBus(t, "Enterprise")

	// Second known ship.
	if err := os.MkdirAll(filepath.Join(b.MsgDir, "Defiant", "unseen"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Legacy shared broadcast, exactly as the old code posted it.
	legacy := msgRecord{ID: "m12507", Epoch: now(), ISO: isots(), From: "Defiant", Target: "all", Text: "old broadcast"}
	data, _ := json.Marshal(legacy)
	legacyDir := filepath.Join(b.MsgDir, "all", "unseen")
	if err := os.MkdirAll(legacyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacyDir, "m12507.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := b.DoMigrateBroadcasts(); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(b.MsgDir, "all")); !os.IsNotExist(err) {
		t.Errorf("msgs/all should be removed after migration: err=%v", err)
	}
	for _, ship := range []string{"Enterprise", "Defiant"} {
		p := filepath.Join(b.MsgDir, ship, "unseen", "m12507.json")
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("migrated copy for %s missing: %v", ship, err)
		}
		if m, ok := parseMsgFile("m12507", p); !ok || m.Target != ship {
			t.Errorf("migrated copy for %s should carry Target=%s, got %#v", ship, ship, m)
		}
	}

	// A second run is a harmless no-op.
	if err := b.DoMigrateBroadcasts(); err != nil {
		t.Fatal(err)
	}
}
