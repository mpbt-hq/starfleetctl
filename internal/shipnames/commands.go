// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package shipnames

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/metux/starfleetctl/internal/fsutil"
)

// DoAssignFlagship reserves the flagship name under the shared assignment lock.
func (r *Registry) DoAssignFlagship() error {
	if err := os.MkdirAll(r.ShipsDir, 0o755); err != nil {
		return err
	}
	lh, err := r.assignLock()
	if err != nil { return err }
	defer lh.Close()
	name := FlagshipName(r.Root)
	path, err := r.shipFile(name)
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("flagship (%s) already reserved", name)
	}
	if err := writeReservation(path); err != nil {
		return err
	}
	fmt.Println(name)
	return nil
}

// DoAssign implements `ship-names assign`: atomically pick the first unused
// name from the pool, falling back to "ws-<pid>" if all are taken.
func (r *Registry) DoAssign() error {
	name, err := r.AssignName()
	if err != nil {
		return err
	}
	fmt.Println(name)
	return nil
}

// currentTTY returns the TTY device path for the current process, or "" if
// stdin is not a terminal (e.g. piped or background).
func currentTTY() string {
	link, err := os.Readlink("/proc/self/fd/0")
	if err != nil {
		return ""
	}
	if strings.HasPrefix(link, "/dev/") {
		return link
	}
	return ""
}

// isPIDDead reports whether a PID is definitely gone. EPERM means the
// process exists but is not signalable by this user, so it remains reserved.
func isPIDDead(pid int) bool {
	if pid <= 0 {
		return true
	}
	return syscall.Kill(pid, 0) == syscall.ESRCH
}

// reservationActive checks both the reservation owner and the OpenCode PID
// recorded in comms status. Detached launches outlive their short-lived parent.
func (r *Registry) reservationActive(name, data string) bool {
	pid, _, _ := parseReservation(data)
	if pid > 0 && !isPIDDead(pid) {
		return true
	}
	if rec, ok := r.loadStatusMap()[name]; ok && rec.PID > 0 && !isPIDDead(rec.PID) {
		return true
	}
	return false
}

// parseReservation parses a reservation file into its components.
// Format: <PID>:<timestamp>[:<tty>]
func parseReservation(data string) (pid int, epoch int64, tty string) {
	line := firstLine(data)
	parts := strings.SplitN(line, ":", 3)
	if len(parts) >= 1 {
		pid, _ = strconv.Atoi(parts[0])
	}
	if len(parts) >= 2 {
		epoch, _ = strconv.ParseInt(parts[1], 10, 64)
	}
	if len(parts) >= 3 {
		tty = parts[2]
	}
	return
}

// ttyScan scans reservations for a name whose PID is dead and whose TTY
// matches the current terminal. Returns the name (or "" if none found).
func (r *Registry) ttyScan() string {
	entries, err := os.ReadDir(r.ShipsDir)
	if err != nil {
		return ""
	}
	myTTY := currentTTY()
	if myTTY == "" {
		return ""
	}
	for _, e := range entries {
		if e.IsDir() || e.Name() == ".assign.lock" {
			continue
		}
		safe, ok := fsutil.Safe(e.Name())
		if !ok {
			continue
		}
		path := filepath.Join(r.ShipsDir, safe)
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		_, _, tty := parseReservation(string(data))
		if tty != myTTY || r.reservationActive(e.Name(), string(data)) {
			continue
		}
		// No live reservation owner or OpenCode process remains on this TTY.
		return e.Name()
	}
	return ""
}

