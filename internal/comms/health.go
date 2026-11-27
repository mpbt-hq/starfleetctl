// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package comms

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	defaultStalePlugin = 0   // auto: 2*heartbeat + 30s; legacy fallback 600s
	defaultStaleModel  = 300 // model_last_action older than this (while working) → STUCK
)

const healthUsage = `comms health [flags]

Fleet liveness watchdog — reads per-ship status files and reports
unresponsive ships.

Flags:
  --json              output JSON (array of ship objects) instead of a table
  --loop [SEC]        run continuously; default interval 30s (--once is default)
  --stale-plugin N    override heartbeat-derived liveness timeout (seconds)
  --stale-model N     model_last_action age (s) above which a working ship is
                      STUCK (300)
  -h, --help          this help

Effective state prioritizes liveness (OFFLINE), model health (ERROR/DEGRADED),
and correlated runtime state (STARTING/WORKING/WAITING/IDLE). A legacy manual
offline state is STOPPED; task state is reported separately. The default
heartbeat timeout is twice the plugin-reported interval plus 30s (600s for
legacy records without an interval). Exit code 1 for OFFLINE/ERROR/DEGRADED/
BLOCKED/STUCK ships (non-loop mode only), 0 otherwise.
`

// healthEntry mirrors one ship's evaluated health for the watchdog output.
type healthEntry struct {
	Ship              string `json:"ship"`
	State             string `json:"state"`
	RawState          string `json:"raw_state"`
	TaskState         string `json:"task_state"`
	Runtime           string `json:"runtime,omitempty"`
	Health            string `json:"health,omitempty"`
	HealthReason      string `json:"health_reason,omitempty"`
	ErrorTag          string `json:"error_tag,omitempty"`
	RuntimeSessionID  string `json:"runtime_session_id,omitempty"`
	PID               int    `json:"pid"`
	PIDAlive          bool   `json:"pid_alive"`
	PluginAgeS        int64  `json:"plugin_age_s"`
	StalePluginS      int64  `json:"stale_plugin_s"`
	ModelAgeS         int64  `json:"model_age_s"`
	RuntimeAgeS       int64  `json:"runtime_age_s"`
	HealthAgeS        int64  `json:"health_age_s"`
	PluginHeartbeatMS int    `json:"plugin_heartbeat_ms,omitempty"`
}

// readHealth reads status/<agent>.json and returns it, or nil on any error.
func (b *Bus) readHealth(agent string) *StatusRecord {
	rec, ok := parseStatusFile(b.sfile(agent))
	if !ok {
		return nil
	}
	return &rec
}

// DoHealth implements `comms health` — the liveness watchdog.
func (b *Bus) DoHealth(args []string) error {
	jsonOut := false
	loop := false
	interval := 30
	stalePlugin := defaultStalePlugin
	staleModel := defaultStaleModel

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--json":
			jsonOut = true
		case "--loop":
			loop = true
			if i+1 < len(args) {
				if n, err := strconv.Atoi(args[i+1]); err == nil {
					interval = n
					i++
				}
			}
		case "--once":
			loop = false
		case "--stale-plugin":
			if i+1 < len(args) {
				if n, err := strconv.Atoi(args[i+1]); err == nil {
					stalePlugin = n
					i++
				}
			}
		case "--stale-model":
			if i+1 < len(args) {
				if n, err := strconv.Atoi(args[i+1]); err == nil {
					staleModel = n
					i++
				}
			}
		case "-h", "--help":
			fmt.Print(healthUsage)
			return nil
		default:
			return usageErr("comms health: unknown option: " + args[i])
		}
	}

	for {
		if loop {
			fmt.Print("\033[2J\033[H")
			fmt.Printf("=== Fleet Health Watchdog — %s ===\n\n", time.Now().Format("2006-01-02 15:04:05"))
		}
		unhealthy, err := b.checkHealth(jsonOut, stalePlugin, staleModel)
		if err != nil {
			return err
		}
		if loop {
			fmt.Printf("\nNext check in %ds — Ctrl-C to stop\n", interval)
			time.Sleep(time.Duration(interval) * time.Second)
			continue
		}
		if unhealthy > 0 {
			return fmt.Errorf("fleet-health: %d ship(s) unhealthy", unhealthy)
		}
		return nil
	}
}

