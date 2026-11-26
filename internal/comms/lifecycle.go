// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package comms

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/metux/starfleetctl/internal/fsutil"
)

const (
	eventLeaseDuration = 2 * time.Minute
	eventRetryBase     = 3 * time.Second
	eventRetryMax      = 5 * time.Minute
)

// EventLifecycle is the durable, transport-neutral state for one Comms event.
// SessionRef and MessageRef are opaque to Go; OpenCode-specific correlation is
// owned by the plugin adapter.
type EventLifecycle struct {
	EventID       string `json:"event_id"`
	Target        string `json:"target"`
	State         string `json:"state"`
	Consumer      string `json:"consumer,omitempty"`
	LeaseExpires  int64  `json:"lease_expires,omitempty"`
	Attempts      int    `json:"attempts"`
	NextRetry     int64  `json:"next_retry,omitempty"`
	SessionRef    string `json:"session_ref,omitempty"`
	MessageRef    string `json:"message_ref,omitempty"`
	UpdatedAt     int64  `json:"updated_at"`
	Error         string `json:"error,omitempty"`
}

var lifecycleOrder = map[string]int{
	"offered": 0,
	"received": 1,
	"prepared": 2,
	"submitted": 3,
	"turn_started": 4,
	"completed": 5,
}

func (b *Bus) lifecycleDir() string {
	return filepath.Join(b.BusDir, "event-lifecycle", fsafe(b.ShipID))
}

func (b *Bus) lifecyclePath(id string) (string, error) {
	safe, ok := fsutil.Safe(id)
	if !ok {
		return "", fmt.Errorf("events: invalid event id %q", id)
	}
	return filepath.Join(b.lifecycleDir(), safe+".json"), nil
}

func (b *Bus) readLifecycleLocked(id string) (*EventLifecycle, error) {
	path, err := b.lifecyclePath(id)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var record EventLifecycle
	if err := json.Unmarshal(data, &record); err != nil {
		return nil, fmt.Errorf("events: decode lifecycle %s: %w", id, err)
	}
	if record.EventID != id || record.Target != b.ShipID || !validLifecycleState(record.State) {
		return nil, fmt.Errorf("events: invalid lifecycle record for %s", id)
	}
	return &record, nil
}

// writeLifecycleLocked uses write+fsync+rename and then fsyncs the containing
// directory. Callers must hold the Comms bus lock.
func (b *Bus) writeLifecycleLocked(record *EventLifecycle) error {
	path, err := b.lifecyclePath(record.EventID)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".lifecycle-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	ok := false
	defer func() {
		tmp.Close()
		if !ok {
			_ = os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return err
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		return err
	}
	ok = true
	return nil
}

func validLifecycleState(state string) bool {
	if _, ok := lifecycleOrder[state]; ok {
		return true
	}
	return state == "failed"
}

func lifecycleCanTransition(from, to string) bool {
	if from == to {
		return true
	}
	if to == "failed" {
		return from != "completed" && validLifecycleState(from)
	}
	if from == "failed" {
		return false // retries are initiated only by a fresh poll after backoff
	}
	return lifecycleOrder[to] == lifecycleOrder[from]+1
}

func lifecycleRetryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := eventRetryBase
	for i := 1; i < attempt && delay < eventRetryMax; i++ {
		delay *= 2
	}
	if delay > eventRetryMax {
		return eventRetryMax
	}
	return delay
}

// claimDispatchEvents leases unread events to one adapter consumer. An empty
// consumer preserves the pre-lifecycle protocol for older plugin versions.
func (b *Bus) claimDispatchEvents(messages []Message, consumer string) ([]DispatchEvent, error) {
	events := eventsFromMessages(messages)
	if strings.TrimSpace(consumer) == "" {
		return events, nil
	}
	lock, err := b.lockBus()
	if err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	var claimed []DispatchEvent
	var completed []string
	for _, event := range events {
		// dispatchInbox is intentionally lock-free for its read path; recheck
		// acknowledgement under the lifecycle lock before acquiring a lease.
		if b.acked(event.ID, b.ShipID) {
			continue
		}
		record, err := b.readLifecycleLocked(event.ID)
		if err != nil {
			lock.Close()
			return nil, err
		}
		if record == nil {
			record = &EventLifecycle{
				EventID: event.ID, Target: b.ShipID, State: "offered", UpdatedAt: now,
			}
			if err := b.writeLifecycleLocked(record); err != nil {
				lock.Close()
				return nil, err
			}
		}
		if record.State == "completed" {
			completed = append(completed, event.ID)
			continue
		}
		if record.State == "failed" {
			if record.NextRetry > now {
				continue
			}
			record.State = "received"
			record.Error = ""
			record.NextRetry = 0
		}
		if record.Consumer != "" && record.Consumer != consumer && record.LeaseExpires > now {
			continue
		}
		if record.State == "offered" {
			record.State = "received"
		}
		if record.Consumer != consumer || record.LeaseExpires <= now {
			record.Attempts++
		}
		record.Consumer = consumer
		record.LeaseExpires = now + int64(eventLeaseDuration.Seconds())
		record.UpdatedAt = now
		if err := b.writeLifecycleLocked(record); err != nil {
			lock.Close()
			return nil, err
		}
		event.Lifecycle = &DispatchEventLifecycle{
			State: record.State, SessionRef: record.SessionRef, MessageRef: record.MessageRef,
		}
		claimed = append(claimed, event)
	}
	if err := lock.Close(); err != nil {
		return nil, err
	}
	// Reconcile the narrow crash window where lifecycle completion was durable
	// but the corresponding seen/ move was not yet written.
	for _, id := range completed {
		if err := b.DoAck(id, "correlated-turn-completed"); err != nil {
			return nil, err
		}
	}
	return claimed, nil
}

