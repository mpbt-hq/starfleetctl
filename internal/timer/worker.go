// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
//
// Timer worker: background daemon that polls timer directories every 2s,
// resolves fleet targets at fire time, and sends agent-bus directives.
//
// Signals:
//
//	SIGHUP  — immediate poll (re-read timers, useful after timer set)
//	SIGTERM — graceful shutdown
//	SIGINT  — graceful shutdown
//
// Singleton: PID-file guarded (_WORK_/agent-bus/timer-worker.pid).
// Logs to _WORK_/agent-bus/logs/timer-worker.log.
package timer

import (
	"fmt"
	"math/rand"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/metux/starfleetctl/internal/agentbus"
	"github.com/robfig/cron/v3"
)

const (
	workerPollInterval = 2 * time.Second
	workerPidFile      = "timer-worker.pid"
	workerLogFile      = "timer-worker.log"
)

// RunWorker starts the timer worker daemon (blocking). It polls all timer
// directories, fires due timers, and handles SIGHUP/SIGTERM.
func RunWorker(root string) error {
	bus, err := agentbus.New(root)
	if err != nil {
		return fmt.Errorf("timer worker: agent-bus: %w", err)
	}

	pidFile := workerPath(root, workerPidFile)
	if err := ensurePIDFile(pidFile); err != nil {
		return err
	}
	defer removePIDFile(pidFile)

	if err := writePIDFile(pidFile); err != nil {
		return err
	}

	logFile, err := openLogFile(root)
	if err != nil {
		return err
	}
	defer logFile.Close()

	// Build stores for ephemeral and persistent timers.
	ephemeralDir := filepath.Join(root, "_WORK_", "agent-bus", "timers")
	persistentConfDir := filepath.Join(root, ".starfleet-ai", "conf", "timers")
	persistentVarDir := filepath.Join(root, ".starfleet-ai", "var", "timers")
	_ = os.MkdirAll(persistentConfDir, 0o755)
	_ = os.MkdirAll(persistentVarDir, 0o755)

	stores := []*Store{}
	if s, err := NewStore(ephemeralDir, "e"); err == nil {
		stores = append(stores, s)
	}
	if s, err := NewStore(persistentConfDir, "p"); err == nil {
		stores = append(stores, s)
	}

	logf(logFile, "timer worker started (pid %d, stores: %d)", os.Getpid(), len(stores))

	// Signal handling.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGHUP, syscall.SIGTERM, syscall.SIGINT)

	ticker := time.NewTicker(workerPollInterval)
	defer ticker.Stop()

	// Immediate first tick.
	processTimers(stores, bus, logFile, persistentVarDir)

	for {
		select {
		case s := <-sigCh:
			switch s {
			case syscall.SIGHUP:
				logf(logFile, "SIGHUP received — re-polling timers")
				processTimers(stores, bus, logFile, persistentVarDir)
			case syscall.SIGTERM, syscall.SIGINT:
				logf(logFile, "signal %v — shutting down", s)
				return nil
			}
		case <-ticker.C:
			processTimers(stores, bus, logFile, persistentVarDir)
		}
	}
}

// StopWorker sends SIGTERM to the running worker daemon.
func StopWorker(root string) error {
	pidFile := workerPath(root, workerPidFile)
	data, err := os.ReadFile(pidFile)
	if err != nil {
		return fmt.Errorf("timer worker: not running (no PID file)")
	}
	pid, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil || pid <= 0 {
		return fmt.Errorf("timer worker: invalid PID file")
	}
	proc, err := os.FindProcess(int(pid))
	if err != nil {
		return fmt.Errorf("timer worker: process %d not found", pid)
	}
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		return fmt.Errorf("timer worker: signal failed: %w", err)
	}
	return nil
}

// WorkerStatus reports whether the worker is running.
func WorkerStatus(root string) (running bool, pid int) {
	pidFile := workerPath(root, workerPidFile)
	data, err := os.ReadFile(pidFile)
	if err != nil {
		return false, 0
	}
	n, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil || n <= 0 {
		return false, 0
	}
	proc, err := os.FindProcess(int(n))
	if err != nil {
		return false, 0
	}
	if err := proc.Signal(syscall.Signal(0)); err != nil {
		return false, 0
	}
	return true, int(n)
}

