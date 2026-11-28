//go:build linux

// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package session

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/metux/starfleetctl/internal/comms"
)

// TestFakeOpenCodeProcessHelper acts as a deterministic OpenCode executable.
// It publishes the same PID-correlated plugin startup and model observations
// that the production plugin publishes, then stays alive until its process
// group is stopped by the production supervisor.
func TestFakeOpenCodeProcessHelper(t *testing.T) {
	if os.Getenv("STARFLEET_TEST_FAKE_OPENCODE") != "1" {
		return
	}
	root := os.Getenv("MPBT_WORKSPACE_ROOT")
	shipID := os.Getenv("STARFLEET_SHIP_ID")
	if root == "" || shipID == "" {
		t.Fatal("fake OpenCode helper requires workspace root and ship identity")
	}
	bus, err := comms.New(root)
	if err != nil {
		t.Fatal(err)
	}
	model := os.Getenv("STARFLEET_TEST_MODEL")
	if model == "" {
		model = "test-model"
	}
	// The fail-model strategy deliberately exits before publishing plugin
	// readiness so the production supervisor must exercise its rollback path.
	if model == "fail-model" {
		return
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	modelArgs := []string{
		"--pid", strconv.Itoa(os.Getpid()),
		"--plugin-version", "test-plugin",
		"--heartbeat-ms", "5000",
		"--plugin-ts", now,
		"--model", model,
		"--server", "meta-model",
	}
	if err := bus.DoHealthUpdate(modelArgs); err != nil {
		t.Fatalf("publish fake plugin health: %v", err)
	}
	if err := bus.DoRuntimeUpdate(comms.RuntimeUpdate{
		Runtime: "starting", Health: "healthy",
		EventID: "plugin-start:" + shipID + ":" + strconv.Itoa(os.Getpid()) + ":" + now,
		ObservedAt: now, SessionID: "test-session-" + strconv.Itoa(os.Getpid()),
		PID: os.Getpid(), Model: model, Provider: "meta-model",
	}); err != nil {
		t.Fatalf("publish fake plugin startup: %v", err)
	}
	descendant := exec.Command("sleep", "60")
	if err := descendant.Start(); err != nil {
		t.Fatalf("start fake OpenCode descendant: %v", err)
	}
	descendantPath := filepath.Join(root, fmt.Sprintf("descendant-%d.pid", os.Getpid()))
	if err := os.WriteFile(descendantPath, []byte(strconv.Itoa(descendant.Process.Pid)), 0600); err != nil {
		t.Fatal(err)
	}
	_, _ = os.Stdout.Write([]byte(fmt.Sprintf("FAKE-OPENCODE-STARTED-%d\n", os.Getpid())))
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(signals)
	<-signals
	_ = descendant.Wait()
}

// TestProductionSupervisorTermctlHelper runs RunTermctl in a separate process;
// its PTY shell execs the production starfleetctl binary and its real
// ship-supervisor command.
func TestProductionSupervisorTermctlHelper(t *testing.T) {
	if os.Getenv("STARFLEET_TEST_TERMCTL_PRODUCTION") != "1" {
		return
	}
	root := os.Getenv("MPBT_WORKSPACE_ROOT")
	shipID := os.Getenv("STARFLEET_TEST_SHIP_ID")
	cliBinary := os.Getenv("STARFLEET_TEST_CLI_BINARY")
	binDir := os.Getenv("STARFLEET_TEST_OPENCODE_BIN_DIR")
	if cliBinary == "" || binDir == "" {
		t.Fatal("production CLI and fake OpenCode bin directory must be set")
	}
	pipePath := PipePath(root, shipID)
	shellCmd := "cd " + shellQuote(root) +
		"; export MPBT_WORKSPACE_ROOT=" + shellQuote(root) +
		"; export STARFLEET_SHIP_ID=" + shellQuote(shipID) +
		"; export STARFLEET_TEST_FAKE_OPENCODE=1" +
		"; export PATH=" + shellQuote(binDir) + ":\"$PATH\"" +
		"; exec " + shellQuote(cliBinary) +
		" ship-supervisor " + shellQuote(shipID) + " test-model terminal Enterprise '' false true" +
		" 2>" + shellQuote(filepath.Join(root, ".starfleet-ai", "var", "ships", shipID+".supervisor.stderr.log"))
	if code := RunTermctl(root, []string{shipID, pipePath, shellCmd}); code != 0 {
		t.Fatalf("RunTermctl returned %d", code)
	}
}

func procTTYIdentity(t *testing.T, pid int) (uint64, uint64, uint64) {
	t.Helper()
	info, err := os.Stat(fmt.Sprintf("/proc/%d/fd/0", pid))
	if err != nil {
		t.Fatalf("stat supervisor PTY fd for pid %d: %v", pid, err)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("unexpected stat type for supervisor PTY fd: %T", info.Sys())
	}
	return uint64(st.Dev), st.Ino, uint64(st.Rdev)
}

func procForegroundPID(t *testing.T, pid int) int {
	t.Helper()
	// TIOCGPGRP requires the caller itself to have the PTY as its controlling
	// terminal. Opening /proc/<pid>/fd/0 from the test process duplicates the
	// slave FD but does not transfer that controlling-terminal relationship.
	// Linux exposes the foreground process group as tpgid (field 8) in stat.
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		t.Fatalf("read /proc/%d/stat: %v", pid, err)
	}
	line := string(data)
	end := strings.LastIndex(line, ")")
	if end < 0 {
		t.Fatalf("malformed /proc stat for pid %d: %q", pid, line)
	}
	fields := strings.Fields(line[end+1:])
	// Fields begin with state (field 3); tty_nr is field 7 and tpgid field 8.
	if len(fields) < 6 {
		t.Fatalf("short /proc stat for pid %d: %q", pid, line)
	}
	tty, err := strconv.ParseInt(fields[4], 10, 64)
	if err != nil || tty == 0 {
		t.Fatalf("pid %d has no controlling terminal (tty_nr=%q, err=%v)", pid, fields[4], err)
	}
	fg, err := strconv.Atoi(fields[5])
	if err != nil {
		t.Fatalf("parse foreground process group for pid %d from %q: %v", pid, fields[5], err)
	}
	return fg
}