// AssignName picks the first unused name from the pool, falling back to
// "ws-<pid>" if all are taken. Before scanning free names, it attempts to
// reuse a reservation whose PID is dead and whose TTY matches the current
// terminal (supports restart cycles without wasting ship names).
//
// An existing reservation is reclaimable only when neither its owner PID nor
// the OpenCode PID recorded in comms status is alive.
func (r *Registry) AssignName() (string, error) {
	if err := os.MkdirAll(r.ShipsDir, 0o755); err != nil {
		return "", err
	}
	lh, err := r.assignLock()
	if err != nil {
		return "", err
	}
	defer lh.Close()

	// 1. Try to reclaim a reservation for the same TTY (restart cycle).
	if name := r.ttyScan(); name != "" {
		path, err := r.shipFile(name)
		if err == nil {
			if err := writeReservation(path); err == nil {
				return name, nil
			}
		}
	}

	// 2. Pick first truly free name.
	names, err := r.readNames()
	if err != nil {
		return "", err
	}

	for _, name := range names {
		path, err := r.shipFile(name)
		if err != nil {
			continue
		}
		_, err = os.Stat(path)
		if err != nil {
			// No reservation file — name is free
			if err := writeReservation(path); err != nil {
				return "", err
			}
			return name, nil
		}
		// Keep the name while either the runner or its OpenCode client lives.
		data, readErr := os.ReadFile(path)
		if readErr != nil || r.reservationActive(name, string(data)) {
			continue
		}
		// Both processes are gone; reclaim the stale reservation.
		if err := writeReservation(path); err != nil {
			return "", err
		}
		return name, nil
	}
	return fmt.Sprintf("ws-%d", os.Getpid()), nil
}

// loadStatusMap reads all status files and returns a map of ship name -> StatusRecord
func (r *Registry) loadStatusMap() map[string]StatusRecord {
	statusMap := make(map[string]StatusRecord)
	entries, err := os.ReadDir(r.StatusDir)
	if err != nil {
		return statusMap
	}
	for _, e := range entries {
		if !e.Type().IsRegular() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".json")
		path := filepath.Join(r.StatusDir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var rec StatusRecord
		if err := json.Unmarshal(data, &rec); err != nil {
			continue
		}
		if rec.Agent == "" {
			rec.Agent = name
		}
		statusMap[rec.Agent] = rec
	}
	return statusMap
}

// isStale mirrors comms.Bus.stale logic: a ship is stale if it's not in "idle"
// state and its epoch is older than BusTTL (900s default).
func (r *Registry) isStale(epoch int64, state string) bool {
	if state == "idle" {
		return false
	}
	busTTL := int64(900) // default BusTTL from comms
	// Try to read BusTTL from environment or config
	if v := os.Getenv("STARFLEET_BUS_TTL"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			busTTL = n
		}
	}
	return time.Now().Unix()-epoch >= busTTL
}

// StatusRecord mirrors the comms.StatusRecord for status file parsing.
type StatusRecord struct {
	Epoch   int64  `json:"epoch"`
	ISO     string `json:"iso"`
	Agent   string `json:"agent"`
	Project string `json:"project"`
	State   string `json:"state"`
	PID     int    `json:"pid"`
	Handle  string `json:"handle"`
	Note    string `json:"note"`
}

func writeReservation(path string) error {
	tty := currentTTY()
	content := fmt.Sprintf("%d:%d:%s\n", os.Getpid(), time.Now().Unix(), tty)
	return os.WriteFile(path, []byte(content), 0o644)
}

// Reserve exclusively claims name. All reservation mutations share the
// assignment lock so explicit and automatic starts cannot race each other.
func (r *Registry) Reserve(name string) error {
	if err := os.MkdirAll(r.ShipsDir, 0o755); err != nil {
		return err
	}
	lh, err := r.assignLock()
	if err != nil {
		return err
	}
	defer lh.Close()
	path, err := r.shipFile(name)
	if err != nil {
		return err
	}
	data, readErr := os.ReadFile(path)
	if readErr == nil {
		if r.reservationActive(name, string(data)) {
			return fmt.Errorf("ship name %q is already in use", name)
		}
	} else if !os.IsNotExist(readErr) {
		return readErr
	}
	return writeReservation(path)
}

