// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package hook

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/metux/starfleetctl/internal/agentbus"
	"github.com/metux/starfleetctl/internal/identity"
)

// permission implements `starfleetctl hook claude permission`.
//
// It is a Claude Code PreToolUse hook that routes tool-permission decisions
// to the central control agent via agent-bus ask/reply, instead of prompting
// the local session.  Reads the PreToolUse JSON from stdin, asks the
// controller, BLOCKS for the answer, and emits a permissionDecision JSON to
// stdout (allow/deny/ask).
//
// Fail-safe: Claude Code's own hook timeout FAILS OPEN (the tool proceeds),
// so we enforce our own timeout and return a decision first:
//
//	$AGENT_PERM_TIMEOUT           seconds to wait (default 60; keep it <
//	                              the hook's Claude Code "timeout" setting)
//	$AGENT_PERM_TIMEOUT_DECISION  decision on timeout (default deny)
func permission(root string) int {
	shipID := identity.ShipID()
	if shipID == "" {
		emitPermission("ask", "STARFLEET_SHIP_ID not set — cannot route to control agent")
		return 0
	}

	timeout := int64(60)
	if v := os.Getenv("AGENT_PERM_TIMEOUT"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			timeout = n
		}
	}
	onTimeout := os.Getenv("AGENT_PERM_TIMEOUT_DECISION")
	if onTimeout == "" {
		onTimeout = "deny"
	}

	var payload struct {
		ToolName  string         `json:"tool_name"`
		ShipID    string         `json:"ship_id"`
		ToolInput map[string]any `json:"tool_input"`
	}
	if err := json.NewDecoder(os.Stdin).Decode(&payload); err != nil {
		emitPermission("ask", fmt.Sprintf("cannot parse PreToolUse JSON: %v", err))
		return 0
	}

	summary := ""
	if payload.ToolInput != nil {
		if cmd, ok := payload.ToolInput["command"].(string); ok {
			summary = cmd
		} else if fp, ok := payload.ToolInput["file_path"].(string); ok {
			summary = fp
		} else {
			b, _ := json.Marshal(payload.ToolInput)
			s := string(b)
			if len(s) > 200 {
				s = s[:200]
			}
			summary = s
		}
	}

	tool := payload.ToolName
	agent := payload.ShipID
	if agent == "" {
		agent = "?"
	}
	question := fmt.Sprintf("[perm] allow %s: %s — for %s?", tool, summary, agent)

	bus, err := agentbus.New(root)
	if err != nil {
		emitPermission(onTimeout, fmt.Sprintf("agent-bus init: %v", err))
		return 0
	}

	ctrl := agentbus.Controller()
	ans, err := bus.AskAndWait(question, ctrl, timeout)
	if err != nil {
		emitPermission(onTimeout, fmt.Sprintf("control agent did not answer within %ds (fail-%s)", timeout, onTimeout))
		return 0
	}

	switch strings.ToLower(strings.TrimSpace(ans)) {
	case "allow", "yes", "y", "approve", "ok", "1":
		emitPermission("allow", "approved by control agent")
	case "deny", "no", "n", "block", "0":
		emitPermission("deny", "denied by control agent")
	default:
		emitPermission(onTimeout, "unrecognised control reply: "+ans)
	}
	return 0
}

func emitPermission(decision, reason string) {
	out := map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName":            "PreToolUse",
			"permissionDecision":       decision,
			"permissionDecisionReason": reason,
		},
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(out)
}
