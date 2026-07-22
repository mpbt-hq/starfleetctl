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