func (b *Bus) checkHealth(jsonOut bool, stalePlugin, staleModel int) (int, error) {
	entries, err := os.ReadDir(b.StatusDir)
	if err != nil {
		if jsonOut {
			return 0, printJSON([]healthEntry{})
		}
		fmt.Println("No status directory found — no ships reporting.")
		return 0, nil
	}

	out := make([]healthEntry, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		ship := e.Name()[:len(e.Name())-len(".json")]
		data, rerr := os.ReadFile(filepath.Join(b.StatusDir, e.Name()))
		if rerr != nil {
			continue
		}
		var rec StatusRecord
		if jerr := json.Unmarshal(data, &rec); jerr != nil {
			continue
		}
		out = append(out, EvalHealth(ship, rec, stalePlugin, staleModel))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Ship < out[j].Ship })

	unhealthy := 0
	for _, h := range out {
		if h.State == "BLOCKED" || h.State == "DEAD" || h.State == "STALE" ||
			h.State == "OFFLINE" || h.State == "ERROR" || h.State == "DEGRADED" || h.State == "STUCK" {
			unhealthy++
		}
	}

	if jsonOut {
		return unhealthy, printJSON(out)
	}

	if len(out) == 0 {
		fmt.Println("No ships reporting.")
		return 0, nil
	}
	fmt.Printf("%-15s %-9s %-9s %-9s %-9s %-8s %-10s %-10s\n",
		"SHIP", "STATE", "TASK", "RUNTIME", "HEALTH", "PID", "PLUGIN_AGE", "MODEL_AGE")
	for _, h := range out {
		fmt.Printf("%-15s %-9s %-9s %-9s %-9s %-8d %-10ds %-10ds\n",
			h.Ship, h.State, h.TaskState, h.Runtime, h.Health, h.PID, h.PluginAgeS, h.ModelAgeS)
	}
	return unhealthy, nil
}

func EvalHealth(ship string, rec StatusRecord, stalePlugin, staleModel int) healthEntry {
	now := time.Now()
	var pluginAge, modelAge, runtimeAge, healthAge int64
	if rec.PluginLastRun != "" {
		if t, err := time.Parse(time.RFC3339Nano, rec.PluginLastRun); err == nil {
			pluginAge = now.Unix() - t.Unix()
			if pluginAge < 0 {
				pluginAge = 0
			}
		}
	}
	if rec.ModelLastAction != "" {
		if t, err := time.Parse(time.RFC3339Nano, rec.ModelLastAction); err == nil {
			modelAge = now.Unix() - t.Unix()
			if modelAge < 0 {
				modelAge = 0
			}
		}
	}
	if rec.RuntimeUpdated != "" {
		if t, err := time.Parse(time.RFC3339Nano, rec.RuntimeUpdated); err == nil {
			runtimeAge = now.Unix() - t.Unix()
			if runtimeAge < 0 {
				runtimeAge = 0
			}
		}
	}
	if rec.HealthUpdated != "" {
		if t, err := time.Parse(time.RFC3339Nano, rec.HealthUpdated); err == nil {
			healthAge = now.Unix() - t.Unix()
			if healthAge < 0 {
				healthAge = 0
			}
		}
	}

	stalePluginLimit := int64(stalePlugin)
	if stalePluginLimit <= 0 {
		if rec.PluginHeartbeatMS > 0 {
			heartbeatSeconds := int64((rec.PluginHeartbeatMS + 999) / 1000)
			stalePluginLimit = heartbeatSeconds*2 + 30
		} else {
			stalePluginLimit = 600
		}
	}

	pidAlive := true
	if rec.PID > 0 {
		pidAlive = pidAliveCheck(rec.PID)
	}

	effective := rec.State
	switch {
	case rec.State == "offline":
		effective = "STOPPED" // explicit owner shutdown
	case rec.PID > 0 && !pidAlive:
		effective = "OFFLINE"
	case rec.PluginLastRun != "" && pluginAge > stalePluginLimit:
		effective = "OFFLINE"
	case rec.Health == "error":
		effective = "ERROR"
	case rec.Health == "degraded":
		effective = "DEGRADED"
	case rec.Runtime == "working" && modelAge > int64(staleModel):
		effective = "STUCK"
	case rec.Runtime != "":
		effective = strings.ToUpper(rec.Runtime)
	case rec.State == "blocked":
		effective = "BLOCKED"
	case modelAge > int64(staleModel) && rec.State == "working":
		effective = "STUCK"
	}
	if effective == "" {
		effective = "unknown"
	}

	return healthEntry{
		Ship:              ship,
		State:             effective,
		RawState:          rec.State,
		TaskState:         rec.State,
		Runtime:           rec.Runtime,
		Health:            rec.Health,
		HealthReason:      rec.HealthReason,
		ErrorTag:          rec.ErrorTag,
		RuntimeSessionID:  rec.RuntimeSessionID,
		PID:               rec.PID,
		PIDAlive:          pidAlive,
		PluginAgeS:        pluginAge,
		StalePluginS:      stalePluginLimit,
		ModelAgeS:         modelAge,
		RuntimeAgeS:       runtimeAge,
		HealthAgeS:        healthAge,
		PluginHeartbeatMS: rec.PluginHeartbeatMS,
	}
}

