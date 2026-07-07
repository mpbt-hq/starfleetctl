// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package agentbus

import (
	"fmt"
	"os"
)

const usage = `agent-bus <command> [args…]

Worker session:
  status <state> ["note"]   report/refresh my heartbeat
  inbox [--json]            directives addressed to me or to all, unacked
  ack <id> ["note"]         mark a directive handled
  ask "<q>" [--to <ctrl>] [--timeout <secs>]
  clear                     drop my heartbeat (session end)
  touch                     refresh my heartbeat's timestamp only (same
                            state/note as last posted; no-op if I never
                            posted one) — for a periodic auto-refresh loop,
                            not interactive use; see agent-bus-monitor-loop

Control agent:
  board [--json]            the whole board
  asks [--json]             pending questions addressed to me
  reply <qid> <answer…>     answer a worker's question
  tell <agent> <text…>      queue a directive for one agent
  broadcast <text…>         queue a directive for ALL agents
  msgs [--json]             list all directives with ack status
  events [N]                tail the audit log (default 20)
  prune                     drop stale heartbeats + fully-acked old directives

--json on board/inbox/msgs/asks prints a JSON array instead of the
human-formatted table — for scripts/agents, so no grep/awk/cut is needed.

Ecosystem loops (see DASHBOARD.md starfleetctl row and this package's
monitor.go doc comment for history):
  monitor-loop              Watch my inbox, print new/unacked directives.
                            Wired into production Monitor-tool arming as of
                            2026-07-07 (an earlier live-detection bug under
                            the Monitor tool specifically was found, then
                            could no longer be reproduced after re-testing;
                            see monitor.go doc comment).
  fleet-watch               Watch the board for ships joining/restarting.
                            NOT wired into Monitor-tool arming yet — same
                            failure-mode class as monitor-loop's old bug,
                            never separately re-tested.
  watch [interval|--stop]   desktop-notify daemon for new directives (default
                            15s poll; --stop kills the running instance)
`

// hasJSON reports whether --json is present anywhere in the command's
// remaining args (order-independent, e.g. both `board --json` and, if ever
// combined with other flags, `board --json --whatever`).
func hasJSON(args []string) bool {
	for _, a := range args {
		if a == "--json" {
			return true
		}
	}
	return false
}

// Run dispatches an `agent-bus` invocation exactly like the bash script's
// final case statement, given the resolved workspace root. Returns the
// process exit code.
func Run(root string, args []string) int {
	b, err := New(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "agent-bus:", err)
		return 1
	}

	if len(args) == 0 {
		if err := b.DoBoard(); err != nil {
			return reportErr(err)
		}
		return 0
	}

	var cmdErr error
	switch args[0] {
	case "-h", "--help":
		fmt.Print(usage)
		return 0
	case "status":
		state, note := arg(args, 1), arg(args, 2)
		cmdErr = b.DoStatus(state, note)
	case "clear":
		cmdErr = b.DoClear()
	case "touch":
		cmdErr = b.DoTouch()
	case "inbox":
		if hasJSON(args[1:]) {
			cmdErr = b.DoInboxJSON()
		} else {
			cmdErr = b.DoInbox()
		}
	case "ack":
		cmdErr = b.DoAck(arg(args, 1), arg(args, 2))
	case "board", "-l", "--list":
		if hasJSON(args[1:]) {
			cmdErr = b.DoBoardJSON()
		} else {
			cmdErr = b.DoBoard()
		}
	case "tell":
		if len(args) < 3 {
			cmdErr = usageErr("agent-bus: tell needs <agent> <text…>")
			break
		}
		cmdErr = b.DoPost(args[1], args[2:])
	case "broadcast", "--all":
		cmdErr = b.DoPost("all", args[1:])
	case "ask":
		cmdErr = b.DoAsk(args[1:])
	case "reply":
		if len(args) < 3 {
			cmdErr = usageErr("agent-bus: reply needs <qid> <answer…>")
			break
		}
		cmdErr = b.DoReply(args[1], args[2:])
	case "asks":
		if hasJSON(args[1:]) {
			cmdErr = b.DoAsksJSON()
		} else {
			cmdErr = b.DoAsks()
		}
	case "msgs", "--msgs":
		if hasJSON(args[1:]) {
			cmdErr = b.DoMsgsJSON()
		} else {
			cmdErr = b.DoMsgs()
		}
	case "events":
		n := 20
		if len(args) > 1 {
			fmt.Sscanf(args[1], "%d", &n)
		}
		cmdErr = b.DoEvents(n)
	case "prune":
		cmdErr = b.DoPrune()
	case "monitor-loop":
		cmdErr = b.DoMonitorLoop()
	case "fleet-watch":
		cmdErr = b.DoFleetWatch()
	case "watch":
		cmdErr = b.DoWatch(arg(args, 1), arg(args, 1) == "--stop")
	default:
		if len(args[0]) > 1 && args[0][0] == '-' {
			cmdErr = usageErr(fmt.Sprintf("agent-bus: unknown option: %s", args[0]))
		} else {
			cmdErr = usageErr(fmt.Sprintf("agent-bus: unknown command: %s (try --help)", args[0]))
		}
	}
	if cmdErr != nil {
		return reportErr(cmdErr)
	}
	return 0
}

func arg(args []string, i int) string {
	if i < len(args) {
		return args[i]
	}
	return ""
}

func reportErr(err error) int {
	fmt.Fprintln(os.Stderr, err.Error())
	if _, ok := err.(UsageError); ok {
		return 2
	}
	return 1
}
