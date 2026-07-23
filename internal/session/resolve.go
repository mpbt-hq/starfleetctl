// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package session

import (
	"path/filepath"
	"strings"

	"github.com/metux/starfleetctl/internal/comms"
)

// PipePath returns the canonical termctl pipe path for a ship.
// The path is deterministic: .starfleet-ai/var/ships/<shipID>.pipe
func PipePath(root, shipID string) string {
	return filepath.Join(root, ".starfleet-ai", "var", "ships", shipID+".pipe")
}

// LogPath returns the canonical log path for a ship session.
func LogPath(root, shipID string) string {
	return filepath.Join(root, ".starfleet-ai", "var", "ships", shipID+".log")
}

// shellQuote wraps s in single quotes, escaping embedded single quotes
// with '\” (the standard POSIX-safe quoting pattern for eval).
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// providerFromModel derives a human-readable provider name from a model id.
// opencode model ids are typically "provider/.../model" or "vendor/model";
// the leading path component (before the first '/') is the provider. Falls
// back to "" when the id has no provider prefix.
func providerFromModel(model string) string {
	if i := strings.IndexByte(model, '/'); i >= 0 {
		return model[:i]
	}
	return ""
}

// ListSessions returns the list of running terminals (ship IDs from comms).
func ListSessions(root string) []string {
	bus, err := comms.New(root)
	if err != nil {
		return nil
	}
	var out []string
	seen := make(map[string]bool)
	for _, r := range bus.AllStatusRecords() {
		if !seen[r.Agent] {
			out = append(out, r.Agent)
			seen[r.Agent] = true
		}
	}
	return out
}

// ListBoard returns all status records from the bus board.
func ListBoard(root string) []comms.StatusRecord {
	bus, err := comms.New(root)
	if err != nil {
		return nil
	}
	return bus.AllStatusRecords()
}
