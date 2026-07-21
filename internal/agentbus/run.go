// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package agentbus

import (
	"fmt"
	"os"
	"strings"
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
  tell <agent> --stdin      read the directive body from stdin (bypasses
                            argv/ARG_MAX size limit for large messages)
  tell <agent> --attach <f> attach file <f> as the body; inline text is a
                            short summary (agent fetches full body via get)
  broadcast <text…>         queue a directive for ALL agents
  broadcast --stdin         read the broadcast body from stdin
  broadcast --attach <f>    broadcast a file as the body (short summary inline)
  get <id> [--out <path>]   print (or write) the attachment payload of <id>
  msgs [--json]             list all directives with ack status
  events [N]                tail the audit log (default 20)
  prune                     drop stale heartbeats + fully-acked old directives
  health [--json] [--loop]  fleet liveness watchdog (reads status/<SHIP>.json;
                            Go port of scripts/fleet-health)
  dispatch --stdin          JSON-RPC entry point for the opencode plugin
                            (reads dispatchRequest, returns dispatchResponse)

Large inline directives (>768 bytes, e.g. a full hard-reset broadcast) are
auto-spilled into an attachment automatically: the inline text becomes a short
"fetch: agent-bus get <id>" pointer and is marked [ATT] in inbox/msgs, so it
can never be truncated by an agent display. Retrieve the full payload with
'agent-bus get <id>'.

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
                            Wired into production Monitor-tool arming
                            (flagship/control session) as of 2026-07-07,
                            after its own vorcheck cleared the same
                            failure-mode class as monitor-loop's old bug.
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
		state, note, patch := parseStatusArgs(args[1:])
		cmdErr = b.DoStatus(state, note, patch)
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
		if len(args) < 2 {
			cmdErr = usageErr("agent-bus: tell needs <agent> [--stdin|--attach <file>|--reply <id>|<text…>]")
			break
		}
		target := args[1]
		words, useStdin, attachPath, replyTo := parsePostFlags(args[2:])
		cmdErr = b.DoPost(target, words, useStdin, attachPath, replyTo)
	case "broadcast", "--all":
		words, useStdin, attachPath, replyTo := parsePostFlags(args[1:])
		cmdErr = b.DoPost("all", words, useStdin, attachPath, replyTo)
	case "get":
		id, out := "", ""
		for i := 1; i < len(args); i++ {
			switch {
			case args[i] == "--out":
				if i+1 < len(args) {
					out = args[i+1]
					i++
				}
			default:
				if id == "" {
					id = args[i]
				}
			}
		}
		cmdErr = b.DoGet(id, out)
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
	case "health":
		if len(args) > 1 && args[1] == "update" {
			cmdErr = b.DoHealthUpdate(args[2:])
		} else {
			cmdErr = b.DoHealth(args[1:])
		}
	case "monitor-seen":
		if len(args) < 2 {
			cmdErr = usageErr("agent-bus monitor-seen: need <subcommand> (mark|check|load|load-all)")
			break
		}
		switch args[1] {
		case "mark":
			cmdErr = b.DoMonitorSeenMark(arg(args, 2))
		case "check":
			cmdErr = b.DoMonitorSeenCheck(arg(args, 2))
		case "load":
			cmdErr = b.DoMonitorSeenLoad()
		case "load-all":
			cmdErr = b.DoMonitorSeenLoadAll()
		default:
			cmdErr = usageErr("agent-bus monitor-seen: unknown subcommand: " + args[1])
		}
	case "error":
		cmdErr = b.DoErrorRun(args[1:])
	case "config":
		cmdErr = b.DoConfig()
	case "dispatch":
		cmdErr = b.DoDispatch(args[1:])
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

// parsePostFlags splits a `tell`/`broadcast` argument vector into the inline
// words (summary/text), a --stdin flag, and an optional --attach <file> path.
// Both flags may be combined; --attach makes the file the directive body while
// the remaining words/stdin form the short inline summary.
func parsePostFlags(args []string) (words []string, useStdin bool, attachPath, replyTo string) {
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--stdin":
			useStdin = true
		case "--attach":
			if i+1 < len(args) {
				attachPath = args[i+1]
				i++
			}
		case "--reply":
			if i+1 < len(args) {
				replyTo = args[i+1]
				i++
			}
		default:
			words = append(words, args[i])
		}
	}
	return
}

func reportErr(err error) int {
	fmt.Fprintln(os.Stderr, err.Error())
	if _, ok := err.(UsageError); ok {
		return 2
	}
	return 1
}

// parseStatusArgs parses `agent-bus status` arguments into the legacy
// (state, note) pair plus an optional structured detail patch. Forms accepted:
//
//	status <state> ["note"]                 # legacy; note is the 2nd positional
//	status <state> --task T [--progress N] [--blocker B] [--eta E] [--branch BR] [--note N]
//
// A leading positional after <state> that is NOT a --flag is treated as the
// legacy note (so old callers keep working); any --flag overrides/extends it.
// Progress defaults to -1 (unspecified) so callers can distinguish "leave
// unchanged" from "set to 0".
func parseStatusArgs(args []string) (state, note string, patch StatusPatch) {
	patch.Progress = -1
	if len(args) == 0 {
		return "", "", patch
	}
	state = args[0]
	rest := args[1:]
	for i := 0; i < len(rest); i++ {
		a := rest[i]
		switch {
		case a == "--task":
			if i+1 < len(rest) {
				patch.Task = rest[i+1]
				i++
			}
		case a == "--progress":
			if i+1 < len(rest) {
				fmt.Sscanf(rest[i+1], "%d", &patch.Progress)
				i++
			}
		case a == "--blocker":
			if i+1 < len(rest) {
				patch.Blocker = rest[i+1]
				i++
			}
		case a == "--eta":
			if i+1 < len(rest) {
				patch.ETA = rest[i+1]
				i++
			}
		case a == "--branch":
			if i+1 < len(rest) {
				patch.Branch = rest[i+1]
				i++
			}
		case a == "--note":
			if i+1 < len(rest) {
				patch.Note = rest[i+1]
				i++
			}
		case a == "--launch-type":
			if i+1 < len(rest) {
				patch.LaunchType = rest[i+1]
				i++
			}
		case a == "--parent":
			if i+1 < len(rest) {
				patch.Parent = rest[i+1]
				i++
			}
		case a == "--provider":
			if i+1 < len(rest) {
				patch.Provider = rest[i+1]
				i++
			}
		case a == "--model":
			if i+1 < len(rest) {
				patch.Model = rest[i+1]
				i++
			}
		case strings.HasPrefix(a, "--"):
			// unknown flag: ignore
		default:
			// first non-flag positional after state = legacy note
			if note == "" {
				note = a
			} else {
				note += " " + a
			}
		}
	}
	return
}
