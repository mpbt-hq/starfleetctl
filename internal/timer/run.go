// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
//
// CLI dispatcher for the `timer` subcommand.
package timer

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/metux/starfleetctl/internal/agentbus"
	"github.com/metux/starfleetctl/internal/config"
	"github.com/robfig/cron/v3"
)

const usage = `starfleetctl timer — fleet scheduling (one-time, interval, cron)

Usage:
  timer set --at "17:30" --type ship --text "status?" [flags]     one-time directive
  timer set --every 10m --type ship --text "check status" recurring directive
  timer set --cron "0 4 * * *" --type ship --text "morning"       cron directive
  timer set --every 5m --type command --text "model gpt-4o"       command timer

Flags for set:
  --name <key>                      unique timer key (auto-generated if omitted)
  --desc <text>                     human-readable description
  --type <ship|command>             message type (default: ship)
  --text <text>                     message body or command verb+args
  --target <ship|fleet|fleet-all>   where to send (default: ship = self)
  --tz <timezone>                   display timezone (default: UTC)
  --persistent                      store in .starfleet-ai/ (survive reset)
  --ephemeral                       store in .starfleet-ai/var/ (default for --every/--at)

  timer list [--all] [--json]      list timers
  timer cancel <id>                cancel a timer by key
  timer clear                      cancel all my timers
  timer pause <id>                 disable a timer
  timer resume <id>                re-enable a timer
  timer status                     show worker status

Timer worker subcommands:
  timer worker                     show this help
  timer worker start               run in foreground (blocking)
  timer worker stop                stop the daemon
  timer worker autostart           start daemon if not running
  timer worker restart             stop + autostart (background)
`

// Run dispatches a `timer` invocation given the resolved workspace root.
func Run(root string, args []string) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		fmt.Print(usage)
		return 0
	}
	switch args[0] {
	case "set":
		return runSet(root, args[1:])
	case "list":
		return runList(root, args[1:])
	case "cancel":
		return runCancel(root, args[1:])
	case "clear":
		return runClear(root, args[1:])
	case "pause":
		return runPause(root, args[1:], true)
	case "resume":
		return runPause(root, args[1:], false)
	case "worker":
		return runWorker(root, args[1:])
	case "status":
		return runStatus(root)
	default:
		fmt.Fprintf(os.Stderr, "timer: unknown subcommand: %s\n", args[0])
		return 2
	}
}

