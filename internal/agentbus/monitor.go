// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
//
// Go ports of the agent-bus "ecosystem" polling loops — scripts/
// agent-bus-monitor-loop, agent-bus-fleet-watch, and agent-bus-watch. All
// three are long-running processes reading the same _WORK_/agent-bus/ files
// as the rest of this package, so they're built directly on the existing
// Bus helpers rather than shelling out.
//
// KNOWN ISSUE, DO NOT wire DoMonitorLoop/DoFleetWatch into the Claude Code
// Monitor tool (i.e. do not point a `Monitor{command: "... agent-bus
// monitor-loop"}` arm at this) — found 2026-07-06 (Farragut, directive
// m0047): both reliably detect a BACKLOG match (a message that already
// exists when the process starts, e.g. under `Monitor`'s own first-pass
// scan) but reproducibly FAIL to detect a message that arrives WHILE the
// process is already running, specifically when spawned via the Monitor
// tool — despite the exact same binary/logic working correctly (a) via
// plain `&`-backgrounded bash, and (b) via three separately-written minimal
// Go reproductions of the same shape under Monitor (a bare sleep-loop; a
// directory-listing poll loop; that same poll loop plus an open/held
// append-mode file handle to a sibling path, mirroring this file's own
// seen-file handling). None of the three isolate the cause — directory
// cache staleness, held-fd interference, and workspace-root resolution were
// all specifically tested and ruled out. Root cause NOT understood.
// DoWatch is a different execution model (setsid-detached background
// daemon, not Monitor-tool-managed) and was NOT tested against this same
// failure mode — untested, not confirmed either safe or unsafe.
// scripts/agent-bus-monitor-loop and scripts/agent-bus-fleet-watch (bash)
// remain the only Monitor-tool-armed implementation until this is
// understood; see DASHBOARD.md's starfleetctl row for the full writeup.
package agentbus

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
	if !b.AgentIDSet {
		return fmt.Errorf("agent-bus-monitor-loop: $AGENT_ID is not set")
	}
	seenDir := filepath.Join(b.BusDir, "monitor-seen")
	if err := os.MkdirAll(seenDir, 0o755); err != nil {
		return err
	}
	seenFile := filepath.Join(seenDir, b.AgentID)

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

	for {
		for _, m := range b.allMsgRecords() {
			if m.Target != "all" && m.Target != b.AgentID {
				continue
			}
			if b.acked(m.ID, b.AgentID) || seen[m.ID] {
				continue
			}
			fmt.Printf("[%s] from %s: %s\n", m.ID, m.From, m.Text)
			seen[m.ID] = true
			fmt.Fprintln(f, m.ID)
		}
		time.Sleep(pollInterval)
	}
}

// DoFleetWatch implements scripts/agent-bus-fleet-watch: watches
// _WORK_/agent-bus/status/ for ships joining or restarting (heartbeat
// epoch change), seeded from the board at arm time so only changes AFTER
// arming are reported.
func (b *Bus) DoFleetWatch() error {
	lastEpoch := map[string]int64{}
	for _, r := range b.allStatusRecords() {
		lastEpoch[r.Agent] = r.Epoch
	}

	for {
		for _, r := range b.allStatusRecords() {
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
	agentSafe := fsafe(b.AgentID)
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
			if m.Target != "all" && m.Target != b.AgentID {
				continue
			}
			if b.acked(m.ID, b.AgentID) || seen[m.ID] {
				continue
			}
			notify(logFile, popupOnceDir, b.AgentID, m)
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
