// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package session

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/metux/starfleetctl/internal/comms"
	"github.com/metux/starfleetctl/internal/config"
	"github.com/metux/starfleetctl/internal/identity"
	"github.com/metux/starfleetctl/internal/shipnames"
)

const (
	statusDir  = "comms/status"
	defaultMax = 6
	busTTL     = 900
)

// runAutoscale implements `session autoscale <command> [args…]`.
func runAutoscale(root string, args []string) int {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, "session autoscale: need <command> (status|need|respawn)\n")
		return 2
	}
	switch args[0] {
	case "-h", "--help":
		fmt.Print(`session autoscale <command> [args…]

Fleet auto-scaling (explicit on-demand trigger).

Commands:
  status [--max <cap>]
      Show current non-stale fleet size, idle count, and configured cap.

  need <N> --reason "<text>" [--release <rel>] [--client claude|opencode]
           [--max <cap>] [--supervisor <name>]
           [--permission-mode <mode>] [--dry-run]
       Spawn up to <cap> minus the current non-stale fleet size, capped at
       what's needed after subtracting currently-idle ships.  Always prints
       a decision and appends it to the audit log; an actual spawn also
       posts a loud comms broadcast.

  respawn [--dry-run]
       Respawn any declarative standing ships that have died (crashed/stopped
       without stop-requested marker). Checks stop-requested marker to avoid
       restarting intentionally stopped ships. Uses fleet.yaml ships config
       for model, client, and launch-type.
`)
		return 0
	case "status":
		return runAutoscaleStatus(root, args[1:])
	case "need":
		return runAutoscaleNeed(root, args[1:])
	case "respawn":
		return runAutoscaleRespawn(root, args[1:])
	default:
		fmt.Fprintf(os.Stderr, "session autoscale: unknown command '%s'\n", args[0])
		return 2
	}
}

func runAutoscaleStatus(root string, args []string) int {
	max := defaultMax
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--max":
			i++
			if i < len(args) {
				if n, err := strconv.Atoi(args[i]); err == nil && n >= 0 {
					max = n
				}
			}
		default:
			fmt.Fprintf(os.Stderr, "session autoscale: unknown option '%s'\n", args[i])
			return 2
		}
	}

	total, idle := fleetCounts(root)
	fmt.Printf("fleet-autoscale: %d ship(s) on board (non-stale), %d idle, cap=%d\n", total, idle, max)
	return 0
}

func runAutoscaleNeed(root string, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "session autoscale need: need <N>")
		return 2
	}

	need, err := strconv.Atoi(args[0])
	if err != nil || need < 0 {
		fmt.Fprintln(os.Stderr, "session autoscale need: <N> must be a non-negative integer")
		return 2
	}
	args = args[1:]

	reason := ""
	release := "master"
	client := "claude"
	max := defaultMax
	supervisor := shipnames.FlagshipName(root)
	permissionMode := "dontAsk"
	dry := false

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--reason":
			i++
			if i < len(args) {
				reason = args[i]
			}
		case "--release":
			i++
			if i < len(args) {
				release = args[i]
			}
		case "--client":
			i++
			if i < len(args) {
				client = args[i]
			}
		case "--max":
			i++
			if i < len(args) {
				if n, err := strconv.Atoi(args[i]); err == nil && n >= 0 {
					max = n
				}
			}
		case "--supervisor":
			i++
			if i < len(args) {
				supervisor = args[i]
			}
		case "--permission-mode":
			i++
			if i < len(args) {
				permissionMode = args[i]
			}
		case "--dry-run":
			dry = true
		default:
			fmt.Fprintf(os.Stderr, "session autoscale need: unknown option '%s'\n", args[i])
			return 2
		}
	}

	if reason == "" {
		fmt.Fprintln(os.Stderr, "session autoscale need: --reason \"...\" is required (spawns must be auditable)")
		return 2
	}

	total, idle := fleetCounts(root)

	toSpawn := need - idle
	if toSpawn < 0 {
		toSpawn = 0
	}
	room := max - total
	if room < 0 {
		room = 0
	}
	spawn := toSpawn
	if spawn > room {
		spawn = room
	}

	if spawn <= 0 {
		var msg string
		if toSpawn == 0 {
			msg = fmt.Sprintf("no-op: %d idle ship(s) already cover need=%d (fleet=%d/%d)", idle, need, total, max)
		} else {
			msg = fmt.Sprintf("no-op: at/above cap (fleet=%d/%d, room=%d), NOT spawning %d needed ship(s) — raise --max or wait for a ship to free up", total, max, room, toSpawn)
		}
		fmt.Printf("fleet-autoscale: %s\n", msg)
		appendAudit(root, fmt.Sprintf("need=%d reason=%q %s", need, reason, msg))
		return 0
	}

	fmt.Printf("fleet-autoscale: spawning %d ship(s) (need=%d, idle=%d, fleet=%d/%d) — reason: %s\n", spawn, need, idle, total, max, reason)
	appendAudit(root, fmt.Sprintf("need=%d reason=%q spawning=%d fleet=%d/%d release=%s client=%s supervisor=%s permission_mode=%s dry_run=%v", need, reason, spawn, total, max, release, client, supervisor, permissionMode, dry))

	if dry {
		fmt.Println("fleet-autoscale: --dry-run, not actually spawning")
		return 0
	}

	spawned := spawnShips(root, spawn, release, client, supervisor, permissionMode, reason, need, total, max)
	if len(spawned) == 0 {
		fmt.Fprintln(os.Stderr, "fleet-autoscale: no ships spawned (all attempts failed)")
		return 1
	}

	fmt.Printf("fleet-autoscale: spawned: %s\n", strings.Join(spawned, " "))
	for _, s := range spawned {
		fmt.Printf("  scripts/agent-attach %s\n", s)
	}
	return 0
}

