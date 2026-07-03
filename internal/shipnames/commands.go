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
// Enterprise specifically, unlocked (matches bash — only the general
// assignment path takes .assign.lock).
func (r *Registry) DoAssignFlagship() error {
	if err := os.MkdirAll(r.ShipsDir, 0o755); err != nil {
		return err
	}
	path := r.shipFile(Flagship)
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
	if err := os.MkdirAll(r.ShipsDir, 0o755); err != nil {
		return err
	}
	lh, err := r.assignLock()
	if err != nil {
		return err
	}
	defer lh.Close()

	names, err := r.readNames()
	if err != nil {
		return err
	}
	for _, name := range names {
		path := r.shipFile(name)
		if _, err := os.Stat(path); err != nil {
			if err := writeReservation(path); err != nil {
				return err
			}
			fmt.Println(name)
			return nil
		}
	}
	fmt.Printf("ws-%d\n", os.Getpid())
	return nil
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
	_ = os.Remove(r.shipFile(name)) // rm -f: missing file is not an error
	return nil
}

// DoList implements `ship-names list`.
func (r *Registry) DoList() error {
	fmt.Printf("Ship name registry (%s):\n", r.ShipsDir)
	fmt.Printf("  %-22s  %s\n", "NAME", "STATUS")
	fmt.Printf("  %-22s  %s\n", "----", "------")

	if _, err := os.Stat(r.shipFile(Flagship)); err == nil {
		fmt.Printf("  %-22s  ACTIVE (flagship)\n", Flagship)
	} else {
		fmt.Printf("  %-22s  free\n", Flagship)
	}

	names, err := r.readNames()
	if err != nil {
		return err
	}
	for _, name := range names {
		path := r.shipFile(name)
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
			_ = os.Remove(r.shipFile(name))
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
