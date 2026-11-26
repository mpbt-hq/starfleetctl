// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package comms

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestEventLifecycleTransitionsAreMonotonicAndAckOnlyOnCompletion(t *testing.T) {
	b := newTestBus(t, "Enterprise")
	id, err := b.post(b.ShipID, "directive", "", "", "", "ship")
	if err != nil { t.Fatal(err) }

	poll := b.dispatch(dispatchRequest{Cmd: "events.poll", Consumer: "Enterprise:123"})
	if !poll.OK || poll.Events == nil || len(*poll.Events) != 1 { t.Fatalf("poll = %#v", poll) }
	if b.acked(id, b.ShipID) { t.Fatal("poll must not acknowledge an event") }

	for _, state := range []string{"prepared", "submitted", "turn_started"} {
		resp := b.dispatch(dispatchRequest{Cmd: "events.transition", ID: id, Consumer: "Enterprise:123", LifecycleState: state, SessionRef: "ses_1", MessageRef: "msg_1"})
		if !resp.OK { t.Fatalf("%s transition failed: %s", state, resp.Error) }
		if b.acked(id, b.ShipID) { t.Fatalf("%s must not acknowledge an event", state) }
	}
	done := b.dispatch(dispatchRequest{Cmd: "events.transition", ID: id, Consumer: "Enterprise:123", LifecycleState: "completed"})
	if !done.OK { t.Fatalf("completed transition failed: %s", done.Error) }
	if !b.acked(id, b.ShipID) { t.Fatal("correlated completion must acknowledge the event") }
	if done.Lifecycle == nil || done.Lifecycle.State != "completed" { t.Fatalf("completion record = %#v", done.Lifecycle) }

	// A repeated completion is idempotent, including after the lease is released.
	again := b.dispatch(dispatchRequest{Cmd: "events.transition", ID: id, Consumer: "Enterprise:123", LifecycleState: "completed"})
	if !again.OK { t.Fatalf("duplicate completion failed: %s", again.Error) }
	// A delayed start report after completion is also a harmless monotonic no-op.
	staleStart := b.dispatch(dispatchRequest{Cmd: "events.transition", ID: id, Consumer: "Enterprise:123", LifecycleState: "turn_started"})
	if !staleStart.OK || staleStart.Lifecycle == nil || staleStart.Lifecycle.State != "completed" {
		t.Fatalf("stale turn_started after completion = %#v", staleStart)
	}
}

func TestEventLifecycleCanReassertSubmittedWithoutRegressingActiveTurn(t *testing.T) {
	b := newTestBus(t, "Enterprise")
	id, err := b.post(b.ShipID, "directive", "", "", "", "ship")
	if err != nil {
		t.Fatal(err)
	}
	poll := b.dispatch(dispatchRequest{Cmd: "events.poll", Consumer: "c1"})
	if !poll.OK || poll.Events == nil || len(*poll.Events) != 1 {
		t.Fatalf("poll = %#v", poll)
	}
	for _, state := range []string{"prepared", "submitted", "turn_started"} {
		resp := b.dispatch(dispatchRequest{Cmd: "events.transition", ID: id, Consumer: "c1", LifecycleState: state})
		if !resp.OK {
			t.Fatalf("%s transition failed: %s", state, resp.Error)
		}
	}
	reassert := b.dispatch(dispatchRequest{Cmd: "events.transition", ID: id, Consumer: "c1", LifecycleState: "submitted"})
	if !reassert.OK || reassert.Lifecycle == nil || reassert.Lifecycle.State != "turn_started" {
		t.Fatalf("submitted reassertion regressed or failed: %#v", reassert)
	}
	done := b.dispatch(dispatchRequest{Cmd: "events.transition", ID: id, Consumer: "c1", LifecycleState: "completed"})
	if !done.OK {
		t.Fatalf("completion failed: %s", done.Error)
	}
	afterDone := b.dispatch(dispatchRequest{Cmd: "events.transition", ID: id, Consumer: "c1", LifecycleState: "submitted"})
	if !afterDone.OK || afterDone.Lifecycle == nil || afterDone.Lifecycle.State != "completed" {
		t.Fatalf("submitted reassertion after completion failed: %#v", afterDone)
	}
}

