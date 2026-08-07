// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package comms

import (
	"path/filepath"
	"testing"
)

// TestPostFromAttributesSender verifies that PostFrom records the explicit
// sender as the message's From field instead of the bus identity — this is
// how the timer worker attributes a timer message to the ship that created
// the timer rather than to the (anonymous) worker daemon.
func TestPostFromAttributesSender(t *testing.T) {
	b := newTestBus(t, "Enterprise")

	if err := b.PostFrom("Voyager", "Defiant", "[timer] check CI of PR #3491"); err != nil {
		t.Fatal(err)
	}

	// Locate the message in Defiant's unseen dir and check From == Voyager.
	entries, err := filepath.Glob(filepath.Join(b.MsgDir, "Defiant", "unseen", "*.json"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("expected exactly one message for Defiant, got %d (err=%v)", len(entries), err)
	}
	m, ok := parseMsgFile("", entries[0])
	if !ok {
		t.Fatalf("cannot parse posted message %s", entries[0])
	}
	if m.From != "Voyager" {
		t.Errorf("From = %q, want %q", m.From, "Voyager")
	}
	if m.Text != "[timer] check CI of PR #3491" {
		t.Errorf("Text = %q, want the marker prefixed text intact", m.Text)
	}
	if m.Type != "ship" {
		t.Errorf("Type = %q, want ship", m.Type)
	}
}

// TestPostFromIgnoresBusIdentity verifies that even when the bus identity
// differs, the explicit sender wins (the worker runs as an anonymous daemon,
// so the default b.ShipID must never leak into timer messages).
func TestPostFromIgnoresBusIdentity(t *testing.T) {
	b := newTestBus(t, "Enterprise")

	if err := b.PostFrom("McKinley", "Enterprise", "[timer] status?"); err != nil {
		t.Fatal(err)
	}

	// Self-targeted: message lands in Enterprise's own unseen dir.
	entries, err := filepath.Glob(filepath.Join(b.MsgDir, "Enterprise", "unseen", "*.json"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("expected exactly one message for Enterprise, got %d (err=%v)", len(entries), err)
	}
	m, ok := parseMsgFile("", entries[0])
	if !ok {
		t.Fatalf("cannot parse posted message %s", entries[0])
	}
	if m.From == b.ShipID {
		t.Errorf("From must not be the bus identity %q, want the explicit sender", b.ShipID)
	}
	if m.From != "McKinley" {
		t.Errorf("From = %q, want %q", m.From, "McKinley")
	}
}

// TestCommandFromAttributesSender verifies command-type messages get the
// explicit sender too (used for command timers, e.g. model switches).
func TestCommandFromAttributesSender(t *testing.T) {
	b := newTestBus(t, "Enterprise")

	id, err := b.CommandFrom("Voyager", "Defiant", "model", "big-pickle")
	if err != nil {
		t.Fatal(err)
	}
	m, ok := parseMsgFile(id, filepath.Join(b.MsgDir, "Defiant", "unseen", id+".json"))
	if !ok {
		t.Fatalf("cannot parse posted command message")
	}
	if m.From != "Voyager" {
		t.Errorf("From = %q, want %q", m.From, "Voyager")
	}
	if m.Type != "command" {
		t.Errorf("Type = %q, want command", m.Type)
	}
	if m.Text != "model big-pickle" {
		t.Errorf("Text = %q, want %q", m.Text, "model big-pickle")
	}
}
