// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package session

import (
	"os/exec"
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
// tr -c 'A-Za-z0-9_-' '_').
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

// sessionExists checks whether a tmux session by this exact name is running.
func sessionExists(name string) bool {
	return exec.Command("tmux", "has-session", "-t", name).Run() == nil
}

// ResolveID resolves an agent ID / handle / tmux session name / unique
// substring to the concrete tmux session name.  Returns empty string on
// failure.
func ResolveID(root, arg string) string {
	// 1) exact tmux session
	if sessionExists(arg) {
		return arg
	}

	// 2) sanitized "mpbt-<arg>"
	prefixed := sessPrefix + tmuxSafe(arg)
	if sessionExists(prefixed) {
		return prefixed
	}

	// 3) board lookup: AGENT_ID or handle matches arg
	bus, err := agentbus.New(root)
	if err != nil {
		return ""
	}
	for _, r := range bus.AllStatusRecords() {
		if r.Agent == arg || r.Handle == arg {
			if r.Handle != "" && sessionExists(r.Handle) {
				return r.Handle
			}
		}
	}

	// 4) unique substring of a running mpbt session
	entries := tmuxMpbtSessions()
	var match string
	for _, s := range entries {
		if strings.Contains(s, arg) {
			if match != "" {
				return "" // ambiguous
			}
			match = s
		}
	}
	return match
}

// ListSessions returns the list of running mpbt- tmux sessions.
func ListSessions() []string {
	return tmuxMpbtSessions()
}

// ListBoard returns all status records from the bus board.
func ListBoard(root string) []agentbus.StatusRecord {
	bus, err := agentbus.New(root)
	if err != nil {
		return nil
	}
	return bus.AllStatusRecords()
}

func tmuxMpbtSessions() []string {
	out, err := exec.Command("tmux", "ls", "-F", "#{session_name}").Output()
	if err != nil {
		return nil
	}
	var matches []string
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if strings.HasPrefix(line, sessPrefix) {
			matches = append(matches, line)
		}
	}
	return matches
}


