// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
//
// Package timer implements fleet-wide scheduling: one-time, interval, and
// cron-based timers that fire comms directives or system commands when they expire.
//
// Timers come in two flavours:
//
//   - Ephemeral: stored under .starfleet-ai/var/timers/ — lost on workspace reset.
//     Default for --every and --at schedules.
//
//   - Persistent: stored under .starfleet-ai/conf/timers/ (config) and
//     .starfleet-ai/var/timers/ (runtime state) — survive workspace resets.
//     Default for --cron schedules.
//
// Timer types:
//
//   - "ship" / "command": fire comms directives to ships via bus.DoPost().
//   - "system": execute workspace-level commands (reindex, web, …) directly
//     in the worker process — no agent involvement needed.
//
// A single worker daemon (timer worker) polls all timer directories every 2s,
// resolves fleet targets at fire time, and sends comms directives via
// bus.DoPost(). System commands run in a goroutine to keep the worker responsive.
// The worker is a singleton per workspace (PID-file guarded).
package timer

import (
	"fmt"
	"math/rand"
	"time"
)

// ScheduleType defines when/how often a timer fires.
type ScheduleType string

const (
	ScheduleOnce     ScheduleType = "once"
	ScheduleInterval ScheduleType = "interval"
	ScheduleCron     ScheduleType = "cron"
)

// TargetSpec defines where a timer's directive is sent on fire.
type TargetSpec struct {
	Type  TargetType `json:"type"`
	Value string     `json:"value"` // ship ID when Type == TargetShip
}

// TargetType defines how to resolve the fire target.
type TargetType string

const (
	TargetShip     TargetType = "ship"      // specific ship
	TargetFleet    TargetType = "fleet"     // pick ONE idle ship
	TargetFleetAll TargetType = "fleet-all" // all idle ships
	TargetSystem   TargetType = "system"    // execute directly in worker (no ship)
)

// Schedule holds the timing configuration for a timer.
type Schedule struct {
	Type        ScheduleType `json:"type"`
	CronExpr    string       `json:"cron_expr,omitempty"`
	IntervalSec int64        `json:"interval_sec,omitempty"`
	FireAt      int64        `json:"fire_at,omitempty"`
}

// TimerRecord is the on-disk representation of a single timer.
// The ID field doubles as the unique name — the JSON file is named <id>.json.
// For type="ship"/"command", the target+text fields control dispatch.
// For type="system", the cmd field holds the command line (target/text unused).
type TimerRecord struct {
	ID          string     `json:"id"`                    // unique key, also the filename
	Description string     `json:"description,omitempty"` // human-readable description
	Owner       string     `json:"owner"`
	Target      TargetSpec `json:"target,omitempty"` // used by ship/command timers
	Type        string     `json:"type"`             // "ship", "command", or "system"
	Text        string     `json:"text,omitempty"`   // message body (ship/command)
	Cmd         []string   `json:"cmd,omitempty"`    // command line (system only)
	Schedule    Schedule   `json:"schedule"`
	Timezone    string     `json:"timezone,omitempty"`
	Persistent  bool       `json:"persistent"`
	Enabled     bool       `json:"enabled"`
	CreatedAt   int64      `json:"created_at"`
	NextFire    int64      `json:"next_fire"`
}

// IsDue reports whether the timer should fire at the given time.
func (t *TimerRecord) IsDue(now time.Time) bool {
	return t.Enabled && t.NextFire > 0 && t.NextFire <= now.Unix()
}

// randomAdjectives and randomNouns generate short, memorable timer names.
var randomAdjectives = []string{
	"quick", "bright", "calm", "dark", "eager", "fair", "glad", "keen",
	"loud", "mild", "neat", "proud", "rare", "safe", "tall", "warm",
	"bold", "cool", "deep", "fast", "gold", "iron", "lazy", "wild",
}

var randomNouns = []string{
	"fox", "owl", "elm", "oak", "jay", "ray", "sky", "bay",
	"cat", "dog", "bee", "ant", "ram", "yak", "ape", "fox",
	"star", "moon", "hill", "lake", "rock", "tide", "wind", "fire",
}

// GenerateName creates a random timer name like "quick-fox-42".
func GenerateName() string {
	adj := randomAdjectives[rand.Intn(len(randomAdjectives))]
	noun := randomNouns[rand.Intn(len(randomNouns))]
	num := rand.Intn(100)
	return fmt.Sprintf("%s-%s-%d", adj, noun, num)
}
