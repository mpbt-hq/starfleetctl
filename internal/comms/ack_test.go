// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package comms

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDoAckBroadcast verifies that a broadcast (target "all") message can be
// acked. The shared copy in all/unseen/ must survive (other ships still need
// it), and the ack is registered by a copy in my own seen/ dir.
func TestDoAckBroadcast(t *testing.T) {
	b := newTestBus(t, "Enterprise")

	id, err := b.post("all", "hello fleet", "", "", "", "ship")
	if err != nil {
		t.Fatal(err)
	}
	sharedPath := filepath.Join(b.MsgDir, "all", "unseen", id+".json")
	if _, err := os.Stat(sharedPath); err != nil {
		t.Fatalf("broadcast should live in all/unseen/: %v", err)
	}

	if err := b.DoAck(id, "test note"); err != nil {
		t.Fatalf("DoAck on a broadcast must succeed, got: %v", err)
	}

	if !b.acked(id, b.ShipID) {
		t.Error("message should be acked for this ship after DoAck")
	}
	// The shared broadcast copy must remain for the other ships.
	if _, err := os.Stat(sharedPath); err != nil {
		t.Errorf("broadcast shared copy must stay in all/unseen/: %v", err)
	}
	// The ack is recorded via my own seen/ copy.
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

// TestDoInitBroadcastCopiesIntoSeen verifies the startup path acking also
// handles broadcasts: it must copy (not move) the shared all/unseen/ file.
func TestDoInitBroadcastCopiesIntoSeen(t *testing.T) {
	b := newTestBus(t, "Enterprise")

	id, err := b.post("all", "hello fleet", "", "", "", "ship")
	if err != nil {
		t.Fatal(err)
	}
	sharedPath := filepath.Join(b.MsgDir, "all", "unseen", id+".json")

	if _, err := b.DoInit("startup"); err != nil {
		t.Fatal(err)
	}

	if !b.acked(id, b.ShipID) {
		t.Error("broadcast should be acked for this ship after DoInit")
	}
	if _, err := os.Stat(sharedPath); err != nil {
		t.Errorf("broadcast shared copy must stay in all/unseen/ after DoInit: %v", err)
	}
}
