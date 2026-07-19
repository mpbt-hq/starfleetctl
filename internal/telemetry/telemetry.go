// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
//
// Package telemetry collects and aggregates permission-confirmation
// telemetry across the fleet. A runtime-specific PreToolUse hook (Claude
// Code or opencode) feeds observed tool calls here; this package decides
// whether each call would have needed an interactive permission prompt
// (i.e. did not match the allow rules) and appends a neutral JSONL event
// to a fleet-shared, gitignored log. A later `starfleetctl telemetry
// report` pass mines that log for recurring unmatched command prefixes
// worth turning into a scripts/* / starfleetctl wrapper + allowlist entry.
//
// HARD REQUIREMENT: collection never influences the real permission
// decision. The hook adapters always exit 0 with empty stdout (no
// hookSpecificOutput at all). See internal/hook/telemetry.go.
package telemetry

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/metux/starfleetctl/internal/config"
)

// Event is one observed tool call that did not auto-match an allow rule.
type Event struct {
	TS             int64  `json:"ts"`
	Source         string `json:"source"`          // "hook" (passive) | "escalation" (active, Pegasus m0051+)
	AgentID        string `json:"agent_id"`        // STARFLEET_SHIP_ID
	SessionID      string `json:"session_id"`      //
	Cwd            string `json:"cwd"`             //
	PermissionMode string `json:"permission_mode"` // default|plan|acceptEdits|auto|dontAsk
	FullCommand    string `json:"full_command"`    //
	Subcommand     string `json:"subcommand"`      // normalized (wrappers stripped) leading sub-command
	Category       string `json:"category"`        // no-match|ask-match|deny-match
}

// LogDir / LogFile live under the fleet-shared _WORK_ tree, deliberately
// sharing the collection point with Pegasus's m0051 escalation reporting
// (same directory; the Source field distinguishes passive hook capture
// from active agent-bus tell capture).
const (
	logSubdir = "agent-bus" // relative to _WORK_
	logDir    = "tooling-gaps"
	logName   = "events.jsonl"
)

// LogPath returns the absolute path of the events file for a given
// workspace root.
func LogPath(root string) string {
	return filepath.Join(config.BusDir(root), logDir, logName)
}

