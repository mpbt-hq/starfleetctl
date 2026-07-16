// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package session

import (
	"strings"

	"github.com/metux/starfleetctl/internal/agentbus"
)

// shellQuote wraps s in single quotes, escaping embedded single quotes
// with '\'' (the standard POSIX-safe quoting pattern for eval).
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

const sessPrefix = "mpbt-"

// tmuxSafe sanitizes a string to tmux-safe characters (same as bash's
// tr -c 'A-Za-z0-9_-' '_'). Kept for backward compatibility in name generation.
func tmuxSafe(s string) string {
	b := make([]byte, len(s))
	for i, c := range []byte(s) {
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') ||
			(c >= '0' && c <= '9') || c == '_' || c == '-' {
			b[i] = c
		} else {
			b[i] = '_'
		}
	}
	return string(b)
}

// ResolveID resolves an agent ID / handle / unique substring to the concrete
// termctl pipe path. Returns empty string on failure.
func ResolveID(root, arg string) string {
	reg := NewRegistry(root)

	// 1) Direct registry lookup by ship ID
	if pipe, ok := reg.Get(arg); ok {
		return pipe
	}

	// 2) Check agent-bus board for matching agent/handle
	bus, err := agentbus.New(root)
	if err != nil {
		return ""
	}
	for _, r := range bus.AllStatusRecords() {
		if r.Agent == arg || r.Handle == arg {
			if pipe, ok := reg.Get(r.Agent); ok {
				return pipe
			}
		}
	}

	// 3) Unique substring of a registered ship ID
	entries := reg.List()
	var match string
	for _, s := range entries {
		if strings.Contains(s, arg) {
			if match != "" {
				return "" // ambiguous
			}
			match = s
		}
	}
	if match != "" {
		if pipe, ok := reg.Get(match); ok {
			return pipe
		}
	}
	return ""
}

// ListSessions returns the list of running terminals (ship IDs from registry).
func ListSessions(root string) []string {
	reg := NewRegistry(root)
	return reg.List()
}

// ListBoard returns all status records from the bus board.
func ListBoard(root string) []agentbus.StatusRecord {
	bus, err := agentbus.New(root)
	if err != nil {
		return nil
	}
	return bus.AllStatusRecords()
}