// Lookup reports whether a reservation for name currently exists.
func (r *Registry) Lookup(name string) (string, bool) {
	path, err := r.shipFile(name)
	if err != nil {
		return "", false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	return firstLine(string(data)), true
}

// DoRelease implements `ship-names release <name>`.
func (r *Registry) DoRelease(name string) error {
	if name == "" {
		return fmt.Errorf("release: name required")
	}
	if err := os.MkdirAll(r.ShipsDir, 0o755); err != nil {
		return err
	}
	lh, err := r.assignLock()
	if err != nil {
		return err
	}
	defer lh.Close()
	path, err := r.shipFile(name)
	if err != nil { return err }
	_ = os.Remove(path) // rm -f: missing file is not an error
	return nil
}

// DoList implements `ship-names list`.
func (r *Registry) DoList() error {
	fmt.Printf("Ship name registry (%s):\n", r.ShipsDir)
	fmt.Printf("  %-22s  %s\n", "NAME", "STATUS")
	fmt.Printf("  %-22s  %s\n", "----", "------")

	flagship := FlagshipName(r.Root)
	flagPath, ferr := r.shipFile(flagship)
	if ferr == nil {
		if _, err := os.Stat(flagPath); err == nil {
			fmt.Printf("  %-22s  ACTIVE (flagship)\n", flagship)
		} else {
			fmt.Printf("  %-22s  free\n", flagship)
		}
	} else {
		fmt.Printf("  %-22s  free\n", flagship)
	}

	names, err := r.readNames()
	if err != nil {
		return err
	}
	for _, name := range names {
		path, perr := r.shipFile(name)
		if perr != nil {
			fmt.Printf("  %-22s  free\n", name)
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			fmt.Printf("  %-22s  free\n", name)
			continue
		}
		payload := firstLine(string(data))
		if payload == "" {
			payload = "?"
		}
		fmt.Printf("  %-22s  ACTIVE (%s)\n", name, payload)
	}
	return nil
}

func firstLine(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			return s[:i]
		}
	}
	return s
}

// DoGC implements `ship-names gc`: remove reservations with no matching
// live comms status entry.
func (r *Registry) DoGC() error {
	entries, err := os.ReadDir(r.ShipsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	lh, err := r.assignLock()
	if err != nil {
		return err
	}
	defer lh.Close()

	var names []string
	for _, e := range entries {
		if !e.Type().IsRegular() || e.Name() == ".assign.lock" {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)

	removed := 0
	for _, name := range names {
		path, err := r.shipFile(name)
		if err != nil {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil || r.reservationActive(name, string(data)) {
			continue
		}
		if err := os.Remove(path); err == nil {
			fmt.Printf("ship-names: gc: released stale reservation '%s'\n", name)
			removed++
		}
	}
	fmt.Printf("ship-names: gc: removed %d stale reservation(s)\n", removed)
	return nil
}

// DoFlagship implements `ship-names flagship`.
func (r *Registry) DoFlagship() error {
	fmt.Println(FlagshipName(r.Root))
	return nil
}

// DoShellEnv implements `ship-names shell-env`: print shell code to stdout
// that sets STARFLEET_SHIP_ID, prepends the ship name to PS1, and installs
// an EXIT trap to release the reservation.  Designed to be consumed as:
//
//	eval "$(starfleetctl ship-names shell-env)"
//
// If STARFLEET_SHIP_ID is already set in the caller's environment, the
// existing value is preserved (no reassignment) — matching the original
// comms-auto-id.sh "deliberately does NOT overwrite" semantics.
func (r *Registry) DoShellEnv() error {
	shipID := os.Getenv("STARFLEET_SHIP_ID")
	if shipID == "" {
		var err error
		shipID, err = r.AssignName()
		if err != nil {
			return err
		}
	}

	// Canonical path to this binary for the EXIT trap.
	starfleetctl := filepath.Join(r.Root, ".starfleet-ai", "bin", "starfleetctl")

	fmt.Printf("export STARFLEET_SHIP_ID='%s'\n", shipID)
	fmt.Printf("PS1=\"(%s) ${PS1:-\\$ }\"\n", shipID)
	fmt.Printf("trap '\"%s\" ship-names release \"%s\" >/dev/null 2>&1 || true' EXIT\n",
		starfleetctl, shipID)

	return nil
}
