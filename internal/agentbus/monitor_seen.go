// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package agentbus

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DoMonitorSeenMark implements `agent-bus monitor-seen mark <id>` —
// appends a message ID to the per-ship seen file.
func (b *Bus) DoMonitorSeenMark(id string) error {
	if id == "" {
		return usageErr("agent-bus monitor-seen mark needs <id>")
	}
	seenDir := filepath.Join(b.BusDir, "monitor-seen")
	if err := os.MkdirAll(seenDir, 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(seenDir, fsafe(b.ShipID)),
		os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintln(f, id)
	return err
}

// DoMonitorSeenCheck implements `agent-bus monitor-seen check <id>` —
// exits 0 if the ID is in the seen file, 1 if not.
func (b *Bus) DoMonitorSeenCheck(id string) error {
	if id == "" {
		return usageErr("agent-bus monitor-seen check needs <id>")
	}
	seen, err := b.loadSeen(b.ShipID)
	if err != nil {
		return err
	}
	if seen[id] {
		return nil // exit 0
	}
	os.Exit(1)
	return nil
}

// DoMonitorSeenLoadAll implements `agent-bus monitor-seen load-all` —
// prints all seen IDs across all ships (one per line).
func (b *Bus) DoMonitorSeenLoadAll() error {
	seen, err := b.loadSeenAll()
	if err != nil {
		return err
	}
	for id := range seen {
		fmt.Println(id)
	}
	return nil
}

// loadSeen returns the set of seen IDs for a specific ship.
func (b *Bus) loadSeen(shipID string) (map[string]bool, error) {
	seen := make(map[string]bool)
	fpath := filepath.Join(b.BusDir, "monitor-seen", fsafe(shipID))
	data, err := os.ReadFile(fpath)
	if err != nil {
		if os.IsNotExist(err) {
			return seen, nil
		}
		return nil, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			seen[line] = true
		}
	}
	return seen, nil
}

// loadSeenAll returns the union of seen IDs across all ships.
func (b *Bus) loadSeenAll() (map[string]bool, error) {
	seen := make(map[string]bool)
	dir := filepath.Join(b.BusDir, "monitor-seen")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return seen, nil
		}
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line != "" {
				seen[line] = true
			}
		}
	}
	return seen, nil
}
