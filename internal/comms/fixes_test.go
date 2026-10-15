// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package comms

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestDoAckIdempotent: acking an already-seen message is a successful no-op,
// not a "no such directive" error. The poller marks a directive seen when it
// delivers it, so a ship following the SOP ("ack each directive") would
// otherwise get an error indistinguishable from a bogus id.
func TestDoAckIdempotent(t *testing.T) {
	b := newTestBus(t, "Enterprise")
	id, err := b.post(b.ShipID, "x", "", "", "", "ship")
	if err != nil {
		t.Fatal(err)
	}
	if err := b.DoAck(id, ""); err != nil {
		t.Fatalf("first ack: %v", err)
	}
	if err := b.DoAck(id, ""); err != nil {
		t.Fatalf("second (idempotent) ack must succeed, got: %v", err)
	}
	if !b.acked(id, b.ShipID) {
		t.Error("message should remain acked")
	}
}

// writeMsgAt writes a message file for ship with a specific mtime, to emulate
// store history (including an id counter reset, where a newer message has a
// LOWER id than an older one).
func writeMsgAt(t *testing.T, b *Bus, dir, id, target string, mtime time.Time) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	rec := msgRecord{ID: id, Epoch: mtime.Unix(), ISO: isots(), From: "Defiant", Target: target, Text: id}
	data, _ := json.Marshal(rec)
	p := filepath.Join(dir, id+".json")
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

// TestConversationRecentByTime: with an id counter reset (newer messages carry
// LOWER ids), the "newest N" selection must order by time (mtime), not by id.
func TestConversationRecentByTime(t *testing.T) {
	b := newTestBus(t, "Enterprise")
	dir := filepath.Join(b.MsgDir, b.ShipID, "seen")

	writeMsgAt(t, b, dir, "m9000", b.ShipID, time.Now().Add(-48*time.Hour))  // old
	writeMsgAt(t, b, dir, "m0010", b.ShipID, time.Now().Add(-1*time.Minute)) // new

	got := b.ConversationRecentRecords(b.ShipID, 1)
	if len(got) != 1 {
		t.Fatalf("want 1 record, got %d", len(got))
	}
	if got[0].ID != "m0010" {
		t.Errorf("recent selection must pick the newest by time (m0010), got %s", got[0].ID)
	}
}

// TestNextIDMonotonicAfterSeqReset: a lost/reset .seq must not make nextID
// hand out an id that collides with (or sorts before) an existing message.
func TestNextIDMonotonicAfterSeqReset(t *testing.T) {
	b := newTestBus(t, "Enterprise")
	seen := filepath.Join(b.MsgDir, b.ShipID, "seen")
	if err := os.MkdirAll(seen, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(seen, "m4000.json"), []byte(`{"id":"m4000"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(b.MsgDir, ".seq"), []byte("3\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	lock, err := b.lockBus()
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	id, err := b.nextID()
	if err != nil {
		t.Fatal(err)
	}
	if parseIDNum(id) <= 4000 {
		t.Errorf("nextID must stay above the highest existing id (4000), got %s", id)
	}
}

// TestPruneRemovesSpentSeenCopy: a spent directive lives in seen/ after being
// acked; prune must actually delete it there (the old code only probed the
// unseen/ path, so it counted but never removed it).
func TestPruneRemovesSpentSeenCopy(t *testing.T) {
	b := newTestBus(t, "Enterprise")
	id, err := b.post(b.ShipID, "spent", "", "", "", "ship")
	if err != nil {
		t.Fatal(err)
	}
	if err := b.DoAck(id, ""); err != nil {
		t.Fatal(err)
	}
	seen, err := b.mfileSeen(b.ShipID, id)
	if err != nil {
		t.Fatal(err)
	}

	// Backdate it past BusTTL so it counts as stale.
	old := time.Now().Add(-2 * time.Hour)
	data, _ := json.Marshal(msgRecord{ID: id, Epoch: old.Unix(), From: b.ShipID, Target: b.ShipID, Text: "spent"})
	if err := os.WriteFile(seen, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(seen, old, old); err != nil {
		t.Fatal(err)
	}

	if err := b.DoPrune(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(seen); !os.IsNotExist(err) {
		t.Errorf("prune must delete the spent seen/ copy, still present: err=%v", err)
	}
}

// TestCommsSubcommandHelpDoesNotExecute: `comms clear --help` must print help,
// not run the destructive clear (which would drop the caller's heartbeat).
func TestCommsSubcommandHelpDoesNotExecute(t *testing.T) {
	root := t.TempDir()
	os.Setenv("STARFLEET_SHIP_ID", "Enterprise")
	os.Unsetenv("STARFLEET_BUS_DIR")
	os.Unsetenv("STARFLEET_BUS_TTL")
	t.Cleanup(func() { os.Unsetenv("STARFLEET_SHIP_ID") })

	b, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.DoStatus("idle", "up", StatusPatch{}); err != nil {
		t.Fatal(err)
	}

	if rc := Run(root, []string{"clear", "--help"}); rc != 0 {
		t.Errorf("`clear --help` should print help (rc=0), got rc=%d", rc)
	}
	b2, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(b2.AllStatusRecords()) == 0 {
		t.Error("heartbeat must survive `comms clear --help`")
	}

	// A stray argument must be refused rather than silently run.
	if rc := Run(root, []string{"clear", "surprise"}); rc == 0 {
		t.Error("`clear <arg>` should be refused (non-zero rc)")
	}
}