// pidAliveCheck reports whether a process with the given pid exists and is
// signalable by us. EPERM (owned by another uid) still means it is alive.
func pidAliveCheck(pid int) bool {
	err := syscall.Kill(pid, 0)
	if err == nil {
		return true
	}
	if errors.Is(err, syscall.EPERM) {
		return true
	}
	return false
}

func pluginTs(h healthEntry) string {
	if h.PluginAgeS == 0 && h.RawState == "" {
		return "-"
	}
	return fmt.Sprintf("%ds ago", h.PluginAgeS)
}

func modelTs(h healthEntry) string {
	if h.ModelAgeS == 0 && h.RawState == "" {
		return "-"
	}
	return fmt.Sprintf("%ds ago", h.ModelAgeS)
}

// DoHealthUpdate implements `comms health update` — a structured
// write to status/<ship>.json. Only supplied flags are merged into the
// existing file (read-modify-write).
const healthUpdateUsage = `comms health update [flags]

Write/merge per-ship status data to status/<ship>.json — the single source
of truth. Only supplied flags are merged into the existing file
(read-modify-write).

Plugin flags (used by opencode plugin):
  --state <s>        idle|working|blocked
  --plugin-ts <iso>  ISO timestamp of last plugin heartbeat
  --heartbeat-ms <n> configured plugin heartbeat interval in milliseconds
  --model-ts <iso>   ISO timestamp of last model action
  --pid <n>          process ID
  --model <m>        model identifier (e.g. "openai/gpt-4o")
  --server <s>       provider/server name
  --error-tag <t>    error classification tag

Task status flags (used by Go CLI / status report):
  --task <s>         current task description
  --progress <n>     0-100 (-1 or omit to leave unchanged)
  --blocker <s>      what blocks progress
  --eta <s>          estimated completion (free-form)
  --branch <s>       PR/branch the ship is on
  --note <s>         human-readable note
  --unattached      mark as unattached (working without task/note)

Launch metadata (set on ship startup):
  --launch-type <s>  terminal|background|auto
  --parent <s>       parent ship
  --provider <s>     model provider
  --updated <s>      override timestamp

  --delete           remove the status file entirely
  --reset            remove existing file first, then write (atomic delete+write)
  --touch            after writing, refresh heartbeat timestamp
  -h, --help         this help
`