func procParentPID(t *testing.T, pid int) int {
	t.Helper()
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		t.Fatalf("read /proc/%d/stat: %v", pid, err)
	}
	// comm is parenthesized and may contain spaces; parse after its final ')'.
	line := string(data)
	end := strings.LastIndex(line, ")")
	if end < 0 {
		t.Fatalf("malformed /proc stat for pid %d: %q", pid, line)
	}
	fields := strings.Fields(line[end+1:])
	if len(fields) < 2 {
		t.Fatalf("short /proc stat for pid %d: %q", pid, line)
	}
	parent, err := strconv.Atoi(fields[1])
	if err != nil {
		t.Fatalf("parse parent pid from %q: %v", line, err)
	}
	return parent
}

func TestRunShipSupervisorProductionPathUnderRealTermctl(t *testing.T) {
	root, err := os.MkdirTemp("", "sf-e2e-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	shipID := "TestProductionSupervisor"
	confDir := filepath.Join(root, ".starfleet-ai", "conf")
	if err := os.MkdirAll(confDir, 0755); err != nil {
		t.Fatal(err)
	}
	// A virtual meta-model gives validateModelAvailable a deterministic,
	// network-free catalog entry for the test model.
	modelConfig := `model_proxy:
  providers:
    - id: meta-model
      type: meta-model
  strategies:
    - id: test-model
      strategy: single
      models:
        - id: upstream-test-model
          provider: test-upstream
    - id: other-model
      strategy: single
      models:
        - id: upstream-other-model
          provider: test-upstream
    - id: fail-model
      strategy: single
      models:
        - id: upstream-fail-model
          provider: test-upstream
`
	if err := os.WriteFile(filepath.Join(confDir, "model-proxy.yaml"), []byte(modelConfig), 0644); err != nil {
		t.Fatal(err)
	}

	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	workingDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	moduleRoot := filepath.Clean(filepath.Join(workingDir, "../.."))
	cliBinary := filepath.Join(root, "starfleetctl")
	build := exec.Command("go", "build", "-o", cliBinary, "./cmd/starfleetctl")
	build.Dir = moduleRoot
	if output, buildErr := build.CombinedOutput(); buildErr != nil {
		t.Fatalf("build production starfleetctl binary: %v\n%s", buildErr, output)
	}
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatal(err)
	}
	fakeOpenCode := filepath.Join(binDir, "opencode")
	wrapper := "#!/bin/sh\n" +
		"model=test-model\n" +
		"while [ \"$#\" -gt 0 ]; do\n" +
		"  if [ \"$1\" = \"--model\" ] && [ \"$#\" -gt 1 ]; then shift; model=\"$1\"; fi\n" +
		"  shift\n" +
		"done\n" +
		"case \"$model\" in */*) model=\u0024{model#*/} ;; esac\n" +
		"export STARFLEET_TEST_MODEL=\"$model\"\n" +
		"exec " + shellQuote(self) + " -test.run=^TestFakeOpenCodeProcessHelper$\n"
	if err := os.WriteFile(fakeOpenCode, []byte(wrapper), 0755); err != nil {
		t.Fatal(err)
	}
	pipePath := PipePath(root, shipID)
	if err := os.MkdirAll(filepath.Dir(pipePath), 0755); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(self, "-test.run=^TestProductionSupervisorTermctlHelper$")
	cmd.Env = append(os.Environ(),
		"STARFLEET_TEST_TERMCTL_PRODUCTION=1",
		"STARFLEET_TEST_SHIP_ID="+shipID,
		"STARFLEET_TEST_FAKE_OPENCODE=1",
		"STARFLEET_TEST_CLI_BINARY="+cliBinary,
		"STARFLEET_TEST_OPENCODE_BIN_DIR="+binDir,
		"MPBT_WORKSPACE_ROOT="+root,
		"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
	)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waitDone := make(chan struct{})
	var waitErr error
	go func() { waitErr = cmd.Wait(); close(waitDone) }()
	stopped := false
	defer func() {
		if stopped {
			return
		}
		_ = StopShip(root, shipID)
		select {
		case <-waitDone:
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
			<-waitDone
		}
	}()

	socketPath := SocketPath(root, shipID)
	deadline := time.Now().Add(15 * time.Second)
	var first comms.StatusRecord
	for time.Now().Before(deadline) {
		select {
		case <-waitDone:
			logPath := filepath.Join(root, ".starfleet-ai", "var", "ships", shipID+".supervisor.stderr.log")
			logData, _ := os.ReadFile(logPath)
			t.Fatalf("termctl/supervisor exited before readiness: %v; supervisor stderr (%s):\n%s", waitErr, logPath, logData)
		default:
		}
		if info, statErr := os.Stat(socketPath); statErr == nil && info.Mode()&os.ModeSocket != 0 {
			bus, busErr := comms.New(root)
			if busErr == nil {
				for _, record := range bus.AllStatusRecords() {
					if record.Agent == shipID && record.PluginStartPID > 0 {
						first = record
						break
					}
				}
			}
			if first.PluginStartPID > 0 {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if first.PluginStartPID == 0 {
		t.Fatal("production supervisor did not publish correlated plugin readiness")
	}
	firstChild := first.PluginStartPID
	firstRunner := procParentPID(t, firstChild)
	firstPTYDev, firstPTYIno, firstPTYRdev := procTTYIdentity(t, firstRunner)
	firstDescendantData, err := os.ReadFile(filepath.Join(root, fmt.Sprintf("descendant-%d.pid", firstChild)))
	if err != nil {
		t.Fatalf("read first OpenCode descendant PID: %v", err)
	}
	firstDescendant, err := strconv.Atoi(strings.TrimSpace(string(firstDescendantData)))
	if err != nil {
		t.Fatalf("parse first OpenCode descendant PID: %v", err)
	}
	firstSocketInfo, err := os.Stat(socketPath)
	if err != nil {
		t.Fatal(err)
	}
	firstSocketInode := firstSocketInfo.Sys().(*syscall.Stat_t).Ino
	pidData, err := os.ReadFile(PidPath(root, shipID))
	if err != nil {
		t.Fatalf("read termctl-run PID: %v", err)
	}
	termctlPID, err := strconv.Atoi(strings.TrimSpace(string(pidData)))
	if err != nil || termctlPID != cmd.Process.Pid {
		t.Fatalf("termctl-run PID file=%q helper PID=%d err=%v", pidData, cmd.Process.Pid, err)
	}
	pipeInfo, err := os.Stat(pipePath)
	if err != nil {
		t.Fatal(err)
	}
	pipeInode := pipeInfo.Sys().(*syscall.Stat_t).Ino
	if err := RestartShip(root, shipID, "meta-model/missing-model"); err == nil {
		t.Fatal("invalid model restart unexpectedly succeeded")
	}
	bus, err := comms.New(root)
	if err != nil {
		t.Fatal(err)
	}
	var unchanged comms.StatusRecord
	for _, record := range bus.AllStatusRecords() {
		if record.Agent == shipID {
			unchanged = record
			break
		}
	}
	if unchanged.PluginStartPID != firstChild || unchanged.Model != "test-model" {
		t.Fatalf("invalid model request disturbed the working child: %+v", unchanged)
	}
	if err := syscall.Kill(firstChild, 0); err != nil {
		t.Fatalf("working OpenCode child %d was killed by invalid model request: %v", firstChild, err)
	}

	if err := RestartShip(root, shipID, "meta-model/other-model"); err != nil {
		t.Fatalf("production model-switch restart: %v", err)
	}
	var second comms.StatusRecord
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		bus, busErr := comms.New(root)
		if busErr == nil {
			for _, record := range bus.AllStatusRecords() {
				if record.Agent == shipID && record.PluginStartPID > 0 && record.PluginStartPID != firstChild {
					second = record
					break
				}
			}
		}
		if second.PluginStartPID > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if second.PluginStartPID == 0 {
		t.Fatal("production restart did not publish readiness for a new OpenCode PID")
	}
	secondChild := second.PluginStartPID
	if second.Model != "other-model" || second.Server != "meta-model" {
		t.Fatalf("effective model after requested switch = %s/%s, want meta-model/other-model", second.Server, second.Model)
	}
	secondRunner := procParentPID(t, secondChild)
	if secondRunner != firstRunner {
		t.Fatalf("in-PTY supervisor changed across restart: before=%d after=%d", firstRunner, secondRunner)
	}
	secondPTYDev, secondPTYIno, secondPTYRdev := procTTYIdentity(t, secondRunner)
	if secondPTYDev != firstPTYDev || secondPTYIno != firstPTYIno || secondPTYRdev != firstPTYRdev {
		t.Fatalf("supervisor PTY identity changed: before=(%d,%d,%d) after=(%d,%d,%d)",
			firstPTYDev, firstPTYIno, firstPTYRdev, secondPTYDev, secondPTYIno, secondPTYRdev)
	}
	secondSocketInfo, err := os.Stat(socketPath)
	if err != nil {
		t.Fatalf("supervisor control socket disappeared: %v", err)
	}
	if got := secondSocketInfo.Sys().(*syscall.Stat_t).Ino; got != firstSocketInode {
		t.Fatalf("supervisor socket inode changed: before=%d after=%d", firstSocketInode, got)
	}
	if !waitSupervisorPIDGone(firstDescendant) {
		t.Fatalf("old OpenCode descendant %d survived production restart", firstDescendant)
	}
	if second.RuntimeSessionID == first.RuntimeSessionID {
		t.Fatalf("fake plugin session identity did not change across child restart: %q", second.RuntimeSessionID)
	}
	pidAfter, err := os.ReadFile(PidPath(root, shipID))
	if err != nil {
		t.Fatalf("termctl-run PID file disappeared: %v", err)
	}
	if strings.TrimSpace(string(pidAfter)) != strconv.Itoa(termctlPID) {
		t.Fatalf("termctl-run parent changed across restart: before=%d after=%s", termctlPID, strings.TrimSpace(string(pidAfter)))
	}
	pipeAfter, err := os.Stat(pipePath)
	if err != nil {
		t.Fatalf("termctl control pipe disappeared: %v", err)
	}
	if got := pipeAfter.Sys().(*syscall.Stat_t).Ino; got != pipeInode {
		t.Fatalf("termctl control pipe inode changed: before=%d after=%d", pipeInode, got)
	}
	if !waitSupervisorPIDGone(firstChild) {
		t.Fatalf("old OpenCode process %d survived production restart", firstChild)
	}
	if err := RestartShip(root, shipID, "meta-model/fail-model"); err == nil || !strings.Contains(err.Error(), "restored previous model") {
		t.Fatalf("failed replacement did not report successful rollback: %v", err)
	}
	var rolledBack comms.StatusRecord
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		bus, busErr := comms.New(root)
		if busErr == nil {
			for _, record := range bus.AllStatusRecords() {
				if record.Agent == shipID && record.PluginStartPID > 0 && record.PluginStartPID != secondChild {
					rolledBack = record
					break
				}
			}
		}
		if rolledBack.PluginStartPID > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if rolledBack.PluginStartPID == 0 || rolledBack.Model != "other-model" || rolledBack.Server != "meta-model" {
		t.Fatalf("previous model was not restored after failed replacement: %+v", rolledBack)
	}
	rollbackChild := rolledBack.PluginStartPID
	if procParentPID(t, rollbackChild) != firstRunner {
		t.Fatalf("supervisor changed during rollback")
	}
	secondDescendantData, err := os.ReadFile(filepath.Join(root, fmt.Sprintf("descendant-%d.pid", secondChild)))
	if err != nil {
		t.Fatalf("read model-switch child descendant PID: %v", err)
	}
	secondDescendant, err := strconv.Atoi(strings.TrimSpace(string(secondDescendantData)))
	if err != nil {
		t.Fatalf("parse model-switch child descendant PID: %v", err)
	}
	if !waitSupervisorPIDGone(secondDescendant) {
		t.Fatalf("failed replacement left child descendant %d alive", secondDescendant)
	}
	// Two concurrent callers must be serialized by the one persistent
	// supervisor. Both callers receive an acknowledgement only after their
	// own replacement is ready; the last child remains in the same PTY.
	restartResults := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { restartResults <- RestartShip(root, shipID, "") }()
	}
	for i := 0; i < 2; i++ {
		if err := <-restartResults; err != nil {
			t.Fatalf("concurrent production RestartShip %d: %v", i+1, err)
		}
	}
	var concurrentFinal comms.StatusRecord
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		bus, busErr := comms.New(root)
		if busErr == nil {
			for _, record := range bus.AllStatusRecords() {
				if record.Agent == shipID && record.PluginStartPID > 0 && record.PluginStartPID != rollbackChild {
					concurrentFinal = record
					break
				}
			}
		}
		if concurrentFinal.PluginStartPID > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if concurrentFinal.PluginStartPID == 0 || concurrentFinal.Model != "other-model" {
		t.Fatalf("concurrent restarts did not leave a ready child on the selected model: %+v", concurrentFinal)
	}
	finalChild := concurrentFinal.PluginStartPID
	if procParentPID(t, finalChild) != firstRunner {
		t.Fatalf("supervisor changed during concurrent restarts")
	}
	finalDescendantData, err := os.ReadFile(filepath.Join(root, fmt.Sprintf("descendant-%d.pid", finalChild)))
	if err != nil {
		t.Fatalf("read final OpenCode descendant PID: %v", err)
	}
	finalDescendant, err := strconv.Atoi(strings.TrimSpace(string(finalDescendantData)))
	if err != nil {
		t.Fatalf("parse final OpenCode descendant PID: %v", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, "descendant-") || !strings.HasSuffix(name, ".pid") {
			continue
		}
		data, readErr := os.ReadFile(filepath.Join(root, name))
		if readErr != nil {
			t.Fatalf("read descendant record %s: %v", name, readErr)
		}
		pid, parseErr := strconv.Atoi(strings.TrimSpace(string(data)))
		if parseErr != nil {
			t.Fatalf("parse descendant record %s: %v", name, parseErr)
		}
		if pid != finalDescendant && !waitSupervisorPIDGone(pid) {
			t.Fatalf("superseded OpenCode descendant %d survived serialized restarts", pid)
		}
	}

	// Deliver SIGINT to the foreground OpenCode process group, which is the
	// kernel-level effect of terminal Ctrl-C. The production supervisor must
	// reap the child group, restore itself as foreground, and keep its control
	// socket alive so a new child can be started without replacing the PTY.
	if err := syscall.Kill(-finalChild, syscall.SIGINT); err != nil {
		t.Fatalf("send Ctrl-C SIGINT to foreground child group: %v", err)
	}
	var status string
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		status = supervisorPTYRequest(t, socketPath, "STATUS")
		if status == "STOPPED" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if status != "STOPPED" {
		t.Fatalf("supervisor status after Ctrl-C = %q, want STOPPED", status)
	}
	if procForegroundPID(t, firstRunner) != firstRunner {
		t.Fatal("supervisor did not regain PTY foreground after Ctrl-C")
	}
	if !waitSupervisorPIDGone(finalDescendant) {
		t.Fatalf("OpenCode descendant %d survived Ctrl-C", finalDescendant)
	}
	select {
	case <-waitDone:
		t.Fatalf("termctl-run owner exited after child Ctrl-C: %v", waitErr)
	default:
	}
	if err := RestartShip(root, shipID, ""); err != nil {
		t.Fatalf("restart after Ctrl-C: %v", err)
	}
	var afterInterrupt comms.StatusRecord
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		bus, busErr := comms.New(root)
		if busErr == nil {
			for _, record := range bus.AllStatusRecords() {
				if record.Agent == shipID && record.PluginStartPID > 0 && record.PluginStartPID != finalChild {
					afterInterrupt = record
					break
				}
			}
		}
		if afterInterrupt.PluginStartPID > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if afterInterrupt.PluginStartPID == 0 || afterInterrupt.Model != "other-model" {
		t.Fatalf("production supervisor did not restart after Ctrl-C: %+v", afterInterrupt)
	}
	afterInterruptDescendant := readProductionDescendantPID(t, root, afterInterrupt.PluginStartPID)

	if err := StopShip(root, shipID); err != nil {
		t.Fatalf("explicit StopShip after production restart: %v", err)
	}
	select {
	case <-waitDone:
		stopped = true
		if waitErr != nil {
			t.Fatalf("termctl-run helper exit after StopShip: %v", waitErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("termctl-run owner did not exit after explicit StopShip")
	}
	if !waitSupervisorPIDGone(afterInterruptDescendant) {
		t.Fatalf("post-Ctrl-C OpenCode descendant %d survived explicit stop", afterInterruptDescendant)
	}
}


type productionTermctlTestRun struct {
	cmd     *exec.Cmd
	done    chan struct{}
	waitErr error
}

func startProductionTermctlTestRun(t *testing.T, root, shipID, self, cliBinary, binDir string) *productionTermctlTestRun {
	t.Helper()
	cmd := exec.Command(self, "-test.run=^TestProductionSupervisorTermctlHelper$")
	cmd.Env = append(os.Environ(),
		"STARFLEET_TEST_TERMCTL_PRODUCTION=1",
		"STARFLEET_TEST_SHIP_ID="+shipID,
		"STARFLEET_TEST_FAKE_OPENCODE=1",
		"STARFLEET_TEST_CLI_BINARY="+cliBinary,
		"STARFLEET_TEST_OPENCODE_BIN_DIR="+binDir,
		"MPBT_WORKSPACE_ROOT="+root,
		"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
	)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	run := &productionTermctlTestRun{cmd: cmd, done: make(chan struct{})}
	go func() {
		run.waitErr = cmd.Wait()
		close(run.done)
	}()
	return run
}

func waitProductionTermctlChild(t *testing.T, root, shipID string, run *productionTermctlTestRun) comms.StatusRecord {
	t.Helper()
	socketPath := SocketPath(root, shipID)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-run.done:
			logPath := filepath.Join(root, ".starfleet-ai", "var", "ships", shipID+".supervisor.stderr.log")
			logData, _ := os.ReadFile(logPath)
			t.Fatalf("termctl/supervisor %s exited before readiness: %v\n%s", shipID, run.waitErr, logData)
		default:
		}
		if info, err := os.Stat(socketPath); err == nil && info.Mode()&os.ModeSocket != 0 {
			bus, busErr := comms.New(root)
			if busErr == nil {
				for _, record := range bus.AllStatusRecords() {
					if record.Agent == shipID && record.PluginStartPID > 0 {
						return record
					}
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for production supervisor %s", shipID)
	return comms.StatusRecord{}
}

func readProductionDescendantPID(t *testing.T, root string, childPID int) int {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, fmt.Sprintf("descendant-%d.pid", childPID)))
	if err != nil {
		t.Fatalf("read OpenCode descendant PID for %d: %v", childPID, err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatalf("parse OpenCode descendant PID for %d: %v", childPID, err)
	}
	return pid
}

func waitProductionTermctlExit(t *testing.T, run *productionTermctlTestRun, description string) {
	t.Helper()
	select {
	case <-run.done:
	case <-time.After(5 * time.Second):
		t.Fatalf("termctl-run owner did not exit after %s", description)
	}
}

func TestProductionSupervisorCleansChildAndRunnerCrashes(t *testing.T) {
	root, err := os.MkdirTemp("", "sf-crash-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	confDir := filepath.Join(root, ".starfleet-ai", "conf")
	if err := os.MkdirAll(confDir, 0755); err != nil {
		t.Fatal(err)
	}
	modelConfig := `model_proxy:
  providers:
    - id: meta-model
      type: meta-model
  strategies:
    - id: test-model
      strategy: single
      models:
        - id: upstream-test-model
          provider: test-upstream
`
	if err := os.WriteFile(filepath.Join(confDir, "model-proxy.yaml"), []byte(modelConfig), 0644); err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	workingDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	moduleRoot := filepath.Clean(filepath.Join(workingDir, "../.."))
	cliBinary := filepath.Join(root, "starfleetctl")
	build := exec.Command("go", "build", "-o", cliBinary, "./cmd/starfleetctl")
	build.Dir = moduleRoot
	if output, buildErr := build.CombinedOutput(); buildErr != nil {
		t.Fatalf("build production starfleetctl binary: %v\n%s", buildErr, output)
	}
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatal(err)
	}
	wrapper := "#!/bin/sh\nexec " + shellQuote(self) + " -test.run=^TestFakeOpenCodeProcessHelper$\n"
	if err := os.WriteFile(filepath.Join(binDir, "opencode"), []byte(wrapper), 0755); err != nil {
		t.Fatal(err)
	}

	t.Run("OpenCode child crash cleans its descendants", func(t *testing.T) {
		shipID := "TestChildCrash"
		run := startProductionTermctlTestRun(t, root, shipID, self, cliBinary, binDir)
		record := waitProductionTermctlChild(t, root, shipID, run)
		childPID := record.PluginStartPID
		descendantPID := readProductionDescendantPID(t, root, childPID)
		// Kill only the OpenCode leader, not its process group. The supervisor
		// must notice the child exit and reap the remaining group members.
		if err := syscall.Kill(childPID, syscall.SIGKILL); err != nil {
			t.Fatal(err)
		}
		waitProductionTermctlExit(t, run, "OpenCode child crash")
		if !waitSupervisorPIDGone(descendantPID) {
			t.Fatalf("OpenCode descendant %d survived child crash cleanup", descendantPID)
		}
	})

	t.Run("supervisor crash releases the OpenCode process tree", func(t *testing.T) {
		shipID := "TestRunnerCrash"
		run := startProductionTermctlTestRun(t, root, shipID, self, cliBinary, binDir)
		record := waitProductionTermctlChild(t, root, shipID, run)
		childPID := record.PluginStartPID
		runnerPID := procParentPID(t, childPID)
		descendantPID := readProductionDescendantPID(t, root, childPID)
		if err := syscall.Kill(runnerPID, syscall.SIGKILL); err != nil {
			t.Fatal(err)
		}
		waitProductionTermctlExit(t, run, "supervisor/runner crash")
		if !waitSupervisorPIDGone(childPID) {
			_ = syscall.Kill(-childPID, syscall.SIGKILL)
			t.Fatalf("OpenCode child %d survived supervisor crash", childPID)
		}
		if !waitSupervisorPIDGone(descendantPID) {
			_ = syscall.Kill(-childPID, syscall.SIGKILL)
			t.Fatalf("OpenCode descendant %d survived supervisor crash", descendantPID)
		}
	})
}
