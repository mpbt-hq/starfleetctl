// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package bridged

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/metux/starfleetctl/internal/config"
)

// newScratchRoot builds a workspace root suitable for both comms (just
// needs _WORK_/comms/ to exist under it) and dashboard (needs to be a
// real git working tree with a DASHBOARD.md and an origin remote, since
// pull/commit shell out to real git) — entirely local, no network.
func newScratchRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()

	if err := os.MkdirAll(config.BusDir(root), 0o755); err != nil {
		t.Fatal(err)
	}

	origin := filepath.Join(t.TempDir(), "origin.git")
	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v (in %s): %v\n%s", args, dir, err, out)
		}
	}
	run(t.TempDir(), "init", "--bare", "-q", origin)
	run(root, "init", "-q")
	run(root, "config", "user.email", "test@test.com")
	run(root, "config", "user.name", "Test")
	run(root, "remote", "add", "origin", origin)
	if err := os.MkdirAll(filepath.Join(root, ".starfleet-ai", "var"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".starfleet-ai", "var", "DASHBOARD.md"), []byte("# DASHBOARD\n\ninitial\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// .starfleet-ai/ is a generated dir (like .bin/, .claude/hooks/) — ignore it
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("/.starfleet-ai/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(root, "add", ".gitignore")
	run(root, "commit", "-q", "-m", "initial")
	run(root, "push", "-q", "-u", "origin", "HEAD:master")

	return root
}

// startTestServer starts ListenAndServe in a goroutine against a fresh
// scratch root + a socket path under t.TempDir(), and returns the socket
// path plus a stop func that sends SIGTERM (exercising the real
// clean-shutdown path, not a test-only shortcut) and waits for the server
// goroutine to return.
func startTestServer(t *testing.T) (sockPath string, root string, stop func()) {
	t.Helper()
	root = newScratchRoot(t)
	sockPath = filepath.Join(t.TempDir(), "bridged.sock")

	done := make(chan error, 1)
	go func() { done <- ListenAndServe(root, sockPath) }()

	waitForSocket(t, sockPath)

	stop = func() {
		if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("ListenAndServe returned error: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("ListenAndServe did not shut down within 5s of SIGTERM")
		}
	}
	return sockPath, root, stop
}

func waitForSocket(t *testing.T, sockPath string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := Call(sockPath, Request{Cmd: "ping"}, 200*time.Millisecond); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("server did not become reachable in time")
}

func TestPing(t *testing.T) {
	sockPath, _, stop := startTestServer(t)
	defer stop()

	resp, err := Call(sockPath, Request{Cmd: "ping"}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if resp.ExitCode != 0 || resp.Stdout != "pong\n" {
		t.Errorf("got %+v, want exit 0 / stdout \"pong\\n\"", resp)
	}
}

func TestAgentBusRoundTrip(t *testing.T) {
	sockPath, _, stop := startTestServer(t)
	defer stop()

	os.Setenv("STARFLEET_SHIP_ID", "TestShip")
	defer os.Unsetenv("STARFLEET_SHIP_ID")

	resp, err := Call(sockPath, Request{Cmd: "comms", Args: []string{"status", "working", "via bridged"}}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if resp.ExitCode != 0 {
		t.Fatalf("status: exit %d, stderr=%q", resp.ExitCode, resp.Stderr)
	}

	resp, err = Call(sockPath, Request{Cmd: "comms", Args: []string{"board"}}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if resp.ExitCode != 0 {
		t.Fatalf("board: exit %d, stderr=%q", resp.ExitCode, resp.Stderr)
	}
	if !strings.Contains(resp.Stdout, "TestShip") || !strings.Contains(resp.Stdout, "via bridged") {
		t.Errorf("board output missing expected content: %q", resp.Stdout)
	}
}

// TestPerRequestIdentityNoLeakage is the test Enterprise's directive
// (m0082) explicitly asked for: fire many requests with DIFFERENT
// per-request STARFLEET_SHIP_ID overrides (via Request.Env, not ambient
// process env) at once, and confirm each one's response reflects only its
// own identity — none see another's, and none see the ambient ("unset")
// identity either. Execution is serialized by execMu, so true
// interleaving inside a single comms.Run call is impossible; this test
// instead guards against the failure mode that WOULD leak: a bug in
// applyEnvOverrides that restores the wrong value, or restores too early/
// late relative to the mutex, would show up here as ships seeing each
// other's identity or the wrong (ambient) one.
func TestPerRequestIdentityNoLeakage(t *testing.T) {
	sockPath, _, stop := startTestServer(t)
	defer stop()

	os.Unsetenv("STARFLEET_SHIP_ID") // confirm no ambient identity leaks through

	const n = 15
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ship := fmt.Sprintf("Ship-%02d", i)
			note := fmt.Sprintf("note-%02d", i)

			statusResp, err := Call(sockPath, Request{
				Cmd:  "comms",
				Args: []string{"status", "working", note},
				Env:  map[string]string{"STARFLEET_SHIP_ID": ship},
			}, 5*time.Second)
			if err != nil {
				errs[i] = fmt.Errorf("%s: status call: %w", ship, err)
				return
			}
			if statusResp.ExitCode != 0 {
				errs[i] = fmt.Errorf("%s: status exit %d, stderr=%q", ship, statusResp.ExitCode, statusResp.Stderr)
				return
			}
			if !strings.Contains(statusResp.Stdout, "'"+ship+"'") {
				errs[i] = fmt.Errorf("%s: status response didn't echo own identity: %q", ship, statusResp.Stdout)
				return
			}

			whoResp, err := Call(sockPath, Request{
				Cmd:  "comms",
				Args: []string{"inbox"},
				Env:  map[string]string{"STARFLEET_SHIP_ID": ship},
			}, 5*time.Second)
			if err != nil {
				errs[i] = fmt.Errorf("%s: inbox call: %w", ship, err)
				return
			}
			if whoResp.ExitCode != 0 {
				errs[i] = fmt.Errorf("%s: inbox exit %d, stderr=%q", ship, whoResp.ExitCode, whoResp.Stderr)
			}
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Error(err)
		}
	}

	// The board must show all N ships as distinct rows with their own
	// notes — proves the writes themselves landed under the right
	// identity, not just that each response echoed the right name.
	boardResp, err := Call(sockPath, Request{Cmd: "comms", Args: []string{"board"}}, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		ship := fmt.Sprintf("Ship-%02d", i)
		note := fmt.Sprintf("note-%02d", i)
		if !strings.Contains(boardResp.Stdout, ship) || !strings.Contains(boardResp.Stdout, note) {
			t.Errorf("board missing %s/%s: %s", ship, note, boardResp.Stdout)
		}
	}

	// After all overridden requests, an unspecified-Env request must fall
	// back to the ambient (still-unset) identity, not leak the last
	// override — proves restoration actually ran, not just that concurrent
	// calls happened to not collide.
	plainResp, err := Call(sockPath, Request{Cmd: "comms", Args: []string{"status", "idle", "no override"}}, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		ship := fmt.Sprintf("Ship-%02d", i)
		if strings.Contains(plainResp.Stdout, "'"+ship+"'") {
			t.Errorf("unspecified-Env request leaked a prior override's identity (%s): %q", ship, plainResp.Stdout)
		}
	}
}

func TestDashboardRoundTrip(t *testing.T) {
	sockPath, root, stop := startTestServer(t)
	defer stop()

	resp, err := Call(sockPath, Request{Cmd: "dashboard", Args: []string{"show"}}, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if resp.ExitCode != 0 {
		t.Fatalf("show: exit %d, stderr=%q", resp.ExitCode, resp.Stderr)
	}
	if !strings.Contains(resp.Stdout, "initial") {
		t.Errorf("show output missing initial content: %q", resp.Stdout)
	}

	newFile := filepath.Join(t.TempDir(), "new.md")
	if err := os.WriteFile(newFile, []byte("# DASHBOARD\n\nupdated via bridged\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	resp, err = Call(sockPath, Request{Cmd: "dashboard", Args: []string{"write", newFile}}, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if resp.ExitCode != 0 {
		t.Fatalf("write: exit %d, stderr=%q", resp.ExitCode, resp.Stderr)
	}

	got, err := os.ReadFile(filepath.Join(root, ".starfleet-ai", "var", "DASHBOARD.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "updated via bridged") {
		t.Errorf("DASHBOARD.md not updated: %q", got)
	}
}

// TestDangerousSubcommandsRejected is the single most important safety
// test in this package: "ask" blocks polling for a reply and calls
// os.Exit(3) on timeout; "monitor-loop"/"fleet-watch"/"watch" are
// intentionally infinite loops. Any of them reaching comms.Run() through
// the daemon would wedge or kill the whole process for every connected
// client, not just the caller. The allowlist in server.go must reject all
// four before they're ever invoked.
func TestDangerousSubcommandsRejected(t *testing.T) {
	sockPath, _, stop := startTestServer(t)
	defer stop()

	for _, sub := range []string{"ask", "monitor-loop", "fleet-watch", "watch"} {
		t.Run(sub, func(t *testing.T) {
			resp, err := Call(sockPath, Request{Cmd: "comms", Args: []string{sub, "irrelevant arg"}}, 2*time.Second)
			if err != nil {
				t.Fatalf("call for %q errored (server may have hung/died): %v", sub, err)
			}
			if resp.ExitCode != 2 {
				t.Errorf("%q: got exit %d, want 2 (rejected)", sub, resp.ExitCode)
			}
			if !strings.Contains(resp.Stderr, "not available via the daemon") {
				t.Errorf("%q: stderr = %q, want rejection message", sub, resp.Stderr)
			}
		})
	}

	// The server must still be alive and responsive after all four —
	// proves none of them wedged or killed it.
	resp, err := Call(sockPath, Request{Cmd: "ping"}, time.Second)
	if err != nil || resp.ExitCode != 0 {
		t.Fatalf("server not responsive after rejected calls: resp=%+v err=%v", resp, err)
	}
}

func TestConcurrentConnections(t *testing.T) {
	sockPath, _, stop := startTestServer(t)
	defer stop()

	const n = 20
	var wg sync.WaitGroup
	errs := make([]error, n)
	mismatches := make([]string, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			agent := "Ship" + string(rune('A'+i%26))
			note := "note-" + string(rune('0'+i%10))
			resp, err := Call(sockPath, Request{Cmd: "ping"}, 3*time.Second)
			if err != nil {
				errs[i] = err
				return
			}
			if resp.Stdout != "pong\n" {
				mismatches[i] = resp.Stdout
			}
			_ = agent
			_ = note
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d: %v", i, err)
		}
	}
	for i, m := range mismatches {
		if m != "" {
			t.Errorf("goroutine %d: got corrupted/wrong response %q", i, m)
		}
	}
}

func TestStaleSocketRecovery(t *testing.T) {
	root := newScratchRoot(t)
	sockPath := filepath.Join(t.TempDir(), "bridged.sock")

	// Simulate a crash-left-behind socket file: net.Listen's default
	// UnixListener auto-unlinks its socket file on a graceful Close(), so
	// a plain Listen+Close wouldn't actually leave anything stale behind.
	// SetUnlinkOnClose(false) disables that auto-cleanup, faithfully
	// reproducing what an unclean (SIGKILL'd) daemon exit leaves on disk.
	addr, err := net.ResolveUnixAddr("unix", sockPath)
	if err != nil {
		t.Fatal(err)
	}
	l, err := net.ListenUnix("unix", addr)
	if err != nil {
		t.Fatal(err)
	}
	l.SetUnlinkOnClose(false)
	l.Close()

	if _, err := os.Stat(sockPath); err != nil {
		t.Fatalf("expected stale socket file to exist: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- ListenAndServe(root, sockPath) }()
	waitForSocket(t, sockPath)

	resp, err := Call(sockPath, Request{Cmd: "ping"}, time.Second)
	if err != nil || resp.ExitCode != 0 {
		t.Fatalf("daemon did not come up cleanly after stale socket: resp=%+v err=%v", resp, err)
	}

	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	<-done
}

func TestRefusesSecondInstance(t *testing.T) {
	sockPath, root, stop := startTestServer(t)
	defer stop()

	err := ListenAndServe(root, sockPath)
	if err == nil {
		t.Fatal("expected second ListenAndServe on the same socket to fail")
	}
	if !strings.Contains(err.Error(), "already running") {
		t.Errorf("got error %q, want an \"already running\" refusal", err)
	}
}

func TestCleanShutdownRemovesSocket(t *testing.T) {
	sockPath, _, stop := startTestServer(t)
	stop()

	if _, err := os.Stat(sockPath); !os.IsNotExist(err) {
		t.Errorf("socket file still present after clean shutdown: err=%v", err)
	}
}
