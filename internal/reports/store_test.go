// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package reports

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCreateGetRoundTrip: the MessageID is kept bare (no angle brackets); the
// file carries it as an RFC822 "<id>" header and a .eml suffix, and reading it
// back yields the bare id again.
func TestCreateGetRoundTrip(t *testing.T) {
	root := t.TempDir()
	s, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	const id = "1792000000000000000"
	if _, err := s.Create(&ReportRecord{MessageID: id, From: "Enterprise", Subject: "hi", Body: "body", Created: 1792000000}); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(s.Dir(), id+".eml")); err != nil {
		t.Errorf("expected %s.eml on disk: %v", id, err)
	}
	data, err := os.ReadFile(s.Path(id))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "Message-ID: <"+id+">") {
		t.Errorf("header must carry the angle-bracketed id; got first line: %q", firstLine(string(data)))
	}
	got, err := s.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.MessageID != id {
		t.Errorf("MessageID round-trip = %q, want %q (bare)", got.MessageID, id)
	}
}

// TestMigrateLegacyJSONSuffix: a legacy <id>.json report is renamed to .eml on
// store open and stays readable.
func TestMigrateLegacyJSONSuffix(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".starfleet-ai", "var", "reports")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := "Message-ID: <r-123@starfleet>\nDate: 2026-10-15T00:00:00+02:00\nFrom: X\nSubject: s\n\nbody\n"
	if err := os.WriteFile(filepath.Join(dir, "r-123@starfleet.json"), []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}

	s, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "r-123@starfleet.eml")); err != nil {
		t.Errorf("legacy .json must be migrated to .eml: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "r-123@starfleet.json")); !os.IsNotExist(err) {
		t.Errorf("legacy .json must be gone after migration: %v", err)
	}
	recs, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].MessageID != "r-123@starfleet" {
		t.Errorf("migrated report not readable as bare id: %#v", recs)
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
