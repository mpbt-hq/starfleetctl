// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package comms

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

// RuntimeUpdate is an observation from the running OpenCode plugin. It changes
// only runtime/model-health fields; task ownership and the manual board state
// remain exclusively controlled by explicit task/status commands.
type RuntimeUpdate struct {
	Runtime     string
	Health      string
	EventID     string
	ObservedAt  string
	HealthReason string
	ErrorTag    string
	SessionID   string
	PID         int
	Model       string
	Provider    string
}

func (b *Bus) DoRuntimeUpdate(update RuntimeUpdate) error {
	if update.Runtime == "" && update.Health == "" {
		return fmt.Errorf("runtime update: need runtime and/or health")
	}
	switch update.Runtime {
	case "", "starting", "working", "waiting", "idle":
	default:
		return fmt.Errorf("runtime update: invalid runtime state %q", update.Runtime)
	}
	switch update.Health {
	case "", "healthy", "degraded", "error":
	default:
		return fmt.Errorf("runtime update: invalid health state %q (offline is derived from process/heartbeat liveness)", update.Health)
	}

	eventID := strings.TrimSpace(update.EventID)
	if !validRuntimeEventID(eventID) {
		return fmt.Errorf("runtime update: invalid event_id")
	}
	observedAt, err := time.Parse(time.RFC3339Nano, update.ObservedAt)
	if err != nil {
		return fmt.Errorf("runtime update: invalid observed_at: %w", err)
	}
	observedAt = observedAt.UTC()
	observed := observedAt.Format(time.RFC3339Nano)

	if update.SessionID != "" && len(update.SessionID) > 256 {
		return fmt.Errorf("runtime update: session_id exceeds 256 bytes")
	}
	if len(update.Model) > 256 || len(update.Provider) > 256 {
		return fmt.Errorf("runtime update: model/provider exceeds 256 bytes")
	}

	lock, err := b.lockBus()
	if err != nil {
		return err
	}
	defer lock.Close()

	path := b.sfile(b.ShipID)
	prev, _ := parseStatusFile(path)
	rec := prev
	if rec.Agent == "" {
		rec.Agent = b.ShipID
	}
	if rec.PID == 0 && update.PID > 0 {
		if !pidAliveCheck(update.PID) {
			return fmt.Errorf("runtime update: reported pid %d is not alive", update.PID)
		}
		rec.PID = update.PID
	}

	changed := false
	if update.Runtime != "" && acceptRuntimeEvent(eventID, observedAt, rec.RuntimeEventID, rec.RuntimeUpdated) {
		rec.Runtime = update.Runtime
		rec.RuntimeUpdated = observed
		rec.RuntimeEventID = eventID
		if update.SessionID != "" {
			rec.RuntimeSessionID = clean(update.SessionID)
		}
		if update.Runtime == "working" {
			rec.ModelLastAction = observed
		}
		changed = true
	}
	if update.Health != "" && acceptRuntimeEvent(eventID, observedAt, rec.HealthEventID, rec.HealthUpdated) {
		rec.Health = update.Health
		rec.HealthUpdated = observed
		rec.HealthEventID = eventID
		if update.Health == "healthy" {
			rec.HealthReason = ""
			rec.ErrorTag = ""
		} else {
			rec.HealthReason = truncateRuntimeDetail(clean(update.HealthReason), 512)
			if update.ErrorTag != "" {
				rec.ErrorTag = clean(update.ErrorTag)
			}
		}
		changed = true
	}
	if !changed {
		return nil // duplicate or stale observation; idempotent no-op
	}
	if update.PID > 0 && pidAliveCheck(update.PID) {
		rec.PID = update.PID
	}
	if update.Model != "" {
		rec.Model = clean(update.Model)
	}
	if update.Provider != "" {
		rec.Server = clean(update.Provider)
	}

	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(b.StatusDir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return err
	}
	b.LogEvent("runtime", fmt.Sprintf("runtime=%s health=%s event=%s session=%s",
		rec.Runtime, rec.Health, eventID, rec.RuntimeSessionID))
	return nil
}

func validRuntimeEventID(id string) bool {
	if id == "" || len(id) > 256 {
		return false
	}
	for _, r := range id {
		if r <= 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// acceptRuntimeEvent applies per-dimension idempotency and timestamp ordering.
// An event ID is consumed once; a different event must be strictly newer than
// the last accepted observation. Equal/older timestamps cannot regress state.
func acceptRuntimeEvent(eventID string, observedAt time.Time, previousID, previousAt string) bool {
	if eventID == previousID {
		return false
	}
	if previousAt == "" {
		return true
	}
	last, err := time.Parse(time.RFC3339Nano, previousAt)
	if err != nil {
		return true // recover from a corrupt legacy timestamp
	}
	return observedAt.After(last)
}

func truncateRuntimeDetail(value string, max int) string {
	if max <= 0 || len(value) <= max {
		return value
	}
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	return string(runes[:max])
}
