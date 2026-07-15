// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package shipnames

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// DoAssignFlagship implements `ship-names assign flagship`: reserve
// the flagship name specifically, unlocked (matches bash — only the general
// assignment path takes .assign.lock).
func (r *Registry) DoAssignFlagship() error {
	if err := os.MkdirAll(r.ShipsDir, 0o755); err != nil {
		return err
	}
	path, err := r.shipFile(Flagship)
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("flagship (%s) already reserved", Flagship)
	}
	if err := writeReservation(path); err != nil {
		return err
	}
	fmt.Println(Flagship)
	return nil
}

// DoAssign implements `ship-names assign`: atomically pick the first unused
// name from NamesFile, falling back to "ws-<pid>" if all are taken.
func (r *Registry) DoAssign() error {
	name, err := r.AssignName()
	if err != nil {
		return err
	}
	fmt.Println(name)
	return nil
}

// AssignName picks the first unused name from NamesFile, falling back to
// "ws-<pid>" if all are taken.  Returns the name (not printed), for use
// from the session package.
func (r *Registry) AssignName() (string, error) {
	if err := os.MkdirAll(r.ShipsDir, 0o755); err != nil {
		return "", err
	}
	lh, err := r.assignLock()
	if err != nil {
		return "", err
	}
	defer lh.Close()

	names, err := r.readNames()
	if err != nil {
		return "", err
	}
	for _, name := range names {
		path, err := r.shipFile(name)
		if err != nil {
			continue
		}
		if _, err := os.Stat(path); err != nil {
			if err := writeReservation(path); err != nil {
				return "", err
			}
			return name, nil
		}
	}
	return fmt.Sprintf("ws-%d", os.Getpid()), nil
}

func writeReservation(path string) error {
	content := fmt.Sprintf("%d:%d\n", os.Getpid(), time.Now().Unix())
	return os.WriteFile(path, []byte(content), 0o644)
}

// DoRelease implements `ship-names release <name>`.
func (r *Registry) DoRelease(name string) error {
	if name == "" {
		return fmt.Errorf("release: name required")
	}
	path, err := r.shipFile(name)
	if err != nil {
		return err
	}
	_ = os.Remove(path) // rm -f: missing file is not an error
	return nil
}

// DoList implements `ship-names list`.
func (r *Registry) DoList() error {
	fmt.Printf("Ship name registry (%s):\n", r.ShipsDir)
	fmt.Printf("  %-22s  %s\n", "NAME", "STATUS")
	fmt.Printf("  %-22s  %s\n", "----", "------")

	flagPath, ferr := r.shipFile(Flagship)
	if ferr == nil {
		if _, err := os.Stat(flagPath); err == nil {
			fmt.Printf("  %-22s  ACTIVE (flagship)\n", Flagship)
		} else {
			fmt.Printf("  %-22s  free\n", Flagship)
		}
	} else {
		fmt.Printf("  %-22s  free\n", Flagship)
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
// live agent-bus status entry.
func (r *Registry) DoGC() error {
	entries, err := os.ReadDir(r.ShipsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var names []string
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		if e.Name() == ".assign.lock" {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)

	removed := 0
	for _, name := range names {
		statusFile := filepath.Join(r.StatusDir, name+".tsv")
		if _, err := os.Stat(statusFile); err != nil {
			if path, perr := r.shipFile(name); perr == nil {
				_ = os.Remove(path)
			}
			fmt.Printf("ship-names: gc: released stale reservation '%s'\n", name)
			removed++
		}
	}
	fmt.Printf("ship-names: gc: removed %d stale reservation(s)\n", removed)
	return nil
}

// DoFlagship implements `ship-names flagship`.
func (r *Registry) DoFlagship() error {
	fmt.Println(Flagship)
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
// agent-bus-auto-id.sh "deliberately does NOT overwrite" semantics.
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