// spawnShips launches N worker sessions and returns the successfully spawned names.
func spawnShips(root string, spawn int, release, client, supervisor, permissionMode, reason string, need, total, max int) []string {
	var spawned []string
	callerID := identity.ShipID()
	if callerID == "" {
		callerID = "unknown"
	}

	for i := 0; i < spawn; i++ {
		reg := shipnames.New(root)
		name, err := reg.AssignName()
		if err != nil || name == "" {
			fmt.Fprintf(os.Stderr, "fleet-autoscale: no ship names available, aborting\n")
			break
		}

		launchArgs := []string{release, "--client", client, "--name", name, "--tier", "worker", "--supervisor", supervisor}
		if client == "claude" {
			launchArgs = append(launchArgs, "--permission-mode", permissionMode)
		}

		if err := doSpawn(root, launchArgs); err != nil {
			_ = reg.DoRelease(name)
			appendAudit(root, fmt.Sprintf("spawn FAILED name=%s release=%s client=%s reason=%q", name, release, client, reason))
			fmt.Fprintf(os.Stderr, "fleet-autoscale: spawn failed for %s (%v), releasing name, aborting remaining spawns\n", name, err)
			break
		}

		spawned = append(spawned, name)
		appendAudit(root, fmt.Sprintf("spawned name=%s release=%s client=%s supervisor=%s permission_mode=%s reason=%q", name, release, client, supervisor, permissionMode, reason))
	}

	if len(spawned) > 0 {
		permPart := ""
		if client == "claude" {
			permPart = fmt.Sprintf(", permission-mode=%s", permissionMode)
		}
		bcastText := fmt.Sprintf("Auto-scale: spawned %d new worker ship(s) [%s] (release=%s, supervisor=%s%s) — reason: %s — triggered by %s, fleet now %d/%d",
			len(spawned), strings.Join(spawned, " "), release, supervisor, permPart, reason, callerID, total+len(spawned), max)

		if bus, err := comms.New(root); err == nil {
			_ = bus.DoPost("all", []string{bcastText}, false, "", "", "control")
		}
	}

	return spawned
}

