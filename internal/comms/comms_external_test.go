// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
package comms

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestPostExternal tests basic PostExternal functionality.
func TestPostExternal(t *testing.T) {
	root := t.TempDir()
	b, err := New(root)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	// Test basic post
	id, err := b.PostExternal("fleet-comms", "W0-001", "ShipA", "ShipB", "test message")
	if err != nil {
		t.Fatalf("PostExternal failed: %v", err)
	}
	if id == "" {
		t.Fatal("PostExternal returned empty ID")
	}

	// Verify external record was created
	rec, err := loadExternalRecordLocked(b, "fleet-comms", "W0-001")
	if err != nil {
		t.Fatalf("loadExternalRecordLocked failed: %v", err)
	}
	if rec == nil {
		t.Fatal("loadExternalRecordLocked returned nil")
	}
	if rec.Source != "fleet-comms" {
		t.Errorf("rec.Source = %q, want %q", rec.Source, "fleet-comms")
	}
	if rec.ExternalID != "W0-001" {
		t.Errorf("rec.ExternalID = %q, want %q", rec.ExternalID, "W0-001")
	}
	if rec.From != "ShipA" {
		t.Errorf("rec.From = %q, want %q", rec.From, "ShipA")
	}
	if rec.Target != "ShipB" {
		t.Errorf("rec.Target = %q, want %q", rec.Target, "ShipB")
	}
	if rec.Payload != "test message" {
		t.Errorf("rec.Payload = %q, want %q", rec.Payload, "test message")
	}
	if rec.CommsID == "" {
		t.Error("rec.CommsID should not be empty")
	}
	if rec.State != externalComplete {
		t.Errorf("rec.State = %q, want %q", rec.State, externalComplete)
	}
}

// TestPostExternalIdempotent tests that calling PostExternal with the same
// source/externalID returns the same CommsID without creating duplicate messages.
func TestPostExternalIdempotent(t *testing.T) {
	root := t.TempDir()
	b, err := New(root)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	id1, err := b.PostExternal("fleet-comms", "W0-001", "ShipA", "ShipB", "test message")
	if err != nil {
		t.Fatalf("PostExternal failed: %v", err)
	}

	id2, err := b.PostExternal("fleet-comms", "W0-001", "ShipA", "ShipB", "test message")
	if err != nil {
		t.Fatalf("PostExternal failed on second call: %v", err)
	}

	if id1 != id2 {
		t.Errorf("PostExternal returned different IDs for same input: %q vs %q", id1, id2)
	}

	// Verify only one external record was created
	extDir := filepath.Join(b.BusDir, "external")
	entries, err := os.ReadDir(extDir)
	if err != nil {
		t.Fatalf("ReadDir failed: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("expected 1 external record, got %d", len(entries))
	}
}

// TestPostExternalHashMismatch tests that a changed payload for the same
// source/externalID is rejected.
func TestPostExternalHashMismatch(t *testing.T) {
	root := t.TempDir()
	b, err := New(root)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	_, err = b.PostExternal("fleet-comms", "W0-001", "ShipA", "ShipB", "message 1")
	if err != nil {
		t.Fatalf("PostExternal failed: %v", err)
	}

	if _, err := b.PostExternal("fleet-comms", "W0-001", "ShipA", "ShipB", "message 2"); err == nil {
		t.Fatal("PostExternal should fail with changed payload")
	}
}

// TestPostExternalResumption tests that a crashed publication can be resumed.
func TestPostExternalResumption(t *testing.T) {
	root := t.TempDir()
	b, err := New(root)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	// First call creates the record but doesn't complete (simulate crash)
	id1, err := b.PostExternal("fleet-comms", "W0-001", "ShipA", "ShipB", "test message")
	if err != nil {
		t.Fatalf("PostExternal failed: %v", err)
	}

	// Second call with same source/externalID/text should resume and return same ID
	id2, err := b.PostExternal("fleet-comms", "W0-001", "ShipA", "ShipB", "test message")
	if err != nil {
		t.Fatalf("PostExternal failed on resume: %v", err)
	}

	if id1 != id2 {
		t.Errorf("PostExternal returned different IDs on resume: %q vs %q", id1, id2)
	}
}

// TestPostExternalInvalidKey tests that invalid source/externalID are rejected.
func TestPostExternalInvalidKey(t *testing.T) {
	root := t.TempDir()
	b, err := New(root)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	// Empty source
	if _, err := b.PostExternal("", "W0-001", "ShipA", "ShipB", "test"); err == nil {
		t.Error("PostExternal should reject empty source")
	}
	// Empty externalID
	if _, err := b.PostExternal("fleet-comms", "", "ShipA", "ShipB", "test"); err == nil {
		t.Error("PostExternal should reject empty externalID")
	}
	// Path traversal in source
	if _, err := b.PostExternal("../../etc/passwd", "W0-001", "ShipA", "ShipB", "test"); err == nil {
		t.Error("PostExternal should reject path traversal in source")
	}
	// Path traversal in externalID
	if _, err := b.PostExternal("fleet-comms", "../../etc/passwd", "ShipA", "ShipB", "test"); err == nil {
		t.Error("PostExternal should reject path traversal in externalID")
	}
}

// TestValidExternalKey tests the validExternalKey function.
func TestValidExternalKey(t *testing.T) {
	validKeys := []string{
		"fleet-comms",
		"my-repo-1",
		"repo_1",
		"repo.name",
		"a",
		"Z",
		"9",
		"-",
		"_",
	}
	for _, k := range validKeys {
		if !validExternalKey(k) {
			t.Errorf("validExternalKey(%q) should be true", k)
		}
	}

	invalidKeys := []string{
		"",
		"/etc/passwd",
		"../etc/passwd",
		"..",
		".",
		"a/b",
		"a\\b",
		"a|b",
		"a;b",
		"a&b",
		"a|b",
		"a<b",
		"a>b",
	}
	for _, k := range invalidKeys {
		if validExternalKey(k) {
			t.Errorf("validExternalKey(%q) should be false", k)
		}
	}
}

// TestBroadcastRecipients tests that broadcast messages are sent to all known ships.
func TestBroadcastRecipients(t *testing.T) {
	root := t.TempDir()
	b, err := New(root)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	// Create some ship directories
	for _, ship := range []string{"ShipA", "ShipB", "ShipC"} {
		dir := filepath.Join(b.MsgDir, ship, "unseen")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("MkdirAll failed: %v", err)
		}
	}

	recipients := b.broadcastRecipients()
	// Broadcast includes all known ships + sender itself
	if len(recipients) != 4 {
		t.Errorf("broadcastRecipients returned %d ships, want 3", len(recipients))
	}

	// Check that all ships are in the list
	found := make(map[string]bool)
	for _, r := range recipients {
		found[r] = true
	}
	for _, expected := range []string{"ShipA", "ShipB", "ShipC"} {
		if !found[expected] {
			t.Errorf("broadcastRecipients missing ship %q", expected)
		}
	}
	// The sender should also be included
	if _, ok := found[b.ShipID]; !ok {
		t.Errorf("broadcastRecipients missing sender ship %q", b.ShipID)
	}
}

