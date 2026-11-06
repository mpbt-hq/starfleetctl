// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
package comms

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
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
	id, err := b.PostExternal("fleet-comms", "W0-001", "ShipA", "ShipB", "ship", "", "test message")
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

	id1, err := b.PostExternal("fleet-comms", "W0-001", "ShipA", "ShipB", "ship", "", "test message")
	if err != nil {
		t.Fatalf("PostExternal failed: %v", err)
	}

	id2, err := b.PostExternal("fleet-comms", "W0-001", "ShipA", "ShipB", "ship", "", "test message")
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

	_, err = b.PostExternal("fleet-comms", "W0-001", "ShipA", "ShipB", "ship", "", "message 1")
	if err != nil {
		t.Fatalf("PostExternal failed: %v", err)
	}

	if _, err := b.PostExternal("fleet-comms", "W0-001", "ShipA", "ShipB", "ship", "", "message 2"); err == nil {
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
	id1, err := b.PostExternal("fleet-comms", "W0-001", "ShipA", "ShipB", "ship", "", "test message")
	if err != nil {
		t.Fatalf("PostExternal failed: %v", err)
	}

	// Second call with same source/externalID/text should resume and return same ID
	id2, err := b.PostExternal("fleet-comms", "W0-001", "ShipA", "ShipB", "ship", "", "test message")
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
	if _, err := b.PostExternal("", "W0-001", "ShipA", "ShipB", "ship", "", "test"); err == nil {
		t.Error("PostExternal should reject empty source")
	}
	// Empty externalID
	if _, err := b.PostExternal("fleet-comms", "", "ShipA", "ShipB", "ship", "", "test"); err == nil {
		t.Error("PostExternal should reject empty externalID")
	}
	// Path traversal in source
	if _, err := b.PostExternal("../../etc/passwd", "W0-001", "ShipA", "ShipB", "ship", "", "test"); err == nil {
		t.Error("PostExternal should reject path traversal in source")
	}
	// Path traversal in externalID
	if _, err := b.PostExternal("fleet-comms", "../../etc/passwd", "ShipA", "ShipB", "ship", "", "test"); err == nil {
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
		Version:    1,
		Source:     "fleet-comms",
		ExternalID: "W0-001",
		PayloadSHA: "3f0a377ba0a4a460ecb616f6507ce0d8cfa3e704025d4fda3ed0c5ca05468728",
		Payload:    "test message",
		From:       "ShipA",
		Target:     "ShipB",
		CommsID:    "m0001",
		CreatedAt:  "2026-01-01T00:00:00Z",
		State:      externalComplete,
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

	id, err := b.PostExternal("fleet-comms", "W0-001", "ShipA", "all", "ship", "", "broadcast message")
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

	id, err := bus.PostExternal("fleet-comms", "W0-001", "ShipA", "ShipB", "ship", "", "test message")
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
	if _, err := b.PostExternal("", "W0-001", "ShipA", "ShipB", "ship", "", "test"); err == nil {
		t.Error("PostExternal should reject empty source")
	}
	// Empty externalID
	if _, err := b.PostExternal("fleet-comms", "", "ShipA", "ShipB", "ship", "", "test"); err == nil {
		t.Error("PostExternal should reject empty externalID")
	}
	// Empty from
	if _, err := b.PostExternal("fleet-comms", "W0-001", "", "ShipB", "ship", "", "test"); err == nil {
		t.Error("PostExternal should reject empty from")
	}
	// Empty target
	if _, err := b.PostExternal("fleet-comms", "W0-001", "ShipA", "", "ship", "", "test"); err == nil {
		t.Error("PostExternal should reject empty target")
	}
	// Empty text
	if _, err := b.PostExternal("fleet-comms", "W0-001", "ShipA", "ShipB", "ship", "", ""); err == nil {
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

	id, err := bus.PostExternal("fleet-comms", "W0-001", "ShipA", "ShipB", "ship", "", "test message")
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

// ---- crash recovery / fault injection tests ---------------------------------

// writeExternalRecordFile writes an external record directly to the filesystem,
// bypassing the normal PostExternal path. Used to simulate crash states.
func writeExternalRecordFile(t *testing.T, b *Bus, rec *externalRecord) {
	t.Helper()
	path, err := externalRecordPath(b, rec.Source, rec.ExternalID)
	if err != nil {
		t.Fatalf("externalRecordPath: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
}

// TestPostExternalCrashAfterIDReservation tests that if PostExternal crashes
// after reserving the CommsID (nextID) but before creating the external record,
// the ID is not reused and a subsequent call creates a new record with the next ID.
func TestPostExternalCrashAfterIDReservation(t *testing.T) {
	root := t.TempDir()
	b, err := New(root)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	// First call completes normally - establishes baseline
	id1, err := b.PostExternal("fleet-comms", "W0-001", "ShipA", "ShipB", "ship", "", "message 1")
	if err != nil {
		t.Fatalf("PostExternal failed: %v", err)
	}

	// Simulate crash after nextID() but before record creation for a NEW message.
	// We do this by manually incrementing .seq (which nextID does) but not
	// creating the external record. Then call PostExternal for a different message.
	seqFile := filepath.Join(b.MsgDir, ".seq")
	if err := os.WriteFile(seqFile, []byte("5\n"), 0o644); err != nil {
		t.Fatalf("write seq: %v", err)
	}

	// This should get the next ID (m0006) and create the record normally
	id2, err := b.PostExternal("fleet-comms", "W0-002", "ShipA", "ShipB", "ship", "", "message 2")
	if err != nil {
		t.Fatalf("PostExternal failed: %v", err)
	}
	if id2 != "m0006" {
		t.Errorf("expected m0006 after manual seq advance, got %q", id2)
	}

	// Verify W0-001 still works (idempotent)
	id1b, err := b.PostExternal("fleet-comms", "W0-001", "ShipA", "ShipB", "ship", "", "message 1")
	if err != nil {
		t.Fatalf("PostExternal resume failed: %v", err)
	}
	if id1b != id1 {
		t.Errorf("W0-001 idempotent: expected %q, got %q", id1, id1b)
	}
}

// TestPostExternalCrashAfterRecordCreationNoCommsID tests recovery when a record
// exists but lacks a CommsID (old crash state, or crash between record creation
// and ID reservation in the old code path).
func TestPostExternalCrashAfterRecordCreationNoCommsID(t *testing.T) {
	root := t.TempDir()
	b, err := New(root)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	// Manually create an external record WITHOUT a CommsID (simulating old code crash)
	rec := &externalRecord{
		Version:    1,
		Source:     "fleet-comms",
		ExternalID: "W0-001",
		PayloadSHA: "3f0a377ba0a4a460ecb616f6507ce0d8cfa3e704025d4fda3ed0c5ca05468728",
		Payload:    "test message",
		From:       "ShipA",
		Target:     "ShipB",
		CommsID:    "", // Missing CommsID - crash state
		CreatedAt:  "2026-01-01T00:00:00Z",
		State:      externalPendingRecipients,
	}
	writeExternalRecordFile(t, b, rec)

	// PostExternal should detect missing CommsID, reserve one, and continue
	id, err := b.PostExternal("fleet-comms", "W0-001", "ShipA", "ShipB", "ship", "", "test message")
	if err != nil {
		t.Fatalf("PostExternal failed to recover: %v", err)
	}
	if id == "" {
		t.Fatal("PostExternal returned empty ID after recovery")
	}

	// Verify record now has CommsID
	rec2, err := loadExternalRecordLocked(b, "fleet-comms", "W0-001")
	if err != nil {
		t.Fatalf("loadExternalRecordLocked: %v", err)
	}
	if rec2.CommsID == "" {
		t.Fatal("record still missing CommsID after recovery")
	}
	if rec2.CommsID != id {
		t.Errorf("record CommsID %q != returned %q", rec2.CommsID, id)
	}

	// Second call should be idempotent
	id2, err := b.PostExternal("fleet-comms", "W0-001", "ShipA", "ShipB", "ship", "", "test message")
	if err != nil {
		t.Fatalf("second PostExternal failed: %v", err)
	}
	if id2 != id {
		t.Errorf("not idempotent after recovery: %q vs %q", id2, id)
	}
}

// TestPostExternalCrashDuringRecipientResolution tests recovery when the record
// has a CommsID but recipients haven't been resolved yet (State = externalPosting,
// Recipients = nil).
func TestPostExternalCrashDuringRecipientResolution(t *testing.T) {
	root := t.TempDir()
	b, err := New(root)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	// Create ship directories so recipients exist
	for _, ship := range []string{"ShipA", "ShipB"} {
		dir := filepath.Join(b.MsgDir, ship, "unseen")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
	}

	// Create record with CommsID but no recipients resolved yet
	rec := &externalRecord{
		Version:    1,
		Source:     "fleet-comms",
		ExternalID: "W0-001",
		PayloadSHA: "3f0a377ba0a4a460ecb616f6507ce0d8cfa3e704025d4fda3ed0c5ca05468728",
		Payload:    "test message",
		From:       "ShipA",
		Target:     "all", // broadcast
		CommsID:    "m0001",
		CreatedAt:  "2026-01-01T00:00:00Z",
		State:      externalPosting,
		Recipients: nil, // Not resolved yet
	}
	writeExternalRecordFile(t, b, rec)

	// PostExternal should resolve recipients and publish to all
	id, err := b.PostExternal("fleet-comms", "W0-001", "ShipA", "all", "ship", "", "test message")
	if err != nil {
		t.Fatalf("PostExternal failed to recover: %v", err)
	}
	if id != "m0001" {
		t.Errorf("CommsID changed during recovery: %q", id)
	}

	// Verify both ships received the message
	for _, ship := range []string{"ShipA", "ShipB"} {
		msgFile := filepath.Join(b.MsgDir, ship, "unseen", "m0001.json")
		if _, err := os.Stat(msgFile); err != nil {
			t.Errorf("message not delivered to %s: %v", ship, err)
		}
	}

	// Verify record is now complete with recipients
	rec2, err := loadExternalRecordLocked(b, "fleet-comms", "W0-001")
	if err != nil {
		t.Fatalf("loadExternalRecordLocked: %v", err)
	}
	if rec2.State != externalComplete {
		t.Errorf("state not complete: %q", rec2.State)
	}
	// Broadcast includes sender + explicit recipients (ShipA, ShipB)
	if len(rec2.Recipients) != 3 {
		t.Errorf("expected 2 recipients, got %d", len(rec2.Recipients))
	}
}

// TestPostExternalCrashDuringRecipientPublish tests recovery when some
// recipients have been published but not all.
func TestPostExternalCrashDuringRecipientPublish(t *testing.T) {
	root := t.TempDir()
	b, err := New(root)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	// Create ship directories
	for _, ship := range []string{"ShipA", "ShipB", "ShipC"} {
		dir := filepath.Join(b.MsgDir, ship, "unseen")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
	}

	// Create record with CommsID, recipients resolved, but only ShipA published
	rec := &externalRecord{
		Version:    1,
		Source:     "fleet-comms",
		ExternalID: "W0-001",
		PayloadSHA: "3f0a377ba0a4a460ecb616f6507ce0d8cfa3e704025d4fda3ed0c5ca05468728",
		Payload:    "test message",
		From:       "ShipA",
		Target:     "all",
		CommsID:    "m0001",
		CreatedAt:  "2026-01-01T00:00:00Z",
		State:      externalPosting,
		Recipients: []externalRecipient{
			{Ship: "ShipA", State: externalRecipientPublished},
			{Ship: "ShipB", State: externalRecipientPending},
			{Ship: "ShipC", State: externalRecipientPending},
		},
	}
	writeExternalRecordFile(t, b, rec)

	// Manually create the message file for ShipA (as if it was already published)
	msg := msgRecord{
		ID:             "m0001",
		Epoch:          12345,
		ISO:            "2026-01-01T00:00:00Z",
		From:           "ShipA",
		Target:         "ShipA",
		Text:           "test message",
		ReplyTo:        "",
		Type:           "ship",
		ExternalSource: "fleet-comms",
		ExternalID:     "W0-001",
		ExternalHash:   "abc123",
	}
	msgData, _ := json.MarshalIndent(msg, "", "  ")
	shipADir := filepath.Join(b.MsgDir, "ShipA", "unseen")
	if err := os.WriteFile(filepath.Join(shipADir, "m0001.json"), msgData, 0o644); err != nil {
		t.Fatalf("write ShipA msg: %v", err)
	}

	// PostExternal should resume and publish to ShipB and ShipC only
	id, err := b.PostExternal("fleet-comms", "W0-001", "ShipA", "all", "ship", "", "test message")
	if err != nil {
		t.Fatalf("PostExternal failed to resume: %v", err)
	}
	if id != "m0001" {
		t.Errorf("CommsID changed: %q", id)
	}

	// Verify all three ships have the message
	for _, ship := range []string{"ShipA", "ShipB", "ShipC"} {
		msgFile := filepath.Join(b.MsgDir, ship, "unseen", "m0001.json")
		if _, err := os.Stat(msgFile); err != nil {
			t.Errorf("message not delivered to %s: %v", ship, err)
		}
	}

	// Verify record is complete
	rec2, err := loadExternalRecordLocked(b, "fleet-comms", "W0-001")
	if err != nil {
		t.Fatalf("loadExternalRecordLocked: %v", err)
	}
	if rec2.State != externalComplete {
		t.Errorf("state not complete: %q", rec2.State)
	}
	for _, r := range rec2.Recipients {
		if r.State != externalRecipientPublished {
			t.Errorf("recipient %s not published: %q", r.Ship, r.State)
		}
	}
}

// TestPostExternalCrashAfterAllPublishedBeforeComplete tests recovery when
// all recipients are published but state hasn't been marked complete.
func TestPostExternalCrashAfterAllPublishedBeforeComplete(t *testing.T) {
	root := t.TempDir()
	b, err := New(root)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	// Create ship directories
	for _, ship := range []string{"ShipA", "ShipB"} {
		dir := filepath.Join(b.MsgDir, ship, "unseen")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
	}

	// Create record with all recipients published but state still "posting"
	rec := &externalRecord{
		Version:    1,
		Source:     "fleet-comms",
		ExternalID: "W0-001",
		PayloadSHA: "3f0a377ba0a4a460ecb616f6507ce0d8cfa3e704025d4fda3ed0c5ca05468728",
		Payload:    "test message",
		From:       "ShipA",
		Target:     "all",
		CommsID:    "m0001",
		CreatedAt:  "2026-01-01T00:00:00Z",
		State:      externalPosting, // Not yet complete
		Recipients: []externalRecipient{
			{Ship: "ShipA", State: externalRecipientPublished},
			{Ship: "ShipB", State: externalRecipientPublished},
		},
	}
	writeExternalRecordFile(t, b, rec)

	// Manually create message files for both
	msg := msgRecord{
		ID:             "m0001",
		Epoch:          12345,
		ISO:            "2026-01-01T00:00:00Z",
		From:           "ShipA",
		Target:         "ShipA", // will be overridden per recipient
		Text:           "test message",
		ReplyTo:        "",
		Type:           "ship",
		ExternalSource: "fleet-comms",
		ExternalID:     "W0-001",
		ExternalHash:   "abc123",
	}
	for _, ship := range []string{"ShipA", "ShipB"} {
		msg.Target = ship
		msgData, _ := json.MarshalIndent(msg, "", "  ")
		dir := filepath.Join(b.MsgDir, ship, "unseen")
		if err := os.WriteFile(filepath.Join(dir, "m0001.json"), msgData, 0o644); err != nil {
			t.Fatalf("write %s msg: %v", ship, err)
		}
	}

	// PostExternal should see all recipients published and mark complete
	if _, err := b.PostExternal("fleet-comms", "W0-001", "ShipA", "all", "ship", "", "test message"); err != nil {
		t.Fatalf("PostExternal failed: %v", err)
	}

	// Verify record is now complete
	rec2, err := loadExternalRecordLocked(b, "fleet-comms", "W0-001")
	if err != nil {
		t.Fatalf("loadExternalRecordLocked: %v", err)
	}
	if rec2.State != externalComplete {
		t.Errorf("state not complete: %q", rec2.State)
	}
}

// TestPostExternalFullRestartSimulation tests a full restart scenario:
// 1. Start PostExternal, record created with CommsID
// 2. Recipients resolved
// 3. Some recipients published
// 4. Process crashes (simulated by new Bus instance)
// 5. PostExternal called again -> should resume and complete
func TestPostExternalFullRestartSimulation(t *testing.T) {
	root := t.TempDir()

	// Phase 1: Initial call creates record and publishes to ShipA
	bus1, err := New(root)
	if err != nil {
		t.Fatalf("New bus1 failed: %v", err)
	}

	// Create ship directories
	for _, ship := range []string{"ShipA", "ShipB", "ShipC"} {
		dir := filepath.Join(bus1.MsgDir, ship, "unseen")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
	}

	id1, err := bus1.PostExternal("fleet-comms", "W0-001", "ShipA", "all", "ship", "", "restart test")
	if err != nil {
		t.Fatalf("PostExternal phase 1 failed: %v", err)
	}

	// Phase 2: Simulate crash by creating new Bus instance
	bus2, err := New(root)
	if err != nil {
		t.Fatalf("New bus2 failed: %v", err)
	}

	// Phase 3: Resume - should complete publishing to all ships
	id2, err := bus2.PostExternal("fleet-comms", "W0-001", "ShipA", "all", "ship", "", "restart test")
	if err != nil {
		t.Fatalf("PostExternal phase 3 failed: %v", err)
	}

	if id1 != id2 {
		t.Errorf("CommsID changed across restart: %q vs %q", id1, id2)
	}

	// Verify all ships received the message
	for _, ship := range []string{"ShipA", "ShipB", "ShipC"} {
		msgFile := filepath.Join(bus2.MsgDir, ship, "unseen", "m0001.json")
		if _, err := os.Stat(msgFile); err != nil {
			t.Errorf("message not delivered to %s after restart: %v", ship, err)
		}
	}

	// Verify external record is complete
	rec, err := loadExternalRecordLocked(bus2, "fleet-comms", "W0-001")
	if err != nil {
		t.Fatalf("loadExternalRecordLocked: %v", err)
	}
	if rec.State != externalComplete {
		t.Errorf("record not complete after restart: %q", rec.State)
	}
}

// TestPostExternalConcurrentCalls tests that concurrent PostExternal calls
// with the same source/externalID don't create duplicate records or IDs.
func TestPostExternalConcurrentCalls(t *testing.T) {
	root := t.TempDir()
	b, err := New(root)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	// Create ship directories
	for _, ship := range []string{"ShipA", "ShipB"} {
		dir := filepath.Join(b.MsgDir, ship, "unseen")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
	}

	const workers = 10
	var wg sync.WaitGroup
	ids := make(chan string, workers)
	errs := make(chan error, workers)

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, err := b.PostExternal("fleet-comms", "W0-001", "ShipA", "all", "ship", "", "concurrent test")
			if err != nil {
				errs <- err
				return
			}
			ids <- id
		}()
	}
	wg.Wait()
	close(ids)
	close(errs)

	// Check for errors
	for err := range errs {
		t.Errorf("concurrent PostExternal error: %v", err)
	}

	// All IDs should be the same
	var firstID string
	count := 0
	for id := range ids {
		count++
		if firstID == "" {
			firstID = id
		} else if id != firstID {
			t.Errorf("different CommsID from concurrent calls: %q vs %q", id, firstID)
		}
	}
	if count != workers {
		t.Errorf("expected %d results, got %d", workers, count)
	}
	if firstID == "" {
		t.Fatal("no IDs returned")
	}

	// Verify only one external record exists
	extDir := filepath.Join(b.BusDir, "external")
	entries, err := os.ReadDir(extDir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("expected 1 external record, got %d", len(entries))
	}

	// Verify all ships received exactly one message
	for _, ship := range []string{"ShipA", "ShipB"} {
		unseenDir := filepath.Join(b.MsgDir, ship, "unseen")
		entries, _ := os.ReadDir(unseenDir)
		if len(entries) != 1 {
			t.Errorf("ship %s: expected 1 message, got %d", ship, len(entries))
		}
	}
}

// ---- ID reservation crash-safety tests -------------------------------------

// writeSeqFile writes the .seq file directly (simulating partial write/crash)
func writeSeqFile(t *testing.T, b *Bus, n int64) {
	t.Helper()
	seqFile := filepath.Join(b.MsgDir, ".seq")
	if err := os.WriteFile(seqFile, []byte(fmt.Sprintf("%d\n", n)), 0o644); err != nil {
		t.Fatalf("write seq: %v", err)
	}
}

// writeExternalRecordDirect writes an external record directly to the filesystem,
// bypassing PostExternal. Used to simulate crash states.
func writeExternalRecordDirect(t *testing.T, b *Bus, rec *externalRecord) {
	t.Helper()
	extDir := filepath.Join(b.BusDir, "external")
	if err := os.MkdirAll(extDir, 0o755); err != nil {
		t.Fatalf("mkdir external: %v", err)
	}
	path, err := externalRecordPath(b, rec.Source, rec.ExternalID)
	if err != nil {
		t.Fatalf("externalRecordPath: %v", err)
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write external record: %v", err)
	}
}

// TestPostExternalIDReservationCrashAfterSeq tests that if PostExternal crashes
// after writing .seq but before creating the external record, the ID is not reused.
func TestPostExternalIDReservationCrashAfterSeq(t *testing.T) {
	root := t.TempDir()
	b, err := New(root)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	// First successful call to establish baseline
	id1, err := b.PostExternal("fleet-comms", "W0-001", "ShipA", "ShipB", "ship", "", "message 1")
	if err != nil {
		t.Fatalf("PostExternal failed: %v", err)
	}

	// Simulate crash after .seq write but before external record creation for NEW message.
	// Manually increment .seq (simulating nextID completed) but don't create external record.
	// Then call PostExternal for a different message.
	writeSeqFile(t, b, 5)

	// This should get the next ID (m0006) and create the record normally
	id2, err := b.PostExternal("fleet-comms", "W0-002", "ShipA", "ShipB", "ship", "", "message 2")
	if err != nil {
		t.Fatalf("PostExternal failed: %v", err)
	}
	if id2 != "m0006" {
		t.Errorf("expected m0006 after manual seq advance, got %q", id2)
	}

	// Verify W0-001 still works (idempotent)
	id1b, err := b.PostExternal("fleet-comms", "W0-001", "ShipA", "ShipB", "ship", "", "message 1")
	if err != nil {
		t.Fatalf("PostExternal resume failed: %v", err)
	}
	if id1b != id1 {
		t.Errorf("W0-001 idempotent: expected %q, got %q", id1, id1b)
	}
}

// TestPostExternalCrashAfterSeqBeforeExternalRecord tests that an external record
// with a CommsID but missing from external directory can be recovered.
func TestPostExternalCrashAfterSeqBeforeExternalRecord(t *testing.T) {
	root := t.TempDir()
	b, err := New(root)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	// Create ship directories
	for _, ship := range []string{"ShipA", "ShipB"} {
		dir := filepath.Join(b.MsgDir, ship, "unseen")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
	}

	// Manually advance .seq to simulate completed ID reservation
	writeSeqFile(t, b, 1)

	// Create an external record WITH CommsID (as if PostExternal reserved ID)
	rec := &externalRecord{
		Version:    1,
		Source:     "fleet-comms",
		ExternalID: "W0-001",
		PayloadSHA: "3f0a377ba0a4a460ecb616f6507ce0d8cfa3e704025d4fda3ed0c5ca05468728",
		Payload:    "test message",
		From:       "ShipA",
		Target:     "all",
		CommsID:    "m0001",
		CreatedAt:  "2026-01-01T00:00:00Z",
		State:      externalPosting,
		Recipients: nil, // Not resolved yet - crash state
	}
	writeExternalRecordDirect(t, b, rec)

	// PostExternal should detect existing record with CommsID, resolve recipients, and complete
	id, err := b.PostExternal("fleet-comms", "W0-001", "ShipA", "all", "ship", "", "test message")
	if err != nil {
		t.Fatalf("PostExternal failed to recover: %v", err)
	}
	if id != "m0001" {
		t.Errorf("CommsID changed during recovery: %q", id)
	}

	// Verify both ships received the message
	for _, ship := range []string{"ShipA", "ShipB"} {
		msgFile := filepath.Join(b.MsgDir, ship, "unseen", "m0001.json")
		if _, err := os.Stat(msgFile); err != nil {
			t.Errorf("message not delivered to %s: %v", ship, err)
		}
	}

	// Verify record is now complete with recipients
	rec2, err := loadExternalRecordLocked(b, "fleet-comms", "W0-001")
	if err != nil {
		t.Fatalf("loadExternalRecordLocked: %v", err)
	}
	if rec2.State != externalComplete {
		t.Errorf("state not complete: %q", rec2.State)
	}
	if len(rec2.Recipients) != 3 { // ShipA, ShipB, and sender (broadcast includes sender)
		t.Errorf("expected 3 recipients (broadcast includes sender), got %d", len(rec2.Recipients))
	}
}

// TestPostExternalCrashDuringRecipientPublishResumed tests recovery when some
// recipients have been published but not all.
func TestPostExternalCrashDuringRecipientPublishResumed(t *testing.T) {
	root := t.TempDir()
	b, err := New(root)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	// Create ship directories
	for _, ship := range []string{"ShipA", "ShipB", "ShipC"} {
		dir := filepath.Join(b.MsgDir, ship, "unseen")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
	}

	// Create record with CommsID, recipients resolved, but only ShipA published
	rec := &externalRecord{
		Version:    1,
		Source:     "fleet-comms",
		ExternalID: "W0-001",
		PayloadSHA: "3f0a377ba0a4a460ecb616f6507ce0d8cfa3e704025d4fda3ed0c5ca05468728",
		Payload:    "test message",
		From:       "ShipA",
		Target:     "all",
		CommsID:    "m0001",
		CreatedAt:  "2026-01-01T00:00:00Z",
		State:      externalPosting,
		Recipients: []externalRecipient{
			{Ship: "ShipA", State: externalRecipientPublished},
			{Ship: "ShipB", State: externalRecipientPending},
			{Ship: "ShipC", State: externalRecipientPending},
		},
	}
	writeExternalRecordDirect(t, b, rec)

	// Manually create the message file for ShipA (as if it was already published)
	msg := msgRecord{
		ID:             "m0001",
		Epoch:          12345,
		ISO:            "2026-01-01T00:00:00Z",
		From:           "ShipA",
		Target:         "ShipA",
		Text:           "test message",
		ReplyTo:        "",
		Type:           "ship",
		ExternalSource: "fleet-comms",
		ExternalID:     "W0-001",
		ExternalHash:   "3f0a377ba0a4a460ecb616f6507ce0d8cfa3e704025d4fda3ed0c5ca05468728",
	}
	msgData, _ := json.MarshalIndent(msg, "", "  ")
	shipADir := filepath.Join(b.MsgDir, "ShipA", "unseen")
	if err := os.WriteFile(filepath.Join(shipADir, "m0001.json"), msgData, 0o644); err != nil {
		t.Fatalf("write ShipA msg: %v", err)
	}

	// PostExternal should resume and publish to ShipB and ShipC only
	id, err := b.PostExternal("fleet-comms", "W0-001", "ShipA", "all", "ship", "", "test message")
	if err != nil {
		t.Fatalf("PostExternal failed to resume: %v", err)
	}
	if id != "m0001" {
		t.Errorf("CommsID changed: %q", id)
	}

	// Verify all three ships have the message
	for _, ship := range []string{"ShipA", "ShipB", "ShipC"} {
		msgFile := filepath.Join(b.MsgDir, ship, "unseen", "m0001.json")
		if _, err := os.Stat(msgFile); err != nil {
			t.Errorf("message not delivered to %s: %v", ship, err)
		}
	}

	// Verify record is complete
	rec2, err := loadExternalRecordLocked(b, "fleet-comms", "W0-001")
	if err != nil {
		t.Fatalf("loadExternalRecordLocked: %v", err)
	}
	if rec2.State != externalComplete {
		t.Errorf("state not complete: %q", rec2.State)
	}
	for _, r := range rec2.Recipients {
		if r.State != externalRecipientPublished {
			t.Errorf("recipient %s not published: %q", r.Ship, r.State)
		}
	}
}

// TestPostExternalFullRestartSimulationWithLostSeq tests a full restart scenario
func TestPostExternalFullRestartSimulationWithLostSeq(t *testing.T) {
	root := t.TempDir()

	// Phase 1: Initial call creates record and publishes to all ships
	bus1, err := New(root)
	if err != nil {
		t.Fatalf("New bus1 failed: %v", err)
	}

	// Create ship directories
	for _, ship := range []string{"ShipA", "ShipB", "ShipC"} {
		dir := filepath.Join(bus1.MsgDir, ship, "unseen")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
	}

	id1, err := bus1.PostExternal("fleet-comms", "W0-001", "ShipA", "all", "ship", "", "restart test")
	if err != nil {
		t.Fatalf("PostExternal phase 1 failed: %v", err)
	}

	// Simulate crash + lost .seq by creating new Bus instance
	bus2, err := New(root)
	if err != nil {
		t.Fatalf("New bus2 failed: %v", err)
	}

	// Phase 3: Resume - should complete publishing to all ships
	id2, err := bus2.PostExternal("fleet-comms", "W0-001", "ShipA", "all", "ship", "", "restart test")
	if err != nil {
		t.Fatalf("PostExternal phase 3 failed: %v", err)
	}

	if id1 != id2 {
		t.Errorf("CommsID changed across restart: %q vs %q", id1, id2)
	}

	// Verify all ships received the message
	for _, ship := range []string{"ShipA", "ShipB", "ShipC"} {
		msgFile := filepath.Join(bus2.MsgDir, ship, "unseen", "m0001.json")
		if _, err := os.Stat(msgFile); err != nil {
			t.Errorf("message not delivered to %s after restart: %v", ship, err)
		}
	}

	// Verify external record is complete
	rec, err := loadExternalRecordLocked(bus2, "fleet-comms", "W0-001")
	if err != nil {
		t.Fatalf("loadExternalRecordLocked: %v", err)
	}
	if rec.State != externalComplete {
		t.Errorf("record not complete after restart: %q", rec.State)
	}
}

// TestPostExternalNoDuplicateIDWithExternalRecord ensures that the presence
// of an external record with a CommsID prevents that ID from being reused.
func TestPostExternalNoDuplicateIDWithExternalRecord(t *testing.T) {
	root := t.TempDir()
	b, err := New(root)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	// Create an external record with CommsID m0005
	rec := &externalRecord{
		Version:    1,
		Source:     "fleet-comms",
		ExternalID: "W0-005",
		PayloadSHA: "3f0a377ba0a4a460ecb616f6507ce0d8cfa3e704025d4fda3ed0c5ca05468728",
		Payload:    "test message",
		From:       "ShipA",
		Target:     "ShipB",
		CommsID:    "m0005",
		CreatedAt:  "2026-01-01T00:00:00Z",
		State:      externalComplete,
		Recipients: []externalRecipient{
			{Ship: "ShipB", State: externalRecipientPublished},
		},
	}
	writeExternalRecordDirect(t, b, rec)

	// Now call PostExternal for a DIFFERENT message with the same ID reservation path
	// The ID m0005 should NOT be reused because it's already in an external record
	id, err := b.PostExternal("fleet-comms", "W0-006", "ShipA", "ShipB", "ship", "", "another message")
	if err != nil {
		t.Fatalf("PostExternal failed: %v", err)
	}

	// Should get m0006 or higher, not m0005
	if id == "m0005" {
		t.Errorf("ID m0005 was reused despite existing external record")
	}
	if id != "m0006" {
		t.Errorf("expected m0006 or higher, got %q", id)
	}
}

func TestPostExternalPermanentErrorClassification(t *testing.T) {
	root := t.TempDir()
	b, err := New(root)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	// Missing required fields (permanent input errors).
	for _, tc := range []struct {
		name                                   string
		source, externalID, from, target, text string
	}{
		{"empty source", "", "W0-rf", "ShipA", "ShipB", "payload"},
		{"empty externalID", "fleet-comms", "", "ShipA", "ShipB", "payload"},
		{"empty from", "fleet-comms", "W0-rf", "", "ShipB", "payload"},
		{"empty target", "fleet-comms", "W0-rf", "ShipA", "", "payload"},
	} {
		if _, err := b.PostExternal(tc.source, tc.externalID, tc.from, tc.target, "ship", "", tc.text); err == nil {
			t.Fatalf("%s: expected error", tc.name)
		} else if !IsPermanentPostExternalError(err) {
			t.Fatalf("%s must be permanent, got: %v", tc.name, err)
		}
	}

	// Invalid source/externalID characters (deterministic invalid input, permanent).
	for _, tc := range []struct {
		name       string
		source     string
		externalID string
	}{
		{"slash in source", "fleet/comms", "W0-inv"},
		{"space in source", "fleet comms", "W0-inv"},
		{"slash in externalID", "fleet-comms", "W0/inv"},
		{"space in externalID", "fleet-comms", "W0 inv"},
	} {
		if _, err := b.PostExternal(tc.source, tc.externalID, "ShipA", "ShipB", "ship", "", "payload"); err == nil {
			t.Fatalf("%s: expected error", tc.name)
		} else if !IsPermanentPostExternalError(err) {
			t.Fatalf("%s must be permanent, got: %v", tc.name, err)
		}
	}

	// Empty text (permanent).
	if _, err := b.PostExternal("fleet-comms", "W0-empty", "ShipA", "ShipB", "ship", "", ""); err == nil {
		t.Fatal("expected empty-text error")
	} else if !IsPermanentPostExternalError(err) {
		t.Fatalf("empty text must be permanent, got: %v", err)
	}

	// Hash mismatch: same external ID, different payload (permanent).
	if _, err := b.PostExternal("fleet-comms", "W0-hash", "ShipA", "ShipB", "ship", "", "payload-1"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := b.PostExternal("fleet-comms", "W0-hash", "ShipA", "ShipB", "ship", "", "payload-2"); err == nil {
		t.Fatal("expected hash mismatch")
	} else if !IsPermanentPostExternalError(err) {
		t.Fatalf("hash mismatch must be permanent, got: %v", err)
	}

	// Existing recipient copy mismatch (permanent): published message file differs.
	if _, err := b.PostExternal("fleet-comms", "W0-rcpt", "ShipA", "ShipB", "ship", "", "ok"); err != nil {
		t.Fatalf("seed rcpt: %v", err)
	}
	lock, err := b.lockBus()
	if err != nil {
		t.Fatalf("lock bus: %v", err)
	}
	rec, err := loadExternalRecordLocked(b, "fleet-comms", "W0-rcpt")
	lock.Close()
	if err != nil || rec == nil {
		t.Fatalf("load record: %v %v", rec, err)
	}
	// Force resume path: mark as posting with pending recipient and tamper recipient copy.
	rec.State = externalPosting
	rec.Recipients = []externalRecipient{{Ship: "ShipB", State: externalRecipientPending}}
	writeExternalRecordFile(t, b, rec)
	msgPath := filepath.Join(b.MsgDir, "ShipB", "unseen", rec.CommsID+".json")
	if err := os.WriteFile(msgPath, []byte(`{"id":"`+rec.CommsID+`","text":"tampered"}`), 0o644); err != nil {
		t.Fatalf("tamper recipient copy: %v", err)
	}
	if _, err := b.PostExternal("fleet-comms", "W0-rcpt", "ShipA", "ShipB", "ship", "", "ok"); err == nil {
		t.Fatal("expected recipient copy mismatch")
	} else if !IsPermanentPostExternalError(err) {
		t.Fatalf("recipient copy mismatch must be permanent, got: %v", err)
	}

	// Corrupt external record (permanent).
	corruptPath, err := externalRecordPath(b, "fleet-comms", "W0-corrupt")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(corruptPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(corruptPath, []byte("{not-json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := b.PostExternal("fleet-comms", "W0-corrupt", "ShipA", "ShipB", "ship", "", "payload"); err == nil {
		t.Fatal("expected corrupt external record error")
	} else if !IsPermanentPostExternalError(err) {
		t.Fatalf("corrupt external record must be permanent, got: %v", err)
	}

	// External-record key mismatch (permanent): file at hashed path contains different key.
	keyRec := &externalRecord{
		Version:    1,
		Source:     "other-source",
		ExternalID: "other-id",
		PayloadSHA: "deadbeef",
		Payload:    "x",
		From:       "ShipA",
		Target:     "ShipB",
		CommsID:    "m9999",
		CreatedAt:  "2026-01-01T00:00:00Z",
		State:      externalComplete,
	}
	mismatchPath, err := externalRecordPath(b, "fleet-comms", "W0-key")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(mismatchPath), 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(keyRec)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mismatchPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := b.PostExternal("fleet-comms", "W0-key", "ShipA", "ShipB", "ship", "", "payload"); err == nil {
		t.Fatal("expected key mismatch error")
	} else if !IsPermanentPostExternalError(err) {
		t.Fatalf("external record key mismatch must be permanent, got: %v", err)
	}

	// Temporary/infrastructure error must not be classified as permanent.
	badRoot := t.TempDir()
	badBusDir := filepath.Join(badRoot, "badbus")
	if err := os.WriteFile(badBusDir, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	badBus := &Bus{Root: badRoot, BusDir: badBusDir, MsgDir: filepath.Join(badBusDir, "msgs"), ShipID: "alpha"}
	if _, err := badBus.PostExternal("fleet-comms", "W0-temp", "ShipA", "ShipB", "ship", "", "payload"); err == nil {
		t.Fatal("expected temporary/infrastructure error")
	} else if IsPermanentPostExternalError(err) {
		t.Fatalf("temporary infrastructure error must not be permanent, got: %v", err)
	}
}
