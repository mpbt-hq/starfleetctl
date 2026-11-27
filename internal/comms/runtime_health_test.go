// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package comms

import (
	"os"
	"testing"
)

func TestDoStatusAndHealthUpdatePreserveRuntimeHealth(t *testing.T) {
	b := newTestBus(t, "TestShip")
	if err := b.DoStatus("working", "current task", StatusPatch{Task: "task-30"}); err != nil {
		t.Fatal(err)
	}
	if err := b.DoHealthUpdate([]string{"--plugin-version", "2.5.20"}); err != nil {
		t.Fatal(err)
	}
	rec, ok := parseStatusFile(b.sfile(b.ShipID))
	if !ok {
		t.Fatal("missing status record")
	}
	rec.Runtime = "waiting"
	rec.Health = "degraded"
	rec.RuntimeUpdated = "2026-11-27T12:00:00Z"
	rec.HealthUpdated = "2026-11-27T12:01:00Z"
	rec.HealthReason = "provider retry"
	writeStatusRecord(t, b, b.ShipID, rec)

	if err := b.DoStatus("working", "still same task", StatusPatch{}); err != nil {
		t.Fatal(err)
	}
	if err := b.DoHealthUpdate([]string{"--plugin-ts", "2026-11-27T12:02:00Z"}); err != nil {
		t.Fatal(err)
	}
	got, ok := parseStatusFile(b.sfile(b.ShipID))
	if !ok {
		t.Fatal("missing updated status record")
	}
	if got.Runtime != "waiting" || got.Health != "degraded" ||
		got.RuntimeUpdated != rec.RuntimeUpdated || got.HealthUpdated != rec.HealthUpdated ||
		got.HealthReason != rec.HealthReason {
		t.Fatalf("runtime/health fields were lost: got %+v want runtime/health from %+v", got, rec)
	}
	if got.State != "working" || got.Task != "task-30" {
		t.Fatalf("runtime/health preservation changed manual task state: got state=%q task=%q", got.State, got.Task)
	}
}

func TestDispatchModelErrorsDoNotOverwriteManualTaskState(t *testing.T) {
	b := newTestBus(t, "TestShip")
	if err := b.DoStatus("working", "implementation", StatusPatch{Task: "task-30"}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		run  func() dispatchResponse
	}{
		{
			name: "error",
			run: func() dispatchResponse {
				return b.dispatchError(dispatchRequest{Detail: "ResourceExhausted: provider temporarily overloaded"})
			},
		},
		{
			name: "error-handle",
			run: func() dispatchResponse {
				return b.dispatchErrorHandle(dispatchRequest{
					Detail: "ResourceExhausted: provider temporarily overloaded",
					Source: "session.error",
				})
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := tc.run()
			if !resp.OK {
				t.Fatalf("dispatch failed: %+v", resp)
			}
			rec, ok := parseStatusFile(b.sfile(b.ShipID))
			if !ok {
				t.Fatal("missing status record")
			}
			if rec.State != "working" || rec.Task != "task-30" {
				t.Fatalf("model error overwrote task/manual state: state=%q task=%q", rec.State, rec.Task)
			}
			if rec.ErrorTag == "" {
				t.Fatal("expected model error classification to remain recorded")
			}
		})
	}
}

func TestDoErrorHandleDoesNotOverwriteManualTaskState(t *testing.T) {
	b := newTestBus(t, "TestShip")
	if err := b.DoStatus("working", "implementation", StatusPatch{Task: "task-30"}); err != nil {
		t.Fatal(err)
	}

	input, err := os.CreateTemp(t.TempDir(), "error-payload-*.json")
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	if _, err := input.WriteString(`{"detail":"ResourceExhausted: provider temporarily overloaded"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := input.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	previousStdin := os.Stdin
	os.Stdin = input
	defer func() { os.Stdin = previousStdin }()

	if err := b.DoErrorHandle([]string{"--stdin"}); err != nil {
		t.Fatal(err)
	}
	rec, ok := parseStatusFile(b.sfile(b.ShipID))
	if !ok {
		t.Fatal("missing status record")
	}
	if rec.State != "working" || rec.Task != "task-30" {
		t.Fatalf("legacy model error handler overwrote task/manual state: state=%q task=%q", rec.State, rec.Task)
	}
	if rec.ErrorTag != "resource-exhausted" {
		t.Fatalf("error tag = %q, want resource-exhausted", rec.ErrorTag)
	}
}