// TestExternalRecordJSON tests JSON marshaling/unmarshaling of externalRecord.
func TestExternalRecordJSON(t *testing.T) {
	rec := &externalRecord{
		Version:     1,
		Source:      "fleet-comms",
		ExternalID:  "W0-001",
		PayloadSHA:  "abc123",
		Payload:     "test message",
		From:        "ShipA",
		Target:      "ShipB",
		CommsID:     "m0001",
		CreatedAt:   "2026-01-01T00:00:00Z",
		State:       externalComplete,
		Recipients: []externalRecipient{
			{Ship: "ShipA", State: externalRecipientPublished},
			{Ship: "ShipB", State: externalRecipientPublished},
		},
	}

	data, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}

	var rec2 externalRecord
	if err := json.Unmarshal(data, &rec2); err != nil {
		t.Fatalf("json.Unmarshal failed: %v", err)
	}

	if rec2.Source != rec.Source {
		t.Errorf("Source = %q, want %q", rec2.Source, rec.Source)
	}
	if rec2.ExternalID != rec.ExternalID {
		t.Errorf("ExternalID = %q, want %q", rec2.ExternalID, rec.ExternalID)
	}
	if rec2.Payload != rec.Payload {
		t.Errorf("Payload = %q, want %q", rec2.Payload, rec.Payload)
	}
	if rec2.CommsID != rec.CommsID {
		t.Errorf("CommsID = %q, want %q", rec2.CommsID, rec.CommsID)
	}
	if rec2.State != rec.State {
		t.Errorf("State = %q, want %q", rec2.State, rec.State)
	}
	if len(rec2.Recipients) != len(rec.Recipients) {
		t.Errorf("Recipients length = %d, want %d", len(rec2.Recipients), len(rec.Recipients))
	}
}

