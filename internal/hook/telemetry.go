// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
//
// Permission-confirmation telemetry hooks. These are PURE TELEMETRY: they
// reimplement the permission.allow matching logic the runtime itself uses
// and, when a Bash sub-command would NOT be auto-allowed (i.e. would need
// an interactive confirmation), append one JSON line to the fleet-shared
// tooling-gaps log. They NEVER influence the real permission decision:
// they always exit 0 with EMPTY stdout (no hookSpecificOutput), so the
// runtime evaluates deny/ask/allow exactly as if this hook did not exist.
//
// Two runtime adapters share the same classification core:
//   - claude   : Claude Code PreToolUse payload (tool_name / tool_input)
//   - opencode : opencode native PreToolUse payload (tool / args); also
//     tolerant of the claude-hooks shim format, since both are
//     parsed defensively.
package hook

import (
	"encoding/json"
	"os"

	"github.com/metux/starfleetctl/internal/identity"
	"github.com/metux/starfleetctl/internal/telemetry"
)

// rawHookPayload captures the union of fields we might receive from
// Claude Code, opencode-native, or the opencode claude-hooks shim. Only
// the ones we need are decoded; everything else is ignored.
type rawHookPayload struct {
	SessionID string         `json:"session_id"`
	Cwd       string         `json:"cwd"`
	Mode      string         `json:"permission_mode"`
	ToolName  string         `json:"tool_name"`
	Tool      string         `json:"tool"`
	Agent     string         `json:"agent"`
	HookEvent string         `json:"hook_event_name"`
	ToolInput map[string]any `json:"tool_input"`
	Args      map[string]any `json:"args"`
}

// normalizeHookPayload extracts the effective tool name and command from a
// runtime PreToolUse payload, tolerating both Claude Code
// (tool_name/tool_input.command) and opencode (tool/args.command) shapes.
func normalizeHookPayload(p *rawHookPayload) (tool, command string) {
	tool = p.ToolName
	if tool == "" {
		tool = p.Tool
	}
	if tool == "" {
		tool = p.Agent // some opencode variants nest agent oddly; ignore
	}
	var m map[string]any
	if p.ToolInput != nil {
		m = p.ToolInput
	} else if p.Args != nil {
		m = p.Args
	}
	if m != nil {
		if c, ok := m["command"].(string); ok {
			command = c
		}
	}
	return tool, command
}

// telemetryCore classifies a single parsed PreToolUse payload and appends
// a telemetry event if the command would have needed a prompt. It never
// returns a non-zero status and never writes to stdout — pure observer.
func telemetryCore(root string, p *rawHookPayload) {
	tool, command := normalizeHookPayload(p)
	if tool == "" {
		return
	}
	// Only Bash calls carry a meaningful permission.allow story; other
	// tools are out of scope for this telemetry.
	if tool != "Bash" && tool != "bash" {
		return
	}
	if command == "" {
		return
	}
	mode := p.Mode
	if mode == "" {
		mode = "default"
	}

	rules := telemetry.CollectAllowRules(root)
	for _, raw := range telemetry.SplitCompound(command) {
		sub := telemetry.StripWrappers(raw)
		cat := telemetry.Classify(sub, rules)
		if cat == "" {
			continue // auto-allowed, nothing to log
		}
		e := telemetry.NewEvent(
			identity.ShipID(),
			p.SessionID,
			p.Cwd,
			mode,
			command,
			sub,
			cat,
		)
		// Observer only: swallow any logging error so a broken log
		// path can never affect the real tool call.
		_ = telemetry.Append(root, e)
	}
}

// claudeTelemetry implements `starfleetctl hook claude telemetry` — a
// Claude Code PreToolUse hook that records permission-confirmation
// telemetry without affecting the decision.
func claudeTelemetry(root string) int {
	var p rawHookPayload
	if err := json.NewDecoder(os.Stdin).Decode(&p); err != nil {
		return 0
	}
	telemetryCore(root, &p)
	return 0
}

// opencodeTelemetry implements `starfleetctl hook opencode telemetry` — the
// opencode-native equivalent. Payload shape differs (tool/args vs
// tool_name/tool_input) but telemetryCore normalizes both.
func opencodeTelemetry(root string) int {
	var p rawHookPayload
	if err := json.NewDecoder(os.Stdin).Decode(&p); err != nil {
		return 0
	}
	telemetryCore(root, &p)
	return 0
}
