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
  inbox                     directives addressed to me or to all, unacked
  ack <id> ["note"]         mark a directive handled
  ask "<q>" [--to <ctrl>] [--timeout <secs>]
  clear                     drop my heartbeat (session end)

Control agent:
  board                     the whole board
  asks                      pending questions addressed to me
  reply <qid> <answer…>     answer a worker's question
  tell <agent> <text…>      queue a directive for one agent
  broadcast <text…>         queue a directive for ALL agents
  msgs                      list all directives with ack status
  events [N]                tail the audit log (default 20)
  prune                     drop stale heartbeats + fully-acked old directives
`

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
	case "inbox":
		cmdErr = b.DoInbox()
	case "ack":
		cmdErr = b.DoAck(arg(args, 1), arg(args, 2))
	case "board", "-l", "--list":
		cmdErr = b.DoBoard()
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
		cmdErr = b.DoAsks()
	case "msgs", "--msgs":
		cmdErr = b.DoMsgs()
	case "events":
		n := 20
		if len(args) > 1 {
			fmt.Sscanf(args[1], "%d", &n)
		}
		cmdErr = b.DoEvents(n)
	case "prune":
		cmdErr = b.DoPrune()
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
