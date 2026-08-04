// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package session

import (
	"os"
	"os/exec"
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

// resolveClientPath locates a client executable (opencode, claude, ...) by
// name. It first consults PATH (exec.LookPath); if that fails it falls back
// to a few well-known user binary directories. This keeps ship launches
// working even when the launching process — e.g. the web daemon spawned from
// cron — inherited a minimal PATH that doesn't include the user's private
// bin dirs. Returns the bare name when nothing better could be found (the
// inner shell will then resolve it via its own PATH as before).
func resolveClientPath(name string) string {
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	home, _ := os.UserHomeDir()
	dirs := []string{
		home + "/.local/bin",
		home + "/.bin",
		"/usr/local/bin",
	}
	for _, d := range dirs {
		if d == "" {
			continue
		}
		p := filepath.Join(d, name)
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return name
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
