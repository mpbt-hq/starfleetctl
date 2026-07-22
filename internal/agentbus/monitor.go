// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
//
// DoMonitorLoop CLEARED for Monitor-tool use as of 2026-07-07 (Farragut,
// m0087/m0120): originally found reproducibly BROKEN under the Claude Code
// Monitor tool 2026-07-06 (Farragut, directive m0047) — reliably detected a
// BACKLOG match but reproducibly FAILED to detect a message arriving WHILE
// already running under Monitor specifically. Root cause was never
// isolated (three independent minimal repros under Monitor all worked
// fine, ruling out directory-cache staleness, held-fd interference, and
// workspace-root resolution). Two independent re-repro attempts the same
// day (Constellation 7/7, Farragut 4/4) could no longer reproduce it at
// all, and a subsequent extended real-production vorcheck (Farragut,
// 2026-07-07: the actual DoMonitorLoop binary armed live via Monitor
// against the real bus, disposable identity, ~16h continuous runtime) saw
// 5/5 live `all`-broadcasts detected correctly with zero misses — whatever
// the original bug was, it is gone. scripts/agent-bus-monitor-hint arms
// `.starfleet-ai/bin/starfleetctl agent-bus monitor-loop` for new/restarted sessions,
// and scripts/agent-bus-monitor-loop is now a thin bash wrapper exec'ing
// that same Go backend (no separate bash fallback body remains).
//
// DoFleetWatch CLEARED too, 2026-07-07 (Farragut, m0138(1)): its own
// Monitor-tool vorcheck armed both the bash original and this Go binary in
// parallel against the real board and saw 7/7 live status-change events
// (real fleet activity plus a disposable synthetic test ship) detected
// identically by both, including correct "New ship online" vs "Ship
// update" labeling. scripts/agent-bus-monitor-hint's Enterprise branch now
// arms `.starfleet-ai/bin/starfleetctl agent-bus fleet-watch`; scripts/agent-bus-
// fleet-watch (bash) remains in place, untouched, as a fallback.
//
// DoWatch is a different execution model (setsid-detached background
// daemon, not Monitor-tool-managed) and was never tested against the
// original failure mode either way — untested, not confirmed safe or
// unsafe. See DASHBOARD.md's starfleetctl row / the m0047 topic file for
// the full writeup.
package agentbus

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const pollInterval = 2 * time.Second

// DoMonitorLoop implements scripts/agent-bus-monitor-loop: the Monitor-tool
// command that watches this session's own inbox and prints one line per
// new/unacked directive. Runs forever (Monitor tool kills the process to
// stop it) — same shape as the bash `while true; do …; sleep 2; done`.
func (b *Bus) DoMonitorLoop() error {
	if !b.ShipIDSet {
		return fmt.Errorf("agent-bus-monitor-loop: $STARFLEET_SHIP_ID is not set")
	}
	seenDir := filepath.Join(b.BusDir, "monitor-seen")
	if err := os.MkdirAll(seenDir, 0o755); err != nil {
		return err
	}
	seenFile := filepath.Join(seenDir, b.ShipID)

	seen := map[string]bool{}
	if data, err := os.ReadFile(seenFile); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if line != "" {
				seen[line] = true
			}
		}
	}
	f, err := os.OpenFile(seenFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()

	heartbeatInterval := int64(300) // HEARTBEAT_INTERVAL, same default as the bash original
	if v := os.Getenv("HEARTBEAT_INTERVAL"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			heartbeatInterval = n
		}
	}
	// Skip an immediate touch — SessionStart's hook just posted a fresh
	// heartbeat moments ago (matches the bash original's `last_heartbeat` seed).
	lastHeartbeat := now()

	for {
		for _, m := range b.allMsgRecords() {
			if m.Target != "all" && m.Target != b.ShipID {
				continue
			}
			if b.acked(m.ID, b.ShipID) || seen[m.ID] {
				continue
			}
			fmt.Printf("[%s] from %s: %s\n", m.ID, m.From, m.Text)
			seen[m.ID] = true
			fmt.Fprintln(f, m.ID)
		}
		// Periodic heartbeat refresh: a ship deep in a long task that never
		// calls `agent-bus status` itself would otherwise fall out of
		// $STARFLEET_BUS_TTL (15m default) and read as dead/pruned on the board despite
		// the session being alive. `|| true`: a transient touch failure (e.g.
		// lock contention) must not kill this persistent Monitor-tool loop.
		if now()-lastHeartbeat >= heartbeatInterval {
			_ = b.DoTouch()
			lastHeartbeat = now()
		}
		time.Sleep(pollInterval)
	}
}

