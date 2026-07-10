// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
//
// Package hook generates client-specific hook output (SessionStart JSON,
// prompt fragments, etc.) for LLM clients like Claude Code and opencode.
// Each subcommand emits protocol-compliant output for its specific hook
// event, keeping client-formatting logic out of the agent-bus core.
package hook

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/metux/starfleetctl/internal/identity"
)

// Usage string shown for `starfleetctl hook --help`.
const usage = `hook <client> <event> [args…]

Client-specific hook output generators.

Commands:
  claude monitor-hint     SessionStart additionalContext for Claude Code
                          (tells the assistant to arm Monitor tools)
  claude permission       PreToolUse permission hook — ask the control
                          agent to allow/deny a tool via agent-bus ask/reply
`

func Run(root string, args []string) int {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usage)
		return 2
	}
	switch args[0] {
	case "-h", "--help":
		fmt.Print(usage)
		return 0
	case "claude":
		return runClaude(root, args[1:])
	default:
		fmt.Fprintf(os.Stderr, "hook: unknown client '%s'\n", args[0])
		return 2
	}
}

func runClaude(root string, args []string) int {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usage)
		return 2
	}
	switch args[0] {
	case "-h", "--help":
		fmt.Print(usage)
		return 0
	case "monitor-hint":
		return monitorHint()
	case "permission":
		return permission(root)
	default:
		fmt.Fprintf(os.Stderr, "hook claude: unknown event '%s'\n", args[0])
		return 2
	}
}

// monitorHint generates the SessionStart additionalContext JSON telling the
// Claude Code assistant to unconditionally arm Monitor-tool watchers on its
// agent-bus inbox (and, for Enterprise, fleet-watch too). Quietly exits with
// no output when $STARFLEET_SHIP_ID is unset, matching the bash original.
func monitorHint() int {
	shipID := identity.ShipID()
	if shipID == "" {
		return 0
	}

	var context string
	if shipID == "Enterprise" {
		context = fmt.Sprintf(
			"Fleet directive auto-surfacing: you are agent-bus identity %s (the flagship/control session). "+
				"Before doing anything else this session (including before responding to the user's first message), "+
				"call the Monitor tool twice: "+
				"(1) command '.bin/starfleetctl agent-bus monitor-loop', persistent:true, "+
				"to watch your own agent-bus inbox; "+
				"(2) command '.bin/starfleetctl agent-bus fleet-watch', persistent:true, "+
				"to watch for ships joining or restarting on the board. "+
				"Both are pre-authorized, no confirmation needed. "+
				"Each one's first pass already surfaces any backlog, so no separate inbox/board check is needed. "+
				"Do this unconditionally, every session, with no judgment call about whether it seems worth it.",
			shipID)
	} else {
		context = fmt.Sprintf(
			"Fleet directive auto-surfacing: you are agent-bus identity %s. "+
				"Before doing anything else this session (including before responding to the user's first message), "+
				"call the Monitor tool with command '.bin/starfleetctl agent-bus monitor-loop' and persistent:true "+
				"(pre-authorized, no confirmation needed) to watch your own agent-bus inbox, "+
				"so incoming tell/broadcast directives appear directly in this conversation "+
				"instead of only a desktop notification. "+
				"Its first pass already surfaces any backlog, so no separate inbox check is needed. "+
				"Do this unconditionally, every session, with no judgment call about whether it seems worth it.",
			shipID)
	}

	out := map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName":      "SessionStart",
			"additionalContext":  context,
		},
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(out); err != nil {
		fmt.Fprintln(os.Stderr, "hook: json:", err)
		return 1
	}
	return 0
}