func (b *Bus) DoHealthUpdate(args []string) error {
	var state, pluginTS, modelTS, model, server, errorTag, pluginVersion string
	heartbeatMS := 0
	var task, blocker, eta, branch, note, launchType, parent, provider, updated string
	progress := -1
	pid := 0
	reset := false
	touch := false
	deleteFile := false

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--state":
			if i+1 < len(args) {
				state = args[i+1]
				i++
			}
		case "--plugin-ts":
			if i+1 < len(args) {
				pluginTS = args[i+1]
				i++
			}
		case "--model-ts":
			if i+1 < len(args) {
				modelTS = args[i+1]
				i++
			}
		case "--pid":
			if i+1 < len(args) {
				pid, _ = strconv.Atoi(args[i+1])
				i++
			}
		case "--model":
			if i+1 < len(args) {
				model = args[i+1]
				i++
			}
		case "--server":
			if i+1 < len(args) {
				server = args[i+1]
				i++
			}
		case "--error-tag":
			if i+1 < len(args) {
				errorTag = args[i+1]
				i++
			}
		case "--plugin-version":
			if i+1 < len(args) {
				pluginVersion = args[i+1]
				i++
			}
		case "--heartbeat-ms":
			if i+1 < len(args) {
				heartbeatMS, _ = strconv.Atoi(args[i+1])
				i++
			}
		case "--task":
			if i+1 < len(args) {
				task = args[i+1]
				i++
			}
		case "--progress":
			if i+1 < len(args) {
				fmt.Sscanf(args[i+1], "%d", &progress)
				i++
			}
		case "--blocker":
			if i+1 < len(args) {
				blocker = args[i+1]
				i++
			}
		case "--eta":
			if i+1 < len(args) {
				eta = args[i+1]
				i++
			}
		case "--branch":
			if i+1 < len(args) {
				branch = args[i+1]
				i++
			}
		case "--note":
			if i+1 < len(args) {
				note = args[i+1]
				i++
			}
		case "--unattached":
			// Flag: mark this status as unattached (working without task/note)
			// This will be auto-set if state is working/building and no task/note provided
		case "--launch-type":
			if i+1 < len(args) {
				launchType = args[i+1]
				i++
			}
		case "--parent":
			if i+1 < len(args) {
				parent = args[i+1]
				i++
			}
		case "--provider":
			if i+1 < len(args) {
				provider = args[i+1]
				i++
			}
		case "--updated":
			if i+1 < len(args) {
				updated = args[i+1]
				i++
			}
		case "--delete":
			deleteFile = true
		case "--reset":
			reset = true
		case "--touch":
			touch = true
		case "-h", "--help":
			fmt.Print(healthUpdateUsage)
			return nil
		default:
			return usageErr("comms health update: unknown option: " + args[i])
		}
	}

	lock, err := b.lockBus()
	if err != nil {
		return err
	}
	lockHeld := true
	defer func() {
		if lockHeld {
			_ = lock.Close()
		}
	}()

	fpath := b.sfile(b.ShipID)

	if deleteFile {
		os.Remove(fpath)
		return nil
	}

	if reset {
		os.Remove(fpath)
	}

	// Read existing file (if any) to merge with.
	var prev StatusRecord
	if data, err := os.ReadFile(fpath); err == nil {
		_ = json.Unmarshal(data, &prev)
	}

	nowTs := time.Now().Format(time.RFC3339Nano)
	rec := StatusRecord{
		Epoch:   prev.Epoch,
		ISO:     prev.ISO,
		Agent:   coalesce(prev.Agent, b.ShipID),
		Project: prev.Project,
		State:   coalesce(state, prev.State, "idle"),
		// An explicitly transmitted PID (the opencode plugin passes its own
		// long-lived process PID) is authoritative; otherwise a live recorded
		// PID is preserved — never the PID of this short-lived helper process.
		PID:             coalesceInt(pid, statusPIDForWrite(prev.PID)),
		Handle:          prev.Handle,
		Note:            coalesce(note, prev.Note),
		PluginLastRun:   coalesce(pluginTS, prev.PluginLastRun),
		ModelLastAction: coalesce(modelTS, prev.ModelLastAction),
		Model:           coalesce(model, prev.Model),
		Server:          coalesce(server, prev.Server),
		ErrorTag:        coalesce(errorTag, prev.ErrorTag),
		Task:            coalesce(task, prev.Task),
		Progress:        coalesceProgress(progress, prev.Progress),
		Blocker:         coalesce(blocker, prev.Blocker),
		ETA:             coalesce(eta, prev.ETA),
		Branch:          coalesce(branch, prev.Branch),
		LaunchType:      coalesce(launchType, prev.LaunchType),
		Parent:          coalesce(parent, prev.Parent),
		Provider:        coalesce(provider, prev.Provider),
		PluginVersion:   coalesce(pluginVersion, prev.PluginVersion),
		PluginHeartbeatMS: coalesceInt(heartbeatMS, prev.PluginHeartbeatMS),
		Runtime:         prev.Runtime,
		Health:          prev.Health,
		RuntimeUpdated:  prev.RuntimeUpdated,
		HealthUpdated:   prev.HealthUpdated,
		RuntimeEventID:  prev.RuntimeEventID,
		HealthEventID:   prev.HealthEventID,
		RuntimeSessionID: prev.RuntimeSessionID,
		HealthReason:    prev.HealthReason,
		Updated:         coalesce(updated, prev.Updated, nowTs),
		// Auto-set Unattached: working/building without task or note
		Unattached: (state == "working" || state == "building") && task == "" && note == "",
	}

	// Ensure timestamps are set for new records.
	if rec.Epoch == 0 {
		rec.Epoch = now()
	}
	if rec.ISO == "" {
		rec.ISO = isots()
	}

	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(b.StatusDir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(fpath, append(data, '\n'), 0o644); err != nil {
		return err
	}

	// Release the read-modify-write lock before DoTouch, which acquires the
	// same bus lock to refresh the heartbeat timestamp.
	_ = lock.Close()
	lockHeld = false

	// --touch: refresh heartbeat timestamp so the board sees a live ship.
	if touch {
		_ = b.DoTouch()
	}
	return nil
}

func coalesce(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func coalesceInt(vals ...int) int {
	for _, v := range vals {
		if v != 0 {
			return v
		}
	}
	return 0
}

// coalesceProgress picks the first non-negative value (-1 means "unspecified").
func coalesceProgress(vals ...int) int {
	for _, v := range vals {
		if v >= 0 {
			return v
		}
	}
	return 0
}
