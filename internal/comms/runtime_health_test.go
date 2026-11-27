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
	if rec.PluginLastRun != "" || rec.ModelLastAction != "" {
		t.Fatalf("metadata-only health update fabricated liveness/model activity: plugin=%q model=%q", rec.PluginLastRun, rec.ModelLastAction)
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
	if rec.ErrorTag != "nim-overload" {
		t.Fatalf("error tag = %q, want nim-overload", rec.ErrorTag)
	}
}

func TestBoardEntriesExposeRuntimeHealth(t *testing.T) {
	b := newTestBus(t, "TestShip")
	if err := b.DoStatus("working", "implementation", StatusPatch{Task: "task-30"}); err != nil {
		t.Fatal(err)
	}
	rec, ok := parseStatusFile(b.sfile(b.ShipID))
	if !ok {
		t.Fatal("missing status record")
	}
	rec.Runtime = "working"
	rec.Health = "healthy"
	rec.RuntimeUpdated = "2026-11-27T12:10:00Z"
	rec.HealthUpdated = "2026-11-27T12:10:00Z"
	writeStatusRecord(t, b, b.ShipID, rec)

	entries := b.BoardEntries()
	if len(entries) != 1 {
		t.Fatalf("board entries = %d, want 1", len(entries))
	}
	entry := entries[0]
	if entry.Runtime != "working" || entry.Health != "healthy" ||
		entry.RuntimeUpdated != rec.RuntimeUpdated || entry.HealthUpdated != rec.HealthUpdated {
		t.Fatalf("board projection omitted runtime/health fields: got %+v", entry)
	}
}


func TestDoHealthUpdateSerializesWithTaskStatusWrites(t *testing.T) {
	b := newTestBus(t, "TestShip")
	lock, err := b.lockBus()
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- b.DoHealthUpdate([]string{"--plugin-version", "2.5.20"})
	}()

	select {
	case err := <-done:
		_ = lock.Close()
		t.Fatalf("health update did not wait for the shared bus lock: %v", err)
	case <-time.After(300 * time.Millisecond):
	}

	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("health update after lock release: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("health update did not resume after bus lock release")
	}
}

func TestDoRuntimeUpdateIsIdempotentAndPreservesTaskState(t *testing.T) {
	b := newTestBus(t, "TestShip")
	if err := b.DoStatus("working", "implementation", StatusPatch{Task: "task-30"}); err != nil {
		t.Fatal(err)
	}
	first := RuntimeUpdate{
		Runtime: "working", Health: "error", EventID: "turn:session:message:1",
		ObservedAt: "2026-11-27T12:00:00Z", HealthReason: "provider overloaded",
		ErrorTag: "nim-overload", SessionID: "ses-runtime", PID: os.Getpid(),
		Model: "model-a", Provider: "provider-a",
	}
	if err := b.DoRuntimeUpdate(first); err != nil {
		t.Fatal(err)
	}
	rec, ok := parseStatusFile(b.sfile(b.ShipID))
	if !ok {
		t.Fatal("missing runtime status")
	}
	if rec.Runtime != "working" || rec.Health != "error" ||
		rec.RuntimeEventID != first.EventID || rec.HealthEventID != first.EventID ||
		rec.RuntimeSessionID != first.SessionID || rec.HealthReason != first.HealthReason ||
		rec.ErrorTag != first.ErrorTag || rec.Model != first.Model || rec.Server != first.Provider {
		t.Fatalf("runtime observation not persisted: %+v", rec)
	}
	if rec.State != "working" || rec.Task != "task-30" {
		t.Fatalf("runtime update changed task state: state=%q task=%q", rec.State, rec.Task)
	}
	if rec.ModelLastAction != first.ObservedAt {
		t.Fatalf("model last action = %q, want %q", rec.ModelLastAction, first.ObservedAt)
	}

	// Replaying the same event ID with a newer timestamp cannot change either
	// dimension; a new successful event is required for recovery.
	duplicate := RuntimeUpdate{
		Runtime: "idle", Health: "healthy", EventID: first.EventID,
		ObservedAt: "2026-11-27T12:05:00Z", SessionID: first.SessionID,
	}
	if err := b.DoRuntimeUpdate(duplicate); err != nil {
		t.Fatal(err)
	}
	rec, _ = parseStatusFile(b.sfile(b.ShipID))
	if rec.Runtime != "working" || rec.Health != "error" || rec.HealthReason != first.HealthReason {
		t.Fatalf("duplicate event changed runtime/health: %+v", rec)
	}

	recovery := RuntimeUpdate{
		Runtime: "idle", Health: "healthy", EventID: "turn:session:message:2",
		ObservedAt: "2026-11-27T12:06:00Z", SessionID: first.SessionID,
	}
	if err := b.DoRuntimeUpdate(recovery); err != nil {
		t.Fatal(err)
	}
	rec, _ = parseStatusFile(b.sfile(b.ShipID))
	if rec.Runtime != "idle" || rec.Health != "healthy" || rec.HealthReason != "" || rec.ErrorTag != "" {
		t.Fatalf("newer successful event did not clear recoverable health error: %+v", rec)
	}
	if rec.State != "working" || rec.Task != "task-30" {
		t.Fatalf("recovery changed task state: state=%q task=%q", rec.State, rec.Task)
	}
}