// Append writes one event to the shared log, serialized with an exclusive
// flock so concurrent hook invocations across ships cannot interleave or
// truncate each other's lines.
func Append(root string, e Event) error {
	dir := filepath.Join(config.BusDir(root), logDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	lockPath := filepath.Join(dir, ".lock")
	logPath := filepath.Join(dir, logName)

	lockf, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	defer lockf.Close()
	if err := syscall.Flock(int(lockf.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lockf.Fd()), syscall.LOCK_UN)

	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()

	// Disable HTML escaping so command text (e.g. "&&") is written literally,
	// matching the original Python logger and keeping the log human-readable.
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(e); err != nil {
		return err
	}
	_, err = f.Write(buf.Bytes())
	return err
}

// Rules is the union of permissions.allow|deny|ask across the project +
// local + user settings layers.
type Rules struct {
	Allow []string
	Deny  []string
	Ask   []string
}

// CollectAllowRules mirrors the layering Claude Code itself applies: union
// of permissions across project + local + user settings. For opencode we
// read the SAME .claude settings (the fleet's canonical permission source
// — the allowlist triage broadens these), since that is what ultimately
// decides whether a Bash call auto-passes.
func CollectAllowRules(projectDir string) Rules {
	var r Rules
	for _, p := range []string{
		filepath.Join(projectDir, ".claude", "settings.json"),
		filepath.Join(projectDir, ".claude", "settings.local.json"),
		filepath.Join(os.ExpandEnv("$HOME"), ".claude", "settings.json"),
	} {
		r.mergeFile(p)
	}
	return r
}

func (r *Rules) mergeFile(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var doc struct {
		Permissions struct {
			Allow []string `json:"allow"`
			Deny  []string `json:"deny"`
			Ask   []string `json:"ask"`
		} `json:"permissions"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return
	}
	r.Allow = append(r.Allow, doc.Permissions.Allow...)
	r.Deny = append(r.Deny, doc.Permissions.Deny...)
	r.Ask = append(r.Ask, doc.Permissions.Ask...)
}

// ruleMatches reproduces Claude Code's literal-prefix Bash(...) matching
// against a single normalized sub-command:
//
//	"X:*"   / "X *"  -> sub == X || sub starts with "X "
//	"X*"               -> sub starts with X
//	"X"                -> sub == X
func ruleMatches(pattern, sub string) bool {
	switch {
	case strings.HasSuffix(pattern, ":*"), strings.HasSuffix(pattern, " *"):
		base := pattern[:len(pattern)-2]
		return sub == base || strings.HasPrefix(sub, base+" ")
	case strings.HasSuffix(pattern, "*"):
		return strings.HasPrefix(sub, pattern[:len(pattern)-1])
	default:
		return sub == pattern
	}
}

// bashPatterns extracts the inner pattern from "Bash(...)" rules.
func bashPatterns(rules []string) []string {
	out := make([]string, 0, len(rules))
	for _, r := range rules {
		if strings.HasPrefix(r, "Bash(") && strings.HasSuffix(r, ")") {
			out = append(out, r[len("Bash("):len(r)-1])
		}
	}
	return out
}

// Classify returns the telemetry category for a normalized sub-command:
// "deny-match", "ask-match", or "no-match" (allow matches are reported as
// empty string — nothing worth logging).
func Classify(sub string, r Rules) string {
	allow := bashPatterns(r.Allow)
	deny := bashPatterns(r.Deny)
	ask := bashPatterns(r.Ask)
	for _, p := range deny {
		if ruleMatches(p, sub) {
			return "deny-match"
		}
	}
	for _, p := range ask {
		if ruleMatches(p, sub) {
			return "ask-match"
		}
	}
	for _, p := range allow {
		if ruleMatches(p, sub) {
			return "" // auto-allowed, nothing to log
		}
	}
	return "no-match"
}

// SplitCompound is a quote-aware split on shell control operators
// (&& || |& ; | & newline). FD-redirect forms (2>&1, >&2, <&3) are NOT
// split at the bare & — a & immediately after > or < is a redirect, not
// job control.
func SplitCompound(command string) []string {
	var parts []string
	var buf strings.Builder
	n := len(command)
	quote := byte(0)
	i := 0
	for i < n {
		c := command[i]
		if quote != 0 {
			buf.WriteByte(c)
			if c == quote {
				quote = 0
			}
			i++
			continue
		}
		if c == '\'' || c == '"' {
			quote = c
			buf.WriteByte(c)
			i++
			continue
		}
		matched := ""
		for _, op := range []string{"&&", "||", "|&"} {
			if strings.HasPrefix(command[i:], op) {
				matched = op
				break
			}
		}
		if matched == "" && (c == ';' || c == '|' || c == '&' || c == '\n') {
			var prev byte
			if buf.Len() > 0 {
				prev = buf.String()[buf.Len()-1]
			}
			if !(c == '&' && (prev == '>' || prev == '<')) {
				matched = string(c)
			}
		}
		if matched != "" {
			if s := strings.TrimSpace(buf.String()); s != "" {
				parts = append(parts, s)
			}
			buf.Reset()
			i += len(matched)
			continue
		}
		buf.WriteByte(c)
		i++
	}
	if s := strings.TrimSpace(buf.String()); s != "" {
		parts = append(parts, s)
	}
	return parts
}

// wrapperStrip lists process-wrapper tokens Claude Code strips before
// matching (see its documented strip list).
var wrapperStrip = []string{"timeout", "time", "nice", "nohup", "stdbuf"}

// StripWrappers repeatedly removes leading process-wrapper tokens before
// matching, mirroring Claude Code's own pre-match normalization.
func StripWrappers(sub string) string {
	tokens := strings.Fields(sub)
	changed := true
	for changed && len(tokens) > 0 {
		changed = false
		head := tokens[0]
		isWrap := false
		for _, w := range wrapperStrip {
			if head == w {
				isWrap = true
				break
			}
		}
		if isWrap {
			tokens = tokens[1:]
			for len(tokens) > 0 && strings.HasPrefix(tokens[0], "-") {
				tokens = tokens[1:]
			}
			if head == "timeout" && len(tokens) > 0 && !strings.HasPrefix(tokens[0], "-") {
				tokens = tokens[1:]
			}
			changed = true
		} else if head == "xargs" && len(tokens) > 1 && !strings.HasPrefix(tokens[1], "-") {
			tokens = tokens[1:]
			changed = true
		}
	}
	return strings.Join(tokens, " ")
}

// NewEvent builds a telemetry Event for an observed sub-command. It
// returns (event, category) where category == "" means the call was
// auto-allowed and need not be logged.
func NewEvent(agentID, sessionID, cwd, mode, fullCommand, sub string, category string) Event {
	return Event{
		TS:             time.Now().Unix(),
		Source:         "hook",
		AgentID:        agentID,
		SessionID:      sessionID,
		Cwd:            cwd,
		PermissionMode: mode,
		FullCommand:    fullCommand,
		Subcommand:     sub,
		Category:       category,
	}
}

// FormatEvent is a tiny helper used by the aggregator/report for stable
// string rendering; kept here so the event shape stays the single source
// of truth.
func (e Event) String() string {
	return fmt.Sprintf("[%s] %s %s", e.Category, e.AgentID, e.Subcommand)
}