// processTimers fires all due timers across all stores.
func processTimers(stores []*Store, bus *agentbus.Bus, logFile *os.File, persistentVarDir string) {
	now := time.Now()

	for _, store := range stores {
		timers, err := store.List()
		if err != nil {
			logf(logFile, "list %s: %v", store.Dir(), err)
			continue
		}
		for _, t := range timers {
			if !t.IsDue(now) {
				continue
			}
			targets := resolveTargets(bus, t)
			if len(targets) == 0 {
				logf(logFile, "%s: no eligible targets — skipping fire", t.ID)
				continue
			}
			for _, target := range targets {
				err := bus.DoPost(target, strings.Fields(t.Message), false, "", "")
				if err != nil {
					logf(logFile, "%s: tell %s failed: %v", t.ID, target, err)
				} else {
					logf(logFile, "%s: told %s: %s", t.ID, target, truncate(t.Message, 60))
				}
			}
			advanceOrDelete(store, t, persistentVarDir)
		}
	}
}

// resolveTargets returns the list of ship IDs to send the directive to,
// based on the timer's target spec and the current fleet board.
func resolveTargets(bus *agentbus.Bus, t *TimerRecord) []string {
	switch t.Target.Type {
	case TargetShip:
		return []string{t.Target.Value}
	case TargetFleet:
		idle := fleetIdle(bus)
		if len(idle) == 0 {
			return nil
		}
		return []string{pickLeastLoaded(idle)}
	case TargetFleetAll:
		return fleetIdle(bus)
	default:
		return []string{t.Target.Value}
	}
}

// fleetIdle returns all non-stale board entries with state idle or done.
func fleetIdle(bus *agentbus.Bus) []string {
	var out []string
	for _, e := range bus.BoardEntries() {
		if e.Stale {
			continue
		}
		switch e.State {
		case "idle", "done":
			out = append(out, e.Agent)
		}
	}
	return out
}

// pickLeastLoaded selects the ship with the lowest inbox count from a list.
func pickLeastLoaded(ships []string) string {
	if len(ships) == 0 {
		return ""
	}
	if len(ships) == 1 {
		return ships[0]
	}
	// Simple: random for now, could be enhanced with inbox counts.
	return ships[rand.Intn(len(ships))]
}

// advanceOrDelete updates the next_fire time or deletes the timer after firing.
func advanceOrDelete(store *Store, t *TimerRecord, persistentVarDir string) {
	switch t.Schedule.Type {
	case ScheduleOnce:
		// Once timers: delete after fire.
		_ = store.Delete(t.ID)
	case ScheduleCron:
		// Cron timers: compute next fire from the expression.
		parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
		sched, err := parser.Parse(t.Schedule.CronExpr)
		if err == nil {
			t.NextFire = sched.Next(time.Now().UTC()).Unix()
			_ = store.Update(t)
		} else {
			// Bad cron expr: delete.
			_ = store.Delete(t.ID)
		}
	case ScheduleInterval:
		t.NextFire = time.Now().Unix() + t.Schedule.IntervalSec
		_ = store.Update(t)
	}
}

// --- helpers ---

func workerPath(root, file string) string {
	return filepath.Join(root, "_WORK_", "agent-bus", file)
}

func ensurePIDFile(pidFile string) error {
	if _, err := os.Stat(pidFile); err == nil {
		// PID file exists — check if process is alive.
		data, err := os.ReadFile(pidFile)
		if err == nil {
			pid, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
			if err == nil && pid > 0 {
				proc, err := os.FindProcess(int(pid))
				if err == nil && proc.Signal(syscall.Signal(0)) == nil {
					return fmt.Errorf("timer worker: already running (pid %d)", pid)
				}
			}
		}
		// Stale PID file — remove it.
		os.Remove(pidFile)
	}
	return nil
}

func writePIDFile(pidFile string) error {
	return os.WriteFile(pidFile, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o644)
}

func removePIDFile(pidFile string) {
	os.Remove(pidFile)
}

func openLogFile(root string) (*os.File, error) {
	logDir := filepath.Join(root, "_WORK_", "agent-bus", "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return nil, fmt.Errorf("timer worker: mkdir logs: %w", err)
	}
	return os.OpenFile(
		filepath.Join(logDir, workerLogFile),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644,
	)
}

func logf(f *os.File, format string, args ...any) {
	if f == nil {
		return
	}
	ts := time.Now().Format("2006-01-02 15:04:05")
	all := append([]any{ts}, args...)
	fmt.Fprintf(f, "%s\t"+format+"\n", all...)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