func runSet(root string, args []string) int {
	var (
		scheduleType ScheduleType
		atStr        string
		everyStr     string
		cronExpr     string
		timerName    string
		description  string
		msgType      = "ship"
		msgText      string
		targetType   = TargetShip
		targetValue  string
		tz           string
		persistent   *bool // nil = auto
	)

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--at":
			if i+1 < len(args) {
				atStr = args[i+1]
				scheduleType = ScheduleOnce
				i++
			}
		case "--every":
			if i+1 < len(args) {
				everyStr = args[i+1]
				scheduleType = ScheduleInterval
				i++
			}
		case "--cron":
			if i+1 < len(args) {
				cronExpr = args[i+1]
				scheduleType = ScheduleCron
				i++
			}
		case "--type":
			if i+1 < len(args) {
				msgType = args[i+1]
				i++
			}
		case "--text":
			if i+1 < len(args) {
				msgText = args[i+1]
				i++
			}
		case "--name":
			if i+1 < len(args) {
				timerName = args[i+1]
				i++
			}
		case "--desc":
			if i+1 < len(args) {
				description = args[i+1]
				i++
			}
		case "--target":
			if i+1 < len(args) {
				t := args[i+1]
				switch t {
				case "fleet":
					targetType = TargetFleet
				case "fleet-all":
					targetType = TargetFleetAll
				default:
					targetType = TargetShip
					targetValue = t
				}
				i++
			}
		case "--tz":
			if i+1 < len(args) {
				tz = args[i+1]
				i++
			}
		case "--persistent":
			v := true
			persistent = &v
		case "--ephemeral":
			v := false
			persistent = &v
		default:
			// Bare argument = text (for backwards compat)
			if msgText == "" {
				msgText = args[i]
			}
		}
	}

	if scheduleType == "" {
		fmt.Fprintln(os.Stderr, "timer: need --at, --every, or --cron")
		return 2
	}
	if msgText == "" {
		fmt.Fprintln(os.Stderr, "timer: need --text")
		return 2
	}

	// Auto-generate name if not given.
	if timerName == "" {
		timerName = GenerateName()
	}

	// Auto-detect persistence: --cron defaults to persistent.
	if persistent == nil {
		v := scheduleType == ScheduleCron
		persistent = &v
	}

	// Parse schedule into next_fire timestamp.
	nextFire, err := parseSchedule(scheduleType, atStr, everyStr, cronExpr, tz)
	if err != nil {
		fmt.Fprintf(os.Stderr, "timer: %v\n", err)
		return 2
	}

	// Build timer record.
	rec := &TimerRecord{
		ID:          timerName,
		Description: description,
		Owner:       "", // will be set from bus identity
		Target:      TargetSpec{Type: targetType, Value: targetValue},
		Type:        msgType,
		Text:        msgText,
		Schedule:    scheduleFromFlags(scheduleType, cronExpr, everyStr, atStr),
		Timezone:    tz,
		Persistent:  *persistent,
		Enabled:     true,
		CreatedAt:   time.Now().Unix(),
		NextFire:    nextFire,
	}

	// Resolve owner from bus identity.
	bus, err := agentbus.New(root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "timer: agent-bus: %v\n", err)
		return 1
	}
	rec.Owner = bus.ShipID
	if targetType == TargetShip && targetValue == "" {
		rec.Target.Value = bus.ShipID
	}

	// Choose store directory.
	store, err := PickStore(root, rec.Persistent)
	if err != nil {
		fmt.Fprintf(os.Stderr, "timer: %v\n", err)
		return 1
	}

	id, err := store.Create(rec)
	if err != nil {
		fmt.Fprintf(os.Stderr, "timer: %v\n", err)
		return 1
	}

	// SIGHUP the worker for immediate pickup.
	NotifyWorker(root)

	kind := "once"
	if scheduleType == ScheduleInterval {
		kind = "interval"
	} else if scheduleType == ScheduleCron {
		kind = "cron"
	}
	loc := "ephemeral"
	if rec.Persistent {
		loc = "persistent"
	}
	fmt.Printf("timer %s created: %s [%s] (next fire: %s, %s, %s)\n",
		id, kind, msgType, time.Unix(nextFire, 0).UTC().Format("2006-01-02 15:04:05 UTC"), loc, targetDesc(targetType, targetValue, bus.ShipID))
	return 0
}

func runList(root string, args []string) int {
	jsonOut := false
	showAll := false
	for _, a := range args {
		switch a {
		case "--json":
			jsonOut = true
		case "--all":
			showAll = true
		}
	}

	bus, err := agentbus.New(root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "timer: agent-bus: %v\n", err)
		return 1
	}
	owner := bus.ShipID

	var all []*TimerRecord
	for _, td := range TimerDirs(root) {
		store, err := NewStore(td.Dir)
		if err != nil {
			continue
		}
		timers, err := store.List()
		if err != nil {
			continue
		}
		all = append(all, timers...)
	}

	if !showAll {
		var filtered []*TimerRecord
		for _, t := range all {
			if t.Owner == owner {
				filtered = append(filtered, t)
			}
		}
		all = filtered
	}

	if len(all) == 0 {
		if jsonOut {
			fmt.Println("[]")
		} else {
			fmt.Println("no timers")
		}
		return 0
	}

	if jsonOut {
		for _, t := range all {
			data, _ := marshalJSON(t)
			fmt.Println(string(data))
		}
		return 0
	}

	// Human-readable table.
	fmt.Printf("%-20s %-8s %-8s %-22s %-12s %-8s %s\n", "ID", "TYPE", "KIND", "NEXT FIRE", "TARGET", "STATUS", "TEXT")
	for _, t := range all {
		nf := time.Unix(t.NextFire, 0).UTC().Format("2006-01-02 15:04 UTC")
		if t.NextFire == 0 {
			nf = "—"
		}
		st := "active"
		if !t.Enabled {
			st = "paused"
		}
		kind := string(t.Schedule.Type)
		tgt := targetDesc(t.Target.Type, t.Target.Value, bus.ShipID)
		txt := truncate(t.Text, 30)
		fmt.Printf("%-20s %-8s %-8s %-22s %-12s %-8s %s\n", t.ID, t.Type, kind, nf, tgt, st, txt)
	}
	return 0
}