// TestPostExternalBroadcast tests broadcast messages are sent to all known ships.
func TestPostExternalBroadcast(t *testing.T) {
	root := t.TempDir()
	b, err := New(root)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	// Create ship directories
	for _, ship := range []string{"ShipA", "ShipB"} {
		dir := filepath.Join(b.MsgDir, ship, "unseen")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("MkdirAll failed: %v", err)
		}
	}

	id, err := b.PostExternal("fleet-comms", "W0-001", "ShipA", "all", "broadcast message")
	if err != nil {
		t.Fatalf("PostExternal failed: %v", err)
	}

	// Both ships should have received the message
	for _, ship := range []string{"ShipA", "ShipB"} {
		msgFile := filepath.Join(b.MsgDir, ship, "unseen", id+".json")
		if _, err := os.Stat(msgFile); err != nil {
			t.Errorf("Message file not found for ship %s: %v", ship, err)
		}
	}
}

// TestExternalRecordPersistAcrossRestart tests that external records persist
// across bus restarts.
func TestExternalRecordPersistAcrossRestart(t *testing.T) {
	root := t.TempDir()
	bus, err := New(root)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	id, err := bus.PostExternal("fleet-comms", "W0-001", "ShipA", "ShipB", "test message")
	if err != nil {
		t.Fatalf("PostExternal failed: %v", err)
	}

	// Second bus instance reading same data
	bus2, err := New(root)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	rec, err := loadExternalRecordLocked(bus2, "fleet-comms", "W0-001")
	if err != nil {
		t.Fatalf("loadExternalRecordLocked failed: %v", err)
	}
	if rec == nil {
		t.Fatal("loadExternalRecordLocked returned nil")
	}
	if rec.CommsID != id {
		t.Errorf("rec.CommsID = %q, want %q", rec.CommsID, id)
	}
}

// TestPostExternalRejection tests that PostExternal rejects invalid inputs.
func TestPostExternalRejection(t *testing.T) {
	root := t.TempDir()
	b, err := New(root)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	// Empty source
	if _, err := b.PostExternal("", "W0-001", "ShipA", "ShipB", "test"); err == nil {
		t.Error("PostExternal should reject empty source")
	}
	// Empty externalID
	if _, err := b.PostExternal("fleet-comms", "", "ShipA", "ShipB", "test"); err == nil {
		t.Error("PostExternal should reject empty externalID")
	}
	// Empty from
	if _, err := b.PostExternal("fleet-comms", "W0-001", "", "ShipB", "test"); err == nil {
		t.Error("PostExternal should reject empty from")
	}
	// Empty target
	if _, err := b.PostExternal("fleet-comms", "W0-001", "ShipA", "", "test"); err == nil {
		t.Error("PostExternal should reject empty target")
	}
	// Empty text
	if _, err := b.PostExternal("fleet-comms", "W0-001", "ShipA", "ShipB", ""); err == nil {
		t.Error("PostExternal should reject empty text")
	}
}

// TestExternalRecordPersistence tests that external records persist across
// bus restarts and are not deleted by ACK/Purge.
func TestExternalRecordPersistence(t *testing.T) {
	root := t.TempDir()
	bus, err := New(root)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	id, err := bus.PostExternal("fleet-comms", "W0-001", "ShipA", "ShipB", "test message")
	if err != nil {
		t.Fatalf("PostExternal failed: %v", err)
	}

	// Verify external record exists
	extDir := filepath.Join(bus.BusDir, "external")
	entries, err := os.ReadDir(extDir)
	if err != nil {
		t.Fatalf("ReadDir failed: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("expected 1 external record, got %d", len(entries))
	}

	// Simulate ACK/Purge of the comms message (delete the message file)
	msgFile := filepath.Join(bus.MsgDir, "ShipB", "unseen", id+".json")
	if err := os.Remove(msgFile); err != nil {
		t.Fatalf("os.Remove failed: %v", err)
	}

	// External record should still exist
	if _, err := os.Stat(filepath.Join(extDir, entries[0].Name())); err != nil {
		t.Errorf("external record should persist after ACK/Purge: %v", err)
	}

	// Reload bus and verify record still exists
	bus2, err := New(root)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	rec, err := loadExternalRecordLocked(bus2, "fleet-comms", "W0-001")
	if err != nil {
		t.Fatalf("loadExternalRecordLocked failed: %v", err)
	}
	if rec == nil {
		t.Fatal("loadExternalRecordLocked returned nil")
	}
	if rec.CommsID != id {
		t.Errorf("rec.CommsID = %q, want %q", rec.CommsID, id)
	}
}
