// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package agentbus

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"syscall"
	"time"
)

const (
	defaultStalePlugin = 120 // plugin_last_run older than this → STALE
	defaultStaleModel  = 300 // model_last_action older than this (while working) → STUCK
)

const healthUsage = `agent-bus health [flags]

Fleet liveness watchdog — reads the per-ship status files at
_WORK_/agent-bus/status/<SHIP>.json and reports unresponsive ships.

Flags:
  --json              output JSON (array of ship objects) instead of a table
  --loop [SEC]        run continuously; default interval 30s (--once is default)
  --stale-plugin N    plugin_last_run age (s) above which a ship is STALE (120)
  --stale-model N     model_last_action age (s) above which a working ship is
                      STUCK (300)
  -h, --help          this help

Effective state (one of): healthy raw state, or BLOCKED (plugin says so),
DEAD (pid no longer alive), STALE (plugin silent), STUCK (model silent while
working). Exit code 1 if any ship is unhealthy (non-loop mode only), 0 if all
healthy — so it can drive a cron job / CI gate.
`

// healthEntry mirrors one ship's evaluated health for the watchdog output.
type healthEntry struct {
	Ship       string `json:"ship"`
	State      string `json:"state"`
	RawState   string `json:"raw_state"`
	PID        int    `json:"pid"`
	PIDAlive   bool   `json:"pid_alive"`
	PluginAgeS int64  `json:"plugin_age_s"`
	ModelAgeS  int64  `json:"model_age_s"`
}

// readHealth reads status/<agent>.json and returns it, or nil on any error.
func (b *Bus) readHealth(agent string) *StatusRecord {
	rec, ok := parseStatusFile(b.sfile(agent))
	if !ok {
		return nil
	}
	return &rec
}

// DoHealth implements `agent-bus health` — the liveness watchdog.
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
			return usageErr("agent-bus health: unknown option: " + args[i])
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
		out = append(out, evalHealth(ship, rec, stalePlugin, staleModel))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Ship < out[j].Ship })

	unhealthy := 0
	for _, h := range out {
		if h.State == "BLOCKED" || h.State == "DEAD" || h.State == "STALE" || h.State == "STUCK" {
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
	fmt.Printf("%-15s %-9s %-8s %-26s %-26s %s\n",
		"SHIP", "STATE", "PID", "PLUGIN_LAST_RUN", "MODEL_LAST_ACTION", "AGE")
	for _, h := range out {
		fmt.Printf("%-15s %-9s %-8d %-26s %-26s %ds\n",
			h.Ship, h.State, h.PID, pluginTs(h), modelTs(h), h.PluginAgeS)
	}
	return unhealthy, nil
}

func evalHealth(ship string, rec StatusRecord, stalePlugin, staleModel int) healthEntry {
	now := time.Now()
	var pluginAge, modelAge int64
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

	pidAlive := true
	if rec.PID > 0 {
		pidAlive = pidAliveCheck(rec.PID)
	}

	effective := rec.State
	switch {
	case rec.State == "offline":
		effective = "OFFLINE" // intentional shutdown, not a problem
	case rec.State == "blocked":
		effective = "BLOCKED"
	case rec.PID > 0 && !pidAlive:
		effective = "DEAD"
	case pluginAge > int64(stalePlugin):
		effective = "STALE"
	case modelAge > int64(staleModel) && rec.State == "working":
		effective = "STUCK"
	}
	if effective == "" {
		effective = "unknown"
	}

	return healthEntry{
		Ship:       ship,
		State:      effective,
		RawState:   rec.State,
		PID:        rec.PID,
		PIDAlive:   pidAlive,
		PluginAgeS: pluginAge,
		ModelAgeS:  modelAge,
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

// DoHealthUpdate implements `agent-bus health update` — a structured
// write to status/<ship>.json. Only supplied flags are merged into the
// existing file (read-modify-write).
const healthUpdateUsage = `agent-bus health update [flags]

Write/merge per-ship status data to status/<ship>.json — the single source
of truth. Only supplied flags are merged into the existing file
(read-modify-write).

Plugin flags (used by opencode plugin):
  --state <s>        idle|working|blocked
  --plugin-ts <iso>  ISO timestamp of last plugin run
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
	var state, pluginTS, modelTS, model, server, errorTag string
	var task, blocker, eta, branch, note, launchType, parent, provider, updated string
	progress := -1
	pid := 0
	reset := false
	touch := false
	deleteFile := false

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--state":
			if i+1 < len(args) { state = args[i+1]; i++ }
		case "--plugin-ts":
			if i+1 < len(args) { pluginTS = args[i+1]; i++ }
		case "--model-ts":
			if i+1 < len(args) { modelTS = args[i+1]; i++ }
		case "--pid":
			if i+1 < len(args) { pid, _ = strconv.Atoi(args[i+1]); i++ }
		case "--model":
			if i+1 < len(args) { model = args[i+1]; i++ }
		case "--server":
			if i+1 < len(args) { server = args[i+1]; i++ }
		case "--error-tag":
			if i+1 < len(args) { errorTag = args[i+1]; i++ }
		case "--task":
			if i+1 < len(args) { task = args[i+1]; i++ }
		case "--progress":
			if i+1 < len(args) { fmt.Sscanf(args[i+1], "%d", &progress); i++ }
		case "--blocker":
			if i+1 < len(args) { blocker = args[i+1]; i++ }
		case "--eta":
			if i+1 < len(args) { eta = args[i+1]; i++ }
		case "--branch":
			if i+1 < len(args) { branch = args[i+1]; i++ }
		case "--note":
			if i+1 < len(args) { note = args[i+1]; i++ }
		case "--launch-type":
			if i+1 < len(args) { launchType = args[i+1]; i++ }
		case "--parent":
			if i+1 < len(args) { parent = args[i+1]; i++ }
		case "--provider":
			if i+1 < len(args) { provider = args[i+1]; i++ }
		case "--updated":
			if i+1 < len(args) { updated = args[i+1]; i++ }
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
			return usageErr("agent-bus health update: unknown option: " + args[i])
		}
	}

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
		Epoch:           prev.Epoch,
		ISO:             prev.ISO,
		Agent:           coalesce(prev.Agent, b.ShipID),
		Project:         prev.Project,
		State:           coalesce(state, prev.State, "idle"),
		PID:             coalesceInt(pid, prev.PID, os.Getpid()),
		Handle:          prev.Handle,
		Note:            coalesce(note, prev.Note),
		PluginLastRun:   coalesce(pluginTS, prev.PluginLastRun, nowTs),
		ModelLastAction: coalesce(modelTS, prev.ModelLastAction, nowTs),
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
		Updated:         coalesce(updated, prev.Updated, nowTs),
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