func runCancel(root string, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "timer cancel: need <id>")
		return 2
	}
	id := args[0]
	for _, td := range TimerDirs(root) {
		store, err := NewStore(td.Dir)
		if err != nil {
			continue
		}
		if _, err := store.Get(id); err == nil {
			if err := store.Delete(id); err != nil {
				fmt.Fprintf(os.Stderr, "timer: delete %s: %v\n", id, err)
				return 1
			}
			fmt.Printf("timer %s cancelled\n", id)
			NotifyWorker(root)
			return 0
		}
	}
	fmt.Fprintf(os.Stderr, "timer: %s not found\n", id)
	return 1
}

func runClear(root string, args []string) int {
	bus, err := agentbus.New(root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "timer: agent-bus: %v\n", err)
		return 1
	}
	owner := bus.ShipID
	count := 0
	for _, td := range TimerDirs(root) {
		store, err := NewStore(td.Dir)
		if err != nil {
			continue
		}
		timers, err := store.List()
		if err != nil {
			continue
		}
		for _, t := range timers {
			if t.Owner == owner {
				_ = store.Delete(t.ID)
				count++
			}
		}
	}
	NotifyWorker(root)
	fmt.Printf("timer: cleared %d timer(s)\n", count)
	return 0
}

func runPause(root string, args []string, disable bool) int {
	if len(args) == 0 {
		action := "pause"
		if !disable {
			action = "resume"
		}
		fmt.Fprintf(os.Stderr, "timer %s: need <id>\n", action)
		return 2
	}
	id := args[0]
	for _, td := range TimerDirs(root) {
		store, err := NewStore(td.Dir)
		if err != nil {
			continue
		}
		rec, err := store.Get(id)
		if err != nil {
			continue
		}
		rec.Enabled = !disable
		if err := store.Update(rec); err != nil {
			fmt.Fprintf(os.Stderr, "timer: update %s: %v\n", id, err)
			return 1
		}
		action := "paused"
		if !disable {
			action = "resumed"
		}
		fmt.Printf("timer %s %s\n", id, action)
		NotifyWorker(root)
		return 0
	}
	fmt.Fprintf(os.Stderr, "timer: %s not found\n", id)
	return 1
}

func runWorker(root string, args []string) int {
	// If we're the forked child daemon, run directly in foreground.
	if os.Getenv("STARFLEET_TIMER_WORKER") == "1" {
		os.Unsetenv("STARFLEET_TIMER_WORKER")
		if err := RunWorker(root); err != nil {
			fmt.Fprintf(os.Stderr, "timer worker: %v\n", err)
			return 1
		}
		return 0
	}

	// No args or help → show usage
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Print(usage)
		return 0
	}

	switch args[0] {
	case "start":
		// Run in foreground (blocking).
		if err := RunWorker(root); err != nil {
			fmt.Fprintf(os.Stderr, "timer worker: %v\n", err)
			return 1
		}
		return 0

	case "stop":
		if err := StopWorker(root); err != nil {
			fmt.Fprintf(os.Stderr, "timer worker stop: %v\n", err)
			return 1
		}
		fmt.Println("timer worker stopped")
		return 0

	case "autostart":
		running, pid := WorkerStatus(root)
		if running {
			fmt.Printf("timer worker: already running (pid %d)\n", pid)
			return 0
		}
		if err := StartWorker(root); err != nil {
			fmt.Fprintf(os.Stderr, "timer worker autostart: %v\n", err)
			return 1
		}
		fmt.Println("timer worker started")
		return 0

	case "restart":
		if err := RestartWorker(root); err != nil {
			fmt.Fprintf(os.Stderr, "timer worker restart: %v\n", err)
			return 1
		}
		fmt.Println("timer worker restarted")
		return 0

	default:
		fmt.Fprintf(os.Stderr, "timer worker: unknown subcommand: %s\n\n%s", args[0], usage)
		return 2
	}
}