func TestDoRuntimeUpdateRejectsOutOfOrderEvents(t *testing.T) {
	b := newTestBus(t, "TestShip")
	if err := b.DoRuntimeUpdate(RuntimeUpdate{
		Runtime: "working", EventID: "event-new", ObservedAt: "2026-11-27T12:10:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	if err := b.DoRuntimeUpdate(RuntimeUpdate{
		Runtime: "idle", EventID: "event-old", ObservedAt: "2026-11-27T12:09:59Z",
	}); err != nil {
		t.Fatal(err)
	}
	rec, ok := parseStatusFile(b.sfile(b.ShipID))
	if !ok {
		t.Fatal("missing runtime status")
	}
	if rec.Runtime != "working" || rec.RuntimeEventID != "event-new" {
		t.Fatalf("out-of-order event regressed runtime state: %+v", rec)
	}
}

func TestDoRuntimeUpdateValidatesStatesAndEventEnvelope(t *testing.T) {
	b := newTestBus(t, "TestShip")
	tests := []struct {
		name string
		update RuntimeUpdate
	}{
		{name: "runtime offline is derived", update: RuntimeUpdate{Runtime: "offline", EventID: "e1", ObservedAt: "2026-11-27T12:00:00Z"}},
		{name: "health offline is derived", update: RuntimeUpdate{Health: "offline", EventID: "e2", ObservedAt: "2026-11-27T12:00:00Z"}},
		{name: "event id required", update: RuntimeUpdate{Runtime: "working", ObservedAt: "2026-11-27T12:00:00Z"}},
		{name: "timestamp required", update: RuntimeUpdate{Runtime: "working", EventID: "e3"}},
		{name: "event id cannot contain whitespace", update: RuntimeUpdate{Runtime: "working", EventID: "bad id", ObservedAt: "2026-11-27T12:00:00Z"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := b.DoRuntimeUpdate(tc.update); err == nil {
				t.Fatal("expected invalid runtime update to be rejected")
			}
		})
	}
	if _, ok := parseStatusFile(b.sfile(b.ShipID)); ok {
		t.Fatal("invalid observations must not create or mutate status")
	}
}


func TestDoHealthUpdateStoresHeartbeatCadenceWithoutModelActivity(t *testing.T) {
	b := newTestBus(t, "TestShip")
	ts := "2026-11-27T12:20:00Z"
	if err := b.DoHealthUpdate([]string{"--plugin-ts", ts, "--heartbeat-ms", "300000"}); err != nil {
		t.Fatal(err)
	}
	rec, ok := parseStatusFile(b.sfile(b.ShipID))
	if !ok {
		t.Fatal("missing status record")
	}
	if rec.PluginLastRun != ts || rec.PluginHeartbeatMS != 300000 {
		t.Fatalf("heartbeat metadata not persisted: plugin_last_run=%q heartbeat_ms=%d", rec.PluginLastRun, rec.PluginHeartbeatMS)
	}
	if rec.ModelLastAction != "" {
		t.Fatalf("heartbeat falsely recorded model activity at %q", rec.ModelLastAction)
	}
}

