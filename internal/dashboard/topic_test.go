// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package dashboard

import (
	"os"
	"os/exec"
	"testing"
)

// newTestDashboard returns a Dashboard rooted at a fresh temp git repo.
func newTestDashboard(t *testing.T) *Dashboard {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("git", "init", "-q")
	run("git", "config", "user.email", "test@test")
	run("git", "config", "user.name", "test")

	d, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := os.MkdirAll(d.TopicsDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	return d
}

const roundTripSrc = `---
slug: task-round-trip
title: "Round Trip Task"
category: active
kind: task
status: open
assigned-to: "—"
created-by: "Enterprise"
created: "2026-07-15T00:00:00Z"
doc_ref: "—"
---

Some body text that must survive the round trip.
`

// TestTopicLoadRoundTrip verifies DoTopicLoad parses every active-topic
// frontmatter field, and DoTopicUpdate rewrites a topic without dropping any
// of the fields it does not itself mutate (kind/created-by/created/doc_ref).
func TestTopicLoadRoundTrip(t *testing.T) {
	d := newTestDashboard(t)

	// Seed a topic file directly (simulating capture's output).
	if err := os.WriteFile(d.topicPath("task-round-trip"), []byte(roundTripSrc), 0o644); err != nil {
		t.Fatal(err)
	}

	m, body, err := d.DoTopicLoad("task-round-trip")
	if err != nil {
		t.Fatalf("DoTopicLoad: %v", err)
	}
	if m.Slug != "task-round-trip" || m.Title != "Round Trip Task" || m.Kind != "task" {
		t.Fatalf("parsed frontmatter wrong: %+v", m)
	}
	if m.Status != "open" || m.AssignedTo != "—" || m.CreatedBy != "Enterprise" {
		t.Fatalf("parsed assignment/created fields wrong: %+v", m)
	}
	if m.Created != "2026-07-15T00:00:00Z" || m.DocRef != "—" {
		t.Fatalf("parsed created/doc_ref wrong: %+v", m)
	}
	if body != "Some body text that must survive the round trip.\n" {
		t.Fatalf("body wrong: %q", body)
	}

	// Mutate status + assigned-to via the sanctioned update path, then reload.
	m.Status = "assigned"
	m.AssignedTo = "Phoenix"
	if err := d.DoTopicUpdate("task-round-trip", m, body); err != nil {
		t.Fatalf("DoTopicUpdate: %v", err)
	}

	m2, body2, err := d.DoTopicLoad("task-round-trip")
	if err != nil {
		t.Fatalf("DoTopicLoad after update: %v", err)
	}
	if m2.Status != "assigned" || m2.AssignedTo != "Phoenix" {
		t.Fatalf("mutated fields not persisted: %+v", m2)
	}
	// The fields we did NOT touch must survive the round trip.
	if m2.Kind != "task" || m2.CreatedBy != "Enterprise" ||
		m2.Created != "2026-07-15T00:00:00Z" || m2.DocRef != "—" {
		t.Fatalf("non-mutated fields dropped: %+v", m2)
	}
	if body2 != body {
		t.Fatalf("body not preserved: %q", body2)
	}

	// Unassign -> "—", and ensure it round-trips that way too.
	m2.Status = "open"
	m2.AssignedTo = "—"
	if err := d.DoTopicUpdate("task-round-trip", m2, body2); err != nil {
		t.Fatalf("DoTopicUpdate unassign: %v", err)
	}
	m3, _, err := d.DoTopicLoad("task-round-trip")
	if err != nil {
		t.Fatalf("DoTopicLoad after unassign: %v", err)
	}
	if m3.Status != "open" || m3.AssignedTo != "—" {
		t.Fatalf("unassign not persisted: %+v", m3)
	}
}

// TestTopicLoadMissing confirms a non-existent topic yields an error (so the
// task assign/unassign/status commands can map it to exit code 3).
func TestTopicLoadMissing(t *testing.T) {
	d := newTestDashboard(t)
	if _, _, err := d.DoTopicLoad("does-not-exist"); err == nil {
		t.Fatal("expected error for missing topic, got nil")
	}
}