func runStatus(root string) int {
	running, pid := WorkerStatus(root)
	if running {
		fmt.Printf("timer worker: running (pid %d)\n", pid)
	} else {
		fmt.Println("timer worker: not running")
	}
	return 0
}

// --- helpers ---

// StartWorker forks the worker into the background as a daemon.
func StartWorker(root string) error {
	running, _ := WorkerStatus(root)
	if running {
		return fmt.Errorf("timer worker: already running")
	}

	logDir := filepath.Join(config.BusDir(root), "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return fmt.Errorf("timer worker: mkdir logs: %w", err)
	}
	logFile := filepath.Join(logDir, workerLogFile)
	logF, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("timer worker: open log: %w", err)
	}

	cmd := exec.Command(os.Args[0], "timer", "worker")
	cmd.Env = append(os.Environ(), "STARFLEET_TIMER_WORKER=1")
	cmd.Dir = root
	cmd.Stdout = logF
	cmd.Stderr = logF
	cmd.Stdin = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
		Pgid:    0,
	}

	if err := cmd.Start(); err != nil {
		logF.Close()
		return fmt.Errorf("timer worker: start: %w", err)
	}
	logF.Close()
	cmd.Process.Release()
	return nil
}

// RestartWorker stops the worker if running, then starts it again.
func RestartWorker(root string) error {
	if running, _ := WorkerStatus(root); running {
		if err := StopWorker(root); err != nil {
			return fmt.Errorf("timer worker restart: stop: %w", err)
		}
		// Wait for process to exit.
		for i := 0; i < 10; i++ {
			if running, _ = WorkerStatus(root); !running {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	return StartWorker(root)
}

// parseSchedule parses the flags into a next_fire unix timestamp.
func parseSchedule(stype ScheduleType, atStr, everyStr, cronExpr, tz string) (int64, error) {
	now := time.Now().UTC()
	switch stype {
	case ScheduleOnce:
		t, err := ParseAtTime(atStr, tz)
		if err != nil {
			return 0, err
		}
		if t.Before(now) {
			return 0, fmt.Errorf("timer: --at time is in the past")
		}
		return t.Unix(), nil

	case ScheduleInterval:
		d, err := time.ParseDuration(everyStr)
		if err != nil {
			return 0, fmt.Errorf("timer: invalid --every duration: %w", err)
		}
		if d <= 0 {
			return 0, fmt.Errorf("timer: --every must be positive")
		}
		return now.Add(d).Unix(), nil

	case ScheduleCron:
		if cronExpr == "" {
			return 0, fmt.Errorf("timer: --cron requires an expression")
		}
		// For cron, we compute next fire on the fly using robfig/cron.
		// Store the cron expr; the worker will evaluate it.
		// For initial next_fire, use robfig to compute first fire.
		next, err := CronNextFire(cronExpr, tz)
		if err != nil {
			return 0, fmt.Errorf("timer: invalid --cron: %w", err)
		}
		return next.Unix(), nil

	default:
		return 0, fmt.Errorf("timer: unknown schedule type: %s", stype)
	}
}

// ParseAtTime parses a --at string into a UTC time.
// Supported formats:
//   - "17:30" (today)
//   - "2006-01-02 15:04" (absolute)
//   - "2006-01-02T15:04:00Z" (ISO 8601)
//   - "tomorrow 17:30"
func ParseAtTime(s, tz string) (time.Time, error) {
	now := time.Now().UTC()
	if s == "" {
		return time.Time{}, fmt.Errorf("--at requires a time")
	}

	// Handle "tomorrow"
	s = strings.TrimSpace(s)
	prefix := ""
	rest := s
	if strings.HasPrefix(strings.ToLower(s), "tomorrow") {
		prefix = "tomorrow"
		rest = strings.TrimSpace(s[len("tomorrow"):])
	} else if strings.HasPrefix(strings.ToLower(s), "morgen") {
		prefix = "tomorrow"
		rest = strings.TrimSpace(s[len("morgen"):])
	}

	// Try various formats.
	formats := []string{
		"15:04",
		"2006-01-02 15:04",
		"2006-01-02T15:04:00Z",
		"15:04:05",
	}

	for _, fmt := range formats {
		if t, err := time.Parse(fmt, rest); err == nil {
			// Combine with date.
			if prefix == "tomorrow" {
				return time.Date(now.Year(), now.Month(), now.Day()+1,
					t.Hour(), t.Minute(), t.Second(), 0, time.UTC), nil
			}
			// Today or absolute date.
			result := time.Date(now.Year(), now.Month(), now.Day(),
				t.Hour(), t.Minute(), t.Second(), 0, time.UTC)
			if t.Year() > 2000 {
				// Full date was parsed.
				result = t.UTC()
			}
			return result, nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid --at format: %s", s)
}

// CronNextFire computes the next fire time for a cron expression using robfig/cron.
func CronNextFire(expr, tz string) (time.Time, error) {
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	sched, err := parser.Parse(expr)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid cron expression %q: %w", expr, err)
	}
	return sched.Next(time.Now().UTC()), nil
}

func scheduleFromFlags(stype ScheduleType, cronExpr, everyStr, atStr string) Schedule {
	s := Schedule{Type: stype}
	switch stype {
	case ScheduleCron:
		s.CronExpr = cronExpr
	case ScheduleInterval:
		if d, err := time.ParseDuration(everyStr); err == nil {
			s.IntervalSec = int64(d.Seconds())
		}
	case ScheduleOnce:
		// FireAt is computed at set time; store the expression for display.
	}
	return s
}

func targetDesc(tt TargetType, value, self string) string {
	switch tt {
	case TargetFleet:
		return "fleet"
	case TargetFleetAll:
		return "fleet-all"
	default:
		if value == "" || value == self {
			return self
		}
		return value
	}
}

// TimerDir describes a timer store location and its ID prefix.
type TimerDir struct {
	Dir string
}

// TimerDirs returns all timer directories for the given workspace root.
func TimerDirs(root string) []TimerDir {
	return []TimerDir{
		{filepath.Join(root, ".starfleet-ai", "var", "timers")},
		{filepath.Join(root, ".starfleet-ai", "conf", "timers")},
	}
}

// PickStore returns the appropriate store for the given persistence mode.
func PickStore(root string, persistent bool) (*Store, error) {
	if persistent {
		return NewStore(filepath.Join(root, ".starfleet-ai", "conf", "timers"))
	}
	return NewStore(filepath.Join(root, ".starfleet-ai", "var", "timers"))
}

// NotifyWorker sends SIGHUP to the running timer worker (if any) for immediate pickup.
func NotifyWorker(root string) {
	pidFile := workerPath(root, workerPidFile)
	data, err := os.ReadFile(pidFile)
	if err != nil {
		return
	}
	pid, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil || pid <= 0 {
		return
	}
	if proc, err := os.FindProcess(int(pid)); err == nil {
		_ = proc.Signal(syscall.SIGHUP)
	}
}

func marshalJSON(v any) ([]byte, error) {
	return json.Marshal(v)
}
