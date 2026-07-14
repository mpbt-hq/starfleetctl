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
	"strings"
	"syscall"
	"time"
)

const (
	defaultStalePlugin = 120 // plugin_last_run older than this → STALE
	defaultStaleModel  = 300 // model_last_action older than this (while working) → STUCK
)

const healthUsage = `agent-bus health [flags]

Fleet liveness watchdog — reads the per-ship health files written by the
opencode plugin (starfleet-dispatch.ts) at _WORK_/agent-bus/health/<SHIP>.json
and reports unresponsive ships. This is the Go port of scripts/fleet-health,
so the two read the same data and classify ships identically.

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

// healthEntry mirrors one ship's evaluated health, shaped like the JSON the
// bash fleet-health printed (field order/names kept compatible).
type healthEntry struct {
	Ship       string `json:"ship"`
	State      string `json:"state"`
	RawState   string `json:"raw_state"`
	PID        int    `json:"pid"`
	PIDAlive   bool   `json:"pid_alive"`
	PluginAgeS int64  `json:"plugin_age_s"`
	ModelAgeS  int64  `json:"model_age_s"`
}

type rawHealth struct {
	PluginLastRun   string `json:"plugin_last_run"`
	ModelLastAction string `json:"model_last_action"`
	State           string `json:"state"`
	PID             int    `json:"pid"`
}

func (b *Bus) healthDir() string {
	return filepath.Join(b.BusDir, "health")
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
	dir := b.healthDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if jsonOut {
			return 0, printJSON([]healthEntry{})
		}
		fmt.Println("No health directory found — no ships reporting.")
		return 0, nil
	}

	out := make([]healthEntry, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		ship := strings.TrimSuffix(e.Name(), ".json")
		data, rerr := os.ReadFile(filepath.Join(dir, e.Name()))
		if rerr != nil {
			continue
		}
		var rh rawHealth
		if jerr := json.Unmarshal(data, &rh); jerr != nil {
			continue
		}
		out = append(out, evalHealth(ship, rh, stalePlugin, staleModel))
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

func evalHealth(ship string, rh rawHealth, stalePlugin, staleModel int) healthEntry {
	now := time.Now()
	var pluginAge, modelAge int64
	if rh.PluginLastRun != "" {
		if t, err := time.Parse(time.RFC3339Nano, rh.PluginLastRun); err == nil {
			pluginAge = now.Unix() - t.Unix()
			if pluginAge < 0 {
				pluginAge = 0
			}
		}
	}
	if rh.ModelLastAction != "" {
		if t, err := time.Parse(time.RFC3339Nano, rh.ModelLastAction); err == nil {
			modelAge = now.Unix() - t.Unix()
			if modelAge < 0 {
				modelAge = 0
			}
		}
	}

	pidAlive := true
	if rh.PID > 0 {
		pidAlive = pidAliveCheck(rh.PID)
	}

	effective := rh.State
	switch {
	case rh.State == "blocked":
		effective = "BLOCKED"
	case rh.PID > 0 && !pidAlive:
		effective = "DEAD"
	case pluginAge > int64(stalePlugin):
		effective = "STALE"
	case modelAge > int64(staleModel) && rh.State == "working":
		effective = "STUCK"
	}
	if effective == "" {
		effective = "unknown"
	}

	return healthEntry{
		Ship:       ship,
		State:      effective,
		RawState:   rh.State,
		PID:        rh.PID,
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