func (b *Bus) transitionEvent(req dispatchRequest) dispatchResponse {
	if req.ID == "" || strings.TrimSpace(req.Consumer) == "" || !validLifecycleState(req.LifecycleState) {
		return dispatchResponse{OK: false, Error: "events.transition: need id, consumer and valid lifecycle_state"}
	}
	lock, err := b.lockBus()
	if err != nil {
		return dispatchResponse{OK: false, Error: err.Error()}
	}
	record, err := b.readLifecycleLocked(req.ID)
	if err != nil {
		lock.Close()
		return dispatchResponse{OK: false, Error: err.Error()}
	}
	if record == nil {
		lock.Close()
		return dispatchResponse{OK: false, Error: "events.transition: event has not been offered"}
	}
	now := time.Now().Unix()
	if record.State == "failed" && req.LifecycleState == "failed" {
		_ = lock.Close()
		// Failed is terminal until the scheduled poll retry. Repeated failure
		// reports are idempotent and must not extend backoff or alter attempts.
		return dispatchResponse{OK: true, Lifecycle: record}
	}
	if record.State == "completed" {
		_ = lock.Close()
		// Completion is terminal. Delayed monotonic progress reports are
		// idempotent no-ops, including a submitted assertion from a retried
		// current-turn adapter after its original transition write failed.
		if req.LifecycleState == "completed" || req.LifecycleState == "turn_started" || req.LifecycleState == "submitted" {
			return dispatchResponse{OK: true, Lifecycle: record}
		}
		return dispatchResponse{OK: false, Error: "events.transition: completed event is terminal"}
	}
	if record.Consumer != req.Consumer || record.LeaseExpires <= now {
		_ = lock.Close()
		return dispatchResponse{OK: false, Error: "events.transition: lease not owned or expired"}
	}
	// Reasserting submitted is safe after the adapter has injected the event:
	// it must not regress a turn that has already started. This is especially
	// important when a previous submitted write failed transiently.
	if req.LifecycleState == "submitted" && lifecycleOrder[record.State] >= lifecycleOrder["submitted"] {
		_ = lock.Close()
		return dispatchResponse{OK: true, Lifecycle: record}
	}
	if !lifecycleCanTransition(record.State, req.LifecycleState) {
		_ = lock.Close()
		return dispatchResponse{OK: false, Error: fmt.Sprintf("events.transition: invalid transition %s -> %s", record.State, req.LifecycleState)}
	}
	if lifecycleOrder[req.LifecycleState] > lifecycleOrder[record.State] || req.LifecycleState == "failed" {
		record.State = req.LifecycleState
	}
	if req.SessionRef != "" {
		record.SessionRef = req.SessionRef
	}
	if req.MessageRef != "" {
		record.MessageRef = req.MessageRef
	}
	if req.LifecycleState == "failed" {
		detail := strings.TrimSpace(req.LifecycleError)
		if len(detail) > 512 {
			detail = detail[:512]
		}
		record.Error = detail
		record.NextRetry = now + int64(lifecycleRetryDelay(record.Attempts).Seconds())
		record.Consumer = ""
		record.LeaseExpires = 0
	}
	if req.LifecycleState == "completed" {
		record.Consumer = ""
		record.LeaseExpires = 0
		record.NextRetry = 0
		record.Error = ""
	}
	record.UpdatedAt = now
	if err := b.writeLifecycleLocked(record); err != nil {
		_ = lock.Close()
		return dispatchResponse{OK: false, Error: err.Error()}
	}
	if err := lock.Close(); err != nil {
		return dispatchResponse{OK: false, Error: err.Error()}
	}
	if req.LifecycleState == "completed" {
		if err := b.DoAck(req.ID, "correlated-turn-completed"); err != nil {
			return dispatchResponse{OK: false, Error: "lifecycle completed but ack reconciliation failed: " + err.Error(), Lifecycle: record}
		}
	}
	return dispatchResponse{OK: true, Lifecycle: record}
}

func (b *Bus) releaseEvents(req dispatchRequest) dispatchResponse {
	if strings.TrimSpace(req.Consumer) == "" || len(req.IDs) == 0 {
		return dispatchResponse{OK: false, Error: "events.release: need consumer and ids"}
	}
	lock, err := b.lockBus()
	if err != nil {
		return dispatchResponse{OK: false, Error: err.Error()}
	}
	now := time.Now().Unix()
	for _, id := range req.IDs {
		record, err := b.readLifecycleLocked(id)
		if err != nil {
			_ = lock.Close()
			return dispatchResponse{OK: false, Error: err.Error()}
		}
		if record == nil || record.Consumer == "" {
			continue
		}
		if record.Consumer != req.Consumer {
			_ = lock.Close()
			return dispatchResponse{OK: false, Error: "events.release: lease owned by another consumer"}
		}
		record.Consumer = ""
		record.LeaseExpires = 0
		record.UpdatedAt = now
		if err := b.writeLifecycleLocked(record); err != nil {
			_ = lock.Close()
			return dispatchResponse{OK: false, Error: err.Error()}
		}
	}
	if err := lock.Close(); err != nil {
		return dispatchResponse{OK: false, Error: err.Error()}
	}
	return dispatchResponse{OK: true}
}
