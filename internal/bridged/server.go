// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package bridged

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/metux/starfleetctl/internal/agentbus"
	"github.com/metux/starfleetctl/internal/dashboard"
)

// allowedAgentBusSubcommands is a deliberate ALLOWLIST, not a blocklist.
// agentbus.Run() also dispatches "ask" (blocks polling for a reply, up to
// --timeout, and calls os.Exit(3) directly on timeout — which would kill
// this whole daemon, not just fail one request) and "monitor-loop" /
// "fleet-watch" / "watch" (each an intentionally infinite polling loop that
// never returns). Any of those reaching the shared dispatch path would wedge
// or kill the daemon for every connected client, not just the one that
// asked for it. An allowlist means a future agentbus subcommand that turns
// out to block is safe-by-default (rejected) until explicitly reviewed and
// added here, rather than silently reachable through the daemon.
var allowedAgentBusSubcommands = map[string]bool{
	"":          true, // no args -> DoBoard default, same as the CLI
	"-h":        true,
	"--help":    true,
	"status":    true,
	"clear":     true,
	"inbox":     true,
	"ack":       true,
	"board":     true,
	"-l":        true,
	"--list":    true,
	"tell":      true,
	"broadcast": true,
	"--all":     true,
	"reply":     true,
	"asks":      true,
	"msgs":      true,
	"--msgs":    true,
	"events":    true,
	"prune":     true,
}

// execMu serializes command *execution* (not connection acceptance) across
// all connections. This exists solely to make the os.Stdout/os.Stderr
// swap-and-capture trick below safe under concurrency — agentbus.Run and
// dashboard.Run are CLI-shaped (print to the process-wide stdout/stderr,
// return an int exit code) rather than writer-injectable, so capturing one
// call's output means temporarily redirecting the real os.Stdout/os.Stderr
// for that call's duration; without serializing, two concurrent requests
// would corrupt each other's captured output. File-level correctness was
// never resting on this mutex — agentbus/dashboard already serialize their
// real mutations via flock independently of this.
var execMu sync.Mutex

// dispatch runs one request against the given root and returns the
// response fields. Never lets a panic or an unexpected os.Exit from deeper
// code take the whole daemon down for an unrelated connection would be
// ideal, but os.Exit specifically cannot be intercepted in Go (no
// defer/recover reaches it) — which is exactly why the allowlist above
// exists: it keeps every path that's actually allowed to run here provably
// free of os.Exit and unbounded blocking, rather than trying to sandbox
// around it after the fact.
func dispatch(root string, req Request) Response {
	switch req.Cmd {
	case "ping":
		return Response{ExitCode: 0, Stdout: "pong\n"}
	case "agent-bus":
		sub := ""
		if len(req.Args) > 0 {
			sub = req.Args[0]
		}
		if !allowedAgentBusSubcommands[sub] {
			return Response{ExitCode: 2, Stderr: fmt.Sprintf(
				"bridged: agent-bus subcommand %q is not available via the daemon "+
					"(blocking/long-running or process-exiting) — use the CLI directly\n", sub)}
		}
		code, stdout, stderr := runCaptured(func() int { return agentbus.Run(root, req.Args) })
		return Response{ExitCode: code, Stdout: stdout, Stderr: stderr}
	case "dashboard":
		code, stdout, stderr := runCaptured(func() int { return dashboard.Run(root, req.Args) })
		return Response{ExitCode: code, Stdout: stdout, Stderr: stderr}
	default:
		return Response{ExitCode: 2, Stderr: fmt.Sprintf("bridged: unknown cmd %q (want \"agent-bus\" or \"dashboard\")\n", req.Cmd)}
	}
}

// runCaptured calls fn with the process's real os.Stdout/os.Stderr
// temporarily redirected to pipes, capturing everything fn prints, and
// restores them before returning — the standard technique for capturing
// output from an in-process function that was written to print directly
// rather than accept a writer. Held under execMu by every caller.
func runCaptured(fn func() int) (exitCode int, stdout, stderr string) {
	execMu.Lock()
	defer execMu.Unlock()

	origStdout, origStderr := os.Stdout, os.Stderr
	outR, outW, err := os.Pipe()
	if err != nil {
		return 1, "", "bridged: internal error creating stdout pipe\n"
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		outR.Close()
		outW.Close()
		return 1, "", "bridged: internal error creating stderr pipe\n"
	}
	os.Stdout, os.Stderr = outW, errW

	var outBuf, errBuf bytes.Buffer
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); io.Copy(&outBuf, outR) }()
	go func() { defer wg.Done(); io.Copy(&errBuf, errR) }()

	exitCode = fn()

	outW.Close()
	errW.Close()
	os.Stdout, os.Stderr = origStdout, origStderr
	wg.Wait()
	outR.Close()
	errR.Close()

	return exitCode, outBuf.String(), errBuf.String()
}