func TestEventLifecycleRejectsSkippedAndRegressiveTransitions(t *testing.T) {
	b := newTestBus(t, "Enterprise")
	id, err := b.post(b.ShipID, "directive", "", "", "", "ship")
	if err != nil { t.Fatal(err) }
	if resp := b.dispatch(dispatchRequest{Cmd: "events.poll", Consumer: "c1"}); !resp.OK { t.Fatal(resp.Error) }

	duplicate := b.dispatch(dispatchRequest{Cmd: "events.transition", ID: id, Consumer: "c1", LifecycleState: "received"})
	if !duplicate.OK { t.Fatalf("duplicate received transition must be idempotent: %s", duplicate.Error) }
	for _, state := range []string{"completed", "offered"} {
		resp := b.dispatch(dispatchRequest{Cmd: "events.transition", ID: id, Consumer: "c1", LifecycleState: state})
		if resp.OK { t.Fatalf("unexpectedly accepted transition to %s", state) }
	}
	if b.acked(id, b.ShipID) { t.Fatal("invalid transition must not ack the event") }
}

func TestEventLifecycleLeaseExclusivityAndExpiryRecovery(t *testing.T) {
	b := newTestBus(t, "Enterprise")
	id, err := b.post(b.ShipID, "directive", "", "", "", "ship")
	if err != nil { t.Fatal(err) }

	start := make(chan struct{})
	results := make(chan dispatchResponse, 2)
	var wg sync.WaitGroup
	for _, consumer := range []string{"consumer-1", "consumer-2"} {
		wg.Add(1)
		go func(consumer string) {
			defer wg.Done()
			<-start
			results <- b.dispatch(dispatchRequest{Cmd: "events.poll", Consumer: consumer})
		}(consumer)
	}
	close(start)
	wg.Wait()
	close(results)
	claimed := 0
	for result := range results {
		if !result.OK { t.Fatalf("concurrent poll failed: %s", result.Error) }
		if result.Events != nil { claimed += len(*result.Events) }
	}
	if claimed != 1 { t.Fatalf("concurrent consumers claimed %d copies, want exactly one", claimed) }

	lock, err := b.lockBus()
	if err != nil { t.Fatal(err) }
	record, err := b.readLifecycleLocked(id)
	if err != nil || record == nil { t.Fatalf("read lifecycle: record=%#v err=%v", record, err) }
	winner := record.Consumer
	if winner != "consumer-1" && winner != "consumer-2" { t.Fatalf("unexpected lease owner %q", winner) }
	other := "consumer-1"
	if winner == other { other = "consumer-2" }
	record.LeaseExpires = time.Now().Add(-time.Second).Unix()
	if err := b.writeLifecycleLocked(record); err != nil { t.Fatal(err) }
	if err := lock.Close(); err != nil { t.Fatal(err) }

	recovered := b.dispatch(dispatchRequest{Cmd: "events.poll", Consumer: other})
	if !recovered.OK || recovered.Events == nil || len(*recovered.Events) != 1 { t.Fatalf("expired lease not recovered: %#v", recovered) }
	lock, err = b.lockBus()
	if err != nil { t.Fatal(err) }
	record, err = b.readLifecycleLocked(id)
	if err != nil || record == nil { t.Fatalf("read recovered lifecycle: record=%#v err=%v", record, err) }
	if record.Consumer != other || record.Attempts != 2 { t.Fatalf("recovered record = %#v", record) }
	_ = lock.Close()
}