// runAutoscaleRespawn implements `session autoscale respawn [--dry-run]`.
// Respawns any declarative standing ships that have died (crashed/stopped
// without stop-requested marker). Checks stop-requested marker to avoid
// restarting intentionally stopped ships. Uses fleet.yaml ships config
// for model, client, and launch-type.
func runAutoscaleRespawn(root string, args []string) int {
	dry := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--dry-run":
			dry = true
		case "-h", "--help":
			fmt.Print(`session autoscale respawn [--dry-run]

Respawn any declarative standing ships that have died (crashed/stopped
without stop-requested marker). Checks stop-requested marker to avoid
restarting intentionally stopped ships. Uses fleet.yaml ships config
for model, client, and launch-type.

Options:
  --dry-run    Show what would be respawned without actually doing it.
`)
			return 0
		default:
			fmt.Fprintf(os.Stderr, "session autoscale respawn: unknown option '%s'\n", args[i])
			return 2
		}
	}

	// Load fleet config to get declarative ships
	cfg, err := config.Load("")
	if err != nil {
		fmt.Fprintf(os.Stderr, "respawn: load config: %v\n", err)
		return 1
	}
	if len(cfg.Fleet.Ships) == 0 {
		fmt.Println("respawn: no declarative ships configured in fleet.yaml")
		return 0
	}

	// Build map of live ships from status files
	liveShips := make(map[string]bool)
	entries, err := os.ReadDir(filepath.Join(config.BusDir(""), "status"))
	if err == nil {
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".tsv") {
				continue
			}
			ship := strings.TrimSuffix(e.Name(), ".tsv")
			data, err := os.ReadFile(filepath.Join(config.BusDir(""), "status", e.Name()))
			if err != nil {
				continue
			}
			fields := strings.SplitN(strings.TrimSpace(string(data)), "\t", 8)
			if len(fields) >= 8 {
				epoch, err := strconv.ParseInt(fields[0], 10, 64)
				if err == nil && time.Now().Unix()-epoch < busTTL {
					liveShips[ship] = true
				}
			}
		}
	}

	// Check each declarative ship
	var toRespawn []config.ShipConfig
	for _, ship := range cfg.Fleet.Ships {
		// Skip terminal/console launch types - they can't be auto-respawned
		if ship.LaunchType == "terminal" || ship.LaunchType == "console" {
			fmt.Printf("respawn: skipping %s (launch_type=%s not supported for auto-respawn)\n", ship.Name, ship.LaunchType)
			continue
		}

		// Check if ship is alive
		if liveShips[ship.Name] {
			continue // ship is alive
		}

		// Check stop-requested marker - if present, ship was intentionally stopped
		if isStopRequested("", ship.Name) {
			fmt.Printf("respawn: skipping %s (intentional stop requested)\n", ship.Name)
			continue
		}

		// Ship is dead and not intentionally stopped - needs respawn
		toRespawn = append(toRespawn, ship)
		fmt.Printf("respawn: ship %s is dead, will respawn\n", ship.Name)
	}

	if len(toRespawn) == 0 {
		fmt.Println("respawn: no ships need respawning")
		return 0
	}

	fmt.Printf("respawn: will respawn %d ship(s): %s\n", len(toRespawn), shipNames(toRespawn))

	if dry {
		fmt.Println("respawn: --dry-run, not actually respawning")
		return 0
	}

	// Respawn each dead ship
	for _, ship := range toRespawn {
		if err := doSpawn("", []string{"master", "--client", ship.Client, "--name", ship.Name}); err != nil {
			fmt.Fprintf(os.Stderr, "respawn: failed to respawn %s: %v\n", ship.Name, err)
			continue
		}
		fmt.Printf("respawn: respawned %s\n", ship.Name)
	}

	return 0
}

// shipNames returns a comma-separated list of ship names.
func shipNames(ships []config.ShipConfig) string {
	names := make([]string, len(ships))
	for i, s := range ships {
		names[i] = s.Name
	}
	return strings.Join(names, ", ")
}

// appendAudit appends a line to the autoscale audit log, creating the log
func fleetCounts(root string) (total, idle int) {
	statDir := filepath.Join(config.BusDir(root), "status")
	entries, err := os.ReadDir(statDir)
	if err != nil {
		return 0, 0
	}
	now := time.Now().Unix()
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".tsv") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(statDir, e.Name()))
		if err != nil {
			continue
		}
		fields := strings.SplitN(strings.TrimSpace(string(data)), "\t", 8)
		if len(fields) < 8 {
			continue
		}
		epoch, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil {
			continue
		}
		if now-epoch >= busTTL {
			continue
		}
		total++
		if fields[4] == "idle" {
			idle++
		}
	}
	return
}

// appendAudit appends a line to the autoscale audit log, creating the log
// directory if needed.
func appendAudit(root, msg string) {
	logPath := filepath.Join(config.LogDir(root), "autoscale-events.log")
	_ = os.MkdirAll(filepath.Dir(logPath), 0o755)
	callerID := identity.ShipID()
	if callerID == "" {
		callerID = "unknown"
	}
	ts := time.Now().UTC().Format(time.RFC3339)
	line := fmt.Sprintf("%s\t%s\t%s\n", ts, callerID, msg)
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString(line)
}