// watches bus status for ships joining or restarting (heartbeat
// epoch change), seeded from the board at arm time so only changes AFTER
// arming are reported.
func (b *Bus) DoFleetWatch() error {
	lastEpoch := map[string]int64{}
	for _, r := range b.AllStatusRecords() {
		lastEpoch[r.Agent] = r.Epoch
	}

	for {
		for _, r := range b.AllStatusRecords() {
			prev, known := lastEpoch[r.Agent]
			if known && prev == r.Epoch {
				continue
			}
			proj := r.Project
			if proj == "" {
				proj = "—"
			}
			noteSuffix := ""
			if r.Note != "" {
				noteSuffix = fmt.Sprintf(", note: %s", r.Note)
			}
			if !known {
				fmt.Printf("New ship online: %s (project=%s, state=%s%s)\n", r.Agent, proj, r.State, noteSuffix)
			} else {
				fmt.Printf("Ship update: %s (project=%s, state=%s%s)\n", r.Agent, proj, r.State, noteSuffix)
			}
			lastEpoch[r.Agent] = r.Epoch
		}
		time.Sleep(pollInterval)
	}
}

// DoWatch implements scripts/agent-bus-watch: a local, LLM-free desktop
// notifier for new directives targeting this agent (or a broadcast). Single
// instance per agent id (PID-file guard, matching bash); --stop kills it.
func (b *Bus) DoWatch(intervalArg string, stop bool) error {
	notifyDir := filepath.Join(b.BusDir, "notify")
	popupOnceDir := filepath.Join(notifyDir, ".popup-once")
	if err := os.MkdirAll(popupOnceDir, 0o755); err != nil {
		return err
	}
	agentSafe := fsafe(b.ShipID)
	pidFile := filepath.Join(notifyDir, ".watch-"+agentSafe+".pid")
	seenFile := filepath.Join(notifyDir, ".seen-"+agentSafe)
	logFile := filepath.Join(notifyDir, agentSafe+".log")

	if stop {
		return stopWatch(pidFile)
	}

	interval := 15 * time.Second // bash default when no arg given
	if intervalArg != "" {
		if secs, err := parseSeconds(intervalArg); err == nil {
			interval = secs
		}
	}

	if alreadyRunning(pidFile) {
		return nil
	}
	if err := os.WriteFile(pidFile, []byte(fmt.Sprintf("%d", os.Getpid())), 0o644); err != nil {
		return err
	}
	defer os.Remove(pidFile)

	seen := map[string]bool{}
	if data, err := os.ReadFile(seenFile); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if line != "" {
				seen[line] = true
			}
		}
	}
	seenF, err := os.OpenFile(seenFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer seenF.Close()

	for {
		for _, m := range b.allMsgRecords() {
			if m.Target != "all" && m.Target != b.ShipID {
				continue
			}
			if b.acked(m.ID, b.ShipID) || seen[m.ID] {
				continue
			}
			notify(logFile, popupOnceDir, b.ShipID, m)
			seen[m.ID] = true
			fmt.Fprintln(seenF, m.ID)
		}
		reapPopupOnce(popupOnceDir, b.MsgDir)
		time.Sleep(interval)
	}
}

func stopWatch(pidFile string) error {
	data, err := os.ReadFile(pidFile)
	if err != nil {
		return nil
	}
	var pid int
	fmt.Sscanf(strings.TrimSpace(string(data)), "%d", &pid)
	if pid > 0 {
		if proc, err := os.FindProcess(pid); err == nil {
			_ = proc.Signal(syscall.SIGTERM) // plain `kill`, matching the bash original
		}
	}
	return os.Remove(pidFile)
}

// alreadyRunning mirrors bash's `kill -0 "$(cat "$PIDFILE")"` liveness check.
func alreadyRunning(pidFile string) bool {
	data, err := os.ReadFile(pidFile)
	if err != nil {
		return false
	}
	var pid int
	fmt.Sscanf(strings.TrimSpace(string(data)), "%d", &pid)
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

func notify(logFile, popupOnceDir, agentID string, m msgRecord) {
	ts := time.Now().Format("2006-01-02 15:04:05")
	f, err := os.OpenFile(logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err == nil {
		fmt.Fprintf(f, "%s\t%s\tfrom %s\t%s\n", ts, m.ID, m.From, m.Text)
		f.Close()
	}
	title := fmt.Sprintf("agent-bus: directive for %s", agentID)
	if m.Target == "all" {
		title = "agent-bus: broadcast"
		// Atomic "first ship wins" gate, same as bash's mkdir race guard.
		if err := os.Mkdir(filepath.Join(popupOnceDir, m.ID), 0o755); err != nil {
			return
		}
	}
	if _, err := exec.LookPath("notify-send"); err == nil {
		_ = exec.Command("notify-send", "-u", "normal", title, fmt.Sprintf("[%s] %s: %s", m.ID, m.From, m.Text)).Run()
	}
}

func reapPopupOnce(popupOnceDir, msgDir string) {
	entries, err := os.ReadDir(popupOnceDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if _, err := os.Stat(filepath.Join(msgDir, e.Name()+".tsv")); err != nil {
			os.Remove(filepath.Join(popupOnceDir, e.Name()))
		}
	}
}

func parseSeconds(s string) (time.Duration, error) {
	var n int
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil {
		return 0, err
	}
	return time.Duration(n) * time.Second, nil
}
