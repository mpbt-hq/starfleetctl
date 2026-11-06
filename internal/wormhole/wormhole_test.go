// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
package wormhole

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.ListenAddr != "127.0.0.1:9999" {
		t.Errorf("Default ListenAddr = %q, want %q", cfg.ListenAddr, "127.0.0.1:9999")
	}
	if cfg.PIDFile != "pid" {
		t.Errorf("Default PIDFile = %q, want %q", cfg.PIDFile, "pid")
	}
	if cfg.StateFile != "state" {
		t.Errorf("Default StateFile = %q, want %q", cfg.StateFile, "state")
	}
	if cfg.MaxMessageAge != time.Hour {
		t.Errorf("Default MaxMessageAge = %v, want %v", cfg.MaxMessageAge, time.Hour)
	}
	if cfg.AutoStart != true {
		t.Errorf("Default AutoStart = %v, want %v", cfg.AutoStart, true)
	}
	if cfg.ForwardMode != "targeted" {
		t.Errorf("Default ForwardMode = %q, want %q", cfg.ForwardMode, "targeted")
	}
	if cfg.WorkerPollInterval != 3*time.Second {
		t.Errorf("Default WorkerPollInterval = %v, want %v", cfg.WorkerPollInterval, 3*time.Second)
	}
	if cfg.MaxConcurrent != 5 {
		t.Errorf("Default MaxConcurrent = %v, want %v", cfg.MaxConcurrent, 5)
	}
	if cfg.PersistState != true {
		t.Errorf("Default PersistState = %v, want %v", cfg.PersistState, true)
	}
}

func TestNewMessage(t *testing.T) {
	msg := NewMessage("ShipA", "ShipB", "test", "hello world")
	if msg.From != "ShipA" {
		t.Errorf("Message.From = %q, want %q", msg.From, "ShipA")
	}
	if msg.To != "ShipB" {
		t.Errorf("Message.To = %q, want %q", msg.To, "ShipB")
	}
	if msg.Content != "hello world" {
		t.Errorf("Message.Content = %q, want %q", msg.Content, "hello world")
	}
	if msg.State != "pending" {
		t.Errorf("Message.State = %q, want %q", msg.State, "pending")
	}
	if msg.ID == "" {
		t.Error("Message.ID should not be empty")
	}
	if msg.Timestamp.IsZero() {
		t.Error("Message.Timestamp should not be zero")
	}
}

func TestPIDFile(t *testing.T) {
	root := "/tmp"
	pf := PIDFile(root)
	expected := filepath.Join(root, ".starfleet-ai", "var", "wormholes", "pid")
	if pf != expected {
		t.Errorf("PIDFile(%q) = %q, want %q", root, pf, expected)
	}
}

func TestStateFile(t *testing.T) {
	root := "/tmp"
	sf := StateFile(root)
	expected := filepath.Join(root, ".starfleet-ai", "var", "wormholes", "state")
	if sf != expected {
		t.Errorf("StateFile(%q) = %q, want %q", root, sf, expected)
	}
}

func TestConfigFile(t *testing.T) {
	root := "/tmp"
	cf := ConfigFile(root)
	expected := filepath.Join(root, ".starfleet-ai", "conf", "wormholes.yaml")
	if cf != expected {
		t.Errorf("ConfigFile(%q) = %q, want %q", root, cf, expected)
	}
}

func TestMessageQueue(t *testing.T) {
	q := NewMessageQueue()
	if q == nil {
		t.Fatal("NewMessageQueue returned nil")
	}

	msg := NewMessage("ShipA", "ShipB", "test", "test message")
	q.Enqueue(msg)
	pending := q.ListPending()
	if len(pending) != 1 {
		t.Errorf("ListPending returned %d items, want 1", len(pending))
	}

	dequeued := q.Dequeue()
	if dequeued == nil {
		t.Fatal("Dequeue returned nil")
	}
	if dequeued.State != "delivered" {
		t.Errorf("Dequeued message state = %q, want %q", dequeued.State, "delivered")
	}

	pending = q.ListPending()
	if len(pending) != 0 {
		t.Errorf("ListPending returned %d items, want 0 after dequeue", len(pending))
	}

	empty := q.Dequeue()
	if empty != nil {
		t.Errorf("Dequeue from empty queue returned non-nil message")
	}
}

func TestRouteMessage(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()

	msg := NewMessage("ShipA", "ShipB", "test", "test content")
	if err := RouteMessage(root, msg); err != nil {
		t.Fatalf("RouteMessage failed: %v", err)
	}

	targetDir := filepath.Join(root, ".starfleet-ai", "var", "comms", "msgs", "ShipB", "unseen")
	if _, err := os.Stat(targetDir); err != nil {
		t.Errorf("Target dir not created: %v", err)
	}

	msgFile := filepath.Join(targetDir, msg.ID+".json")
	if _, err := os.Stat(msgFile); err != nil {
		t.Errorf("Message file not created: %v", err)
	}

	if msg.State != "delivered" {
		t.Errorf("Message state = %q, want %q", msg.State, "delivered")
	}
}

func TestStartStopDaemon(t *testing.T) {
	root := t.TempDir()

	if err := StartDaemon(root); err != nil {
		t.Fatalf("StartDaemon failed: %v", err)
	}

	status := Status()
	if !contains(status, "running") {
		t.Errorf("Status should show running, got: %s", status)
	}

	if err := StopDaemon(); err != nil {
		t.Fatalf("StopDaemon failed: %v", err)
	}

	status = Status()
	if !contains(status, "not running") {
		t.Errorf("Status should show not running, got: %s", status)
	}
}

func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}