func TestEvalHealthUsesHeartbeatCadenceAndSeparatesRuntimeHealth(t *testing.T) {
	now := time.Now()
	rec := StatusRecord{
		State: "working",
		PID: os.Getpid(),
		PluginLastRun: now.Add(-400 * time.Second).Format(time.RFC3339Nano),
		PluginHeartbeatMS: 300000,
		Runtime: "idle",
		Health: "healthy",
		RuntimeUpdated: now.Add(-5 * time.Second).Format(time.RFC3339Nano),
		HealthUpdated: now.Add(-5 * time.Second).Format(time.RFC3339Nano),
	}
	got := EvalHealth("TestShip", rec, 0, 300)
	if got.StalePluginS != 630 {
		t.Fatalf("heartbeat-derived stale threshold = %d, want 630", got.StalePluginS)
	}
	if got.State != "IDLE" {
		t.Fatalf("runtime idle should not inherit manual task state or be marked stale: %+v", got)
	}
	if got.TaskState != "working" || got.Runtime != "idle" || got.Health != "healthy" {
		t.Fatalf("task/runtime/health dimensions were conflated: %+v", got)
	}
}

func TestEvalHealthMarksExpiredHeartbeatOffline(t *testing.T) {
	now := time.Now()
	rec := StatusRecord{
		State: "working",
		PID: os.Getpid(),
		PluginLastRun: now.Add(-700 * time.Second).Format(time.RFC3339Nano),
		PluginHeartbeatMS: 300000,
		Runtime: "working",
		Health: "healthy",
	}
	got := EvalHealth("TestShip", rec, 0, 300)
	if got.State != "OFFLINE" {
		t.Fatalf("expired heartbeat state = %q, want OFFLINE", got.State)
	}
}

func TestEvalHealthDistinguishesLiveModelErrorFromDeadProcess(t *testing.T) {
	now := time.Now()
	live := StatusRecord{
		State: "working",
		PID: os.Getpid(),
		PluginLastRun: now.Format(time.RFC3339Nano),
		Runtime: "waiting",
		Health: "error",
		HealthReason: "provider rejected request",
		ErrorTag: "nim-overload",
	}
	if got := EvalHealth("TestShip", live, 0, 300); got.State != "ERROR" {
		t.Fatalf("live model error state = %q, want ERROR", got.State)
	}
	dead := live
	dead.PID = 99999999
	dead.Health = "healthy"
	if got := EvalHealth("TestShip", dead, 0, 300); got.State != "OFFLINE" {
		t.Fatalf("dead process state = %q, want OFFLINE", got.State)
	}
}

func TestEvalHealthUsesRuntimeWorkingForStuckDetection(t *testing.T) {
	now := time.Now()
	rec := StatusRecord{
		State: "idle",
		PID: os.Getpid(),
		PluginLastRun: now.Format(time.RFC3339Nano),
		Runtime: "working",
		Health: "healthy",
		ModelLastAction: now.Add(-600 * time.Second).Format(time.RFC3339Nano),
	}
	got := EvalHealth("TestShip", rec, 0, 300)
	if got.State != "STUCK" {
		t.Fatalf("runtime working with stale model activity = %q, want STUCK", got.State)
	}
}

func TestDispatchRuntimeUpdatePersistsRuntimeObservation(t *testing.T) {
	b := newTestBus(t, "TestShip")
	resp := b.dispatchRuntimeUpdate(dispatchRequest{
		Runtime: "working",
		Health: "healthy",
		EventID: "dispatch:turn:1",
		ObservedAt: "2026-11-27T12:30:00Z",
		SessionID: "ses-dispatch",
		PID: os.Getpid(),
		Model: "model-dispatch",
		Server: "provider-dispatch",
	})
	if !resp.OK {
		t.Fatalf("runtime.update dispatch failed: %+v", resp)
	}
	rec, ok := parseStatusFile(b.sfile(b.ShipID))
	if !ok {
		t.Fatal("missing status record")
	}
	if rec.Runtime != "working" || rec.Health != "healthy" ||
		rec.RuntimeEventID != "dispatch:turn:1" || rec.RuntimeSessionID != "ses-dispatch" ||
		rec.Model != "model-dispatch" || rec.Server != "provider-dispatch" {
		t.Fatalf("runtime.update dispatch did not persist request: %+v", rec)
	}
}
