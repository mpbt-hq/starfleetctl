// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package session

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/metux/starfleetctl/internal/agentbus"
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
// with '\'' (the standard POSIX-safe quoting pattern for eval).
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

const sessPrefix = "mpbt-"

// ResolveID resolves an agent ID / handle / unique substring to the concrete
// termctl pipe path. Returns empty string on failure.
func ResolveID(root, arg string) string {
	// 1) Direct lookup by ship ID — pipe path is deterministic
	if arg != "" {
		pipe := PipePath(root, arg)
		if _, err := os.Stat(pipe); err == nil {
			return pipe
		}
	}

	// 2) Check agent-bus board for matching agent/handle
	bus, err := agentbus.New(root)
	if err != nil {
		return ""
	}
	for _, r := range bus.AllStatusRecords() {
		if r.Agent == arg || r.Handle == arg {
			return PipePath(root, r.Agent)
		}
	}

	// 3) Unique substring of a known ship name from agent-bus board
	var match string
	seen := make(map[string]bool)
	for _, r := range bus.AllStatusRecords() {
		if !seen[r.Agent] && strings.Contains(r.Agent, arg) {
			if match != "" {
				return "" // ambiguous
			}
			match = r.Agent
			seen[r.Agent] = true
		}
	}
	if match != "" {
		return PipePath(root, match)
	}
	return ""
}

// ListSessions returns the list of running terminals (ship IDs from agent-bus).
func ListSessions(root string) []string {
	bus, err := agentbus.New(root)
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
func ListBoard(root string) []agentbus.StatusRecord {
	bus, err := agentbus.New(root)
	if err != nil {
		return nil
	}
	return bus.AllStatusRecords()
}