// checkNotAlreadyRunning probes sockPath the same way EnsureAgentClone's
// unix_socket_is_live() probes a stale PR-branch listener path: a
// successful connect means a live daemon already owns this socket (refuse
// to start a second one); a failed connect (ECONNREFUSED against a stale
// file, or ENOENT) means it's safe to unlink-and-bind-fresh.
func checkNotAlreadyRunning(sockPath string) error {
	conn, err := net.DialTimeout("unix", sockPath, 500*time.Millisecond)
	if err == nil {
		conn.Close()
		return fmt.Errorf("already running (socket %s is live)", sockPath)
	}
	return nil
}

// maxSockPathLen is the portable-safe ceiling for a Unix domain socket
// path: struct sockaddr_un's sun_path is 108 bytes on Linux (less on some
// BSDs), including the NUL terminator. Bind/connect fail with the cryptic
// "invalid argument" past this limit — found by hitting it for real with a
// deeply-nested test scratch path (_WORK_/agent-bus/bridged.sock itself is
// nowhere near this in normal use, but a worktree/scratch path easily can
// be), so both the daemon and the client check it upfront with an actual
// diagnostic instead of surfacing that raw syscall error.
const maxSockPathLen = 100

func validateSockPath(sockPath string) error {
	if len(sockPath) > maxSockPathLen {
		return fmt.Errorf("socket path too long for a Unix domain socket (%d bytes, limit ~%d): %s — pass a shorter --socket path",
			len(sockPath), maxSockPathLen, sockPath)
	}
	return nil
}

// ListenAndServe binds sockPath and serves connections until a SIGINT/
// SIGTERM is received, at which point it closes the listener and removes
// the socket file (clean-shutdown case). An unclean exit (SIGKILL, crash)
// leaves the socket file behind exactly like any stale-Unix-socket
// scenario — self-healing on the next ListenAndServe call via
// checkNotAlreadyRunning above, so no separate cleanup step is needed.
func ListenAndServe(root, sockPath string) error {
	if err := validateSockPath(sockPath); err != nil {
		return err
	}
	if err := checkNotAlreadyRunning(sockPath); err != nil {
		return err
	}
	_ = os.Remove(sockPath) // best-effort: clear a confirmed-stale file

	l, err := net.Listen("unix", sockPath)
	if err != nil {
		return err
	}
	if err := os.Chmod(sockPath, 0o600); err != nil {
		l.Close()
		os.Remove(sockPath)
		return err
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	shuttingDown := make(chan struct{})
	go func() {
		<-sigCh
		signal.Stop(sigCh)
		close(shuttingDown)
		l.Close()
		os.Remove(sockPath)
	}()

	fmt.Printf("bridged: listening on %s\n", sockPath)
	for {
		conn, err := l.Accept()
		if err != nil {
			select {
			case <-shuttingDown:
				return nil // expected: Accept unblocked by our own l.Close()
			default:
				return err
			}
		}
		go handleConn(root, conn)
	}
}

// handleConn serves exactly one request/response per connection (matching
// the existing one-shot-process-per-CLI-call model — a drop-in alternative
// transport for the same operations, not a new streaming/session
// protocol), then closes it.
func handleConn(root string, conn net.Conn) {
	defer conn.Close()

	if uc, ok := conn.(*net.UnixConn); ok {
		if allowed, err := peerUIDMatchesSelf(uc); err != nil || !allowed {
			_ = writeFrame(conn, Response{ExitCode: 1, Stderr: "bridged: connection rejected (peer credential check failed)\n"})
			return
		}
	}

	conn.SetDeadline(time.Now().Add(30 * time.Second))

	var req Request
	if err := readFrame(conn, &req); err != nil {
		return // client disconnected or sent garbage; nothing to reply to
	}

	resp := dispatch(root, req)
	_ = writeFrame(conn, resp)
}