func TestEventLifecyclePersistsAcrossBusRecreationAndRetriesFailure(t *testing.T) {
	root := t.TempDir()
	t.Setenv("STARFLEET_SHIP_ID", "Enterprise")
	t.Setenv("STARFLEET_BUS_DIR", filepath.Join(root, ".starfleet-ai", "var", "comms"))
	b, err := New(root)
	if err != nil { t.Fatal(err) }
	id, err := b.post(b.ShipID, "directive", "", "", "", "ship")
	if err != nil { t.Fatal(err) }
	poll := b.dispatch(dispatchRequest{Cmd: "events.poll", Consumer: "process-1"})
	if !poll.OK || poll.Events == nil || len(*poll.Events) != 1 { t.Fatalf("poll = %#v", poll) }
	if (*poll.Events)[0].Lifecycle == nil || (*poll.Events)[0].Lifecycle.State != "received" {
		t.Fatalf("poll lifecycle = %#v, want received", (*poll.Events)[0].Lifecycle)
	}
	failed := b.dispatch(dispatchRequest{Cmd: "events.transition", ID: id, Consumer: "process-1", LifecycleState: "failed", LifecycleError: "temporary provider error"})
	if !failed.OK { t.Fatalf("failure transition: %s", failed.Error) }
	duplicateFailure := b.dispatch(dispatchRequest{Cmd: "events.transition", ID: id, Consumer: "process-1", LifecycleState: "failed", LifecycleError: "duplicate failure"})
	if !duplicateFailure.OK || duplicateFailure.Lifecycle == nil ||
		duplicateFailure.Lifecycle.NextRetry != failed.Lifecycle.NextRetry ||
		duplicateFailure.Lifecycle.Attempts != failed.Lifecycle.Attempts ||
		duplicateFailure.Lifecycle.Error != failed.Lifecycle.Error {
		t.Fatalf("duplicate failure changed durable retry state: first=%#v duplicate=%#v", failed.Lifecycle, duplicateFailure.Lifecycle)
	}

	// A fresh Bus instance models a process restart over the same state directory.
	b2, err := New(root)
	if err != nil { t.Fatal(err) }
	lock, err := b2.lockBus()
	if err != nil { t.Fatal(err) }
	record, err := b2.readLifecycleLocked(id)
	if err != nil || record == nil { t.Fatalf("persisted record missing: %#v err=%v", record, err) }
	if record.State != "failed" || record.Attempts != 1 || record.Error != "temporary provider error" { t.Fatalf("persisted failure = %#v", record) }
	record.NextRetry = time.Now().Add(-time.Second).Unix()
	if err := b2.writeLifecycleLocked(record); err != nil { t.Fatal(err) }
	_ = lock.Close()

	retry := b2.dispatch(dispatchRequest{Cmd: "events.poll", Consumer: "process-2"})
	if !retry.OK || retry.Events == nil || len(*retry.Events) != 1 { t.Fatalf("failed event not retried after backoff: %#v", retry) }
	lock, err = b2.lockBus()
	if err != nil { t.Fatal(err) }
	record, err = b2.readLifecycleLocked(id)
	if err != nil || record == nil { t.Fatalf("read retry record: %#v err=%v", record, err) }
	if record.State != "received" || record.Consumer != "process-2" || record.Attempts != 2 { t.Fatalf("retry record = %#v", record) }
	_ = lock.Close()
	if _, err := os.Stat(filepath.Join(b2.lifecycleDir(), id+".json")); err != nil { t.Fatalf("lifecycle file missing: %v", err) }
}

func TestEventLifecycleReleaseAllowsAnotherConsumer(t *testing.T) {
	b := newTestBus(t, "Enterprise")
	id, err := b.post(b.ShipID, "directive", "", "", "", "ship")
	if err != nil { t.Fatal(err) }
	if resp := b.dispatch(dispatchRequest{Cmd: "events.poll", Consumer: "c1"}); !resp.OK { t.Fatal(resp.Error) }
	wrongRelease := b.dispatch(dispatchRequest{Cmd: "events.release", Consumer: "c2", IDs: []string{id}})
	if wrongRelease.OK { t.Fatal("non-owner released another consumer's lease") }
	release := b.dispatch(dispatchRequest{Cmd: "events.release", Consumer: "c1", IDs: []string{id}})
	if !release.OK { t.Fatalf("release failed: %s", release.Error) }
	next := b.dispatch(dispatchRequest{Cmd: "events.poll", Consumer: "c2"})
	if !next.OK || next.Events == nil || len(*next.Events) != 1 { t.Fatalf("released event not claimable: %#v", next) }
}
