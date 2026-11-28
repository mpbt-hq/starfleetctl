//go:build linux

// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package session

import (
 "bufio"
 "encoding/json"
 "fmt"
 "net"
 "os"
 "os/exec"
 "os/signal"
 "path/filepath"
 "strconv"
 "strings"
 "syscall"
 "testing"
 "time"

 "golang.org/x/sys/unix"
 "github.com/metux/starfleetctl/internal/comms"
 "github.com/X11Libre/go-x11proto/tk/term/termctl"
)

type supervisorPTYTestState struct {
 RunnerPID int
 ChildPID int
 ForegroundPID int
 PTYDev uint64
 PTYIno uint64
 PTYRdev uint64
 SocketIno uint64
 DescendantPID int
 TermiosLflag uint32
}

func TestShipSupervisorPTYHelper(t *testing.T) {
 if os.Getenv("STARFLEET_TEST_PTY_HELPER")!="1" { return }
 sock:=os.Getenv("STARFLEET_TEST_PTY_SOCKET")
 statePath:=os.Getenv("STARFLEET_TEST_PTY_STATE")
 descPath:=os.Getenv("STARFLEET_TEST_PTY_DESC")
 signal.Ignore(syscall.SIGTTOU)
 initial,termErr:=unix.IoctlGetTermios(0,unix.TCGETS);if termErr!=nil{t.Fatal(termErr)}
 s:=&shipSupervisor{ttyFD:0,runnerPGID:os.Getpid(),initialTermios:initial}
 listener,err:=net.Listen("unix",sock);if err!=nil{t.Fatal(err)}
 defer listener.Close()
 start:=func()error{
  cmd:=exec.Command("/bin/sh","-c","sleep 60 & echo $! > "+strconv.Quote(descPath)+"; wait")
  cmd.Stdin,cmd.Stdout,cmd.Stderr=os.Stdin,os.Stdout,os.Stderr
  child,err:=startForegroundProcess(0,cmd,nil);if err!=nil{return err}
  s.child=child
  deadline:=time.Now().Add(2*time.Second)
  for time.Now().Before(deadline) {
   if data,readErr:=os.ReadFile(descPath);readErr==nil&&strings.TrimSpace(string(data))!=""{break}
   time.Sleep(10*time.Millisecond)
  }
  if data,readErr:=os.ReadFile(descPath);readErr!=nil||strings.TrimSpace(string(data))==""{return fmt.Errorf("child did not publish descendant PID")}
  return writeSupervisorPTYTestState(statePath,sock,descPath,s)
 }
 fmt.Fprintln(os.Stdout,"SUPERVISOR-PTY-FIRST")
 for i:=0;i<80;i++{fmt.Fprintf(os.Stdout,"SUPERVISOR-PRE-%02d\n",i)}
 if err:=start();err!=nil{t.Fatal(err)}
 for {
  conn,err:=listener.Accept();if err!=nil{t.Fatal(err)}
  _=conn.SetDeadline(time.Now().Add(5*time.Second))
  line,readErr:=bufio.NewReader(conn).ReadString('\n')
  if readErr!=nil{_=conn.Close();continue}
  switch strings.TrimSpace(line){
  case "RESTART":
   raw,termErr:=unix.IoctlGetTermios(0,unix.TCGETS);if termErr==nil{raw.Lflag &^= unix.ICANON|unix.ECHO;_=unix.IoctlSetTermios(0,unix.TCSETS,raw)}
   if err:=s.stopChild();err!=nil{_,_=fmt.Fprintln(conn,"ERROR "+err.Error());_=conn.Close();continue}
   if err:=start();err!=nil{_,_=fmt.Fprintln(conn,"ERROR "+err.Error());_=conn.Close();continue}
   fmt.Fprintln(os.Stdout,"SUPERVISOR-PTY-SECOND")
   for i:=0;i<80;i++{fmt.Fprintf(os.Stdout,"SUPERVISOR-POST-%02d\n",i)}
   _,_=fmt.Fprintln(conn,"OK")
  case "STATUS":
   if s.child!=nil{select{case <-s.child.done:s.finishExitedChild();default:}}
   _=writeSupervisorPTYTestState(statePath,sock,descPath,s)
   _,_=fmt.Fprintln(conn,"OK")
  case "EXIT":
   _=s.stopChild()
   _=writeSupervisorPTYTestState(statePath,sock,descPath,s)
   _,_=fmt.Fprintln(conn,"BYE")
   _=conn.Close()
   return
  default:_,_=fmt.Fprintln(conn,"ERROR unknown")
  }
  _=conn.Close()
 }
}

func writeSupervisorPTYTestState(path,sock,desc string,s *shipSupervisor)error{
 var st unix.Stat_t
 if err:=unix.Fstat(0,&st);err!=nil{return err}
 fg,err:=unix.IoctlGetInt(0,unix.TIOCGPGRP);if err!=nil{return err}
 info,err:=os.Stat(sock);if err!=nil{return err}
 sockStat:=info.Sys().(*syscall.Stat_t)
 descPID:=0
 if data,err:=os.ReadFile(desc);err==nil{descPID,_=strconv.Atoi(strings.TrimSpace(string(data)))}
 termios,_:=unix.IoctlGetTermios(0,unix.TCGETS)
 state:=supervisorPTYTestState{RunnerPID:os.Getpid(),ForegroundPID:fg,PTYDev:uint64(st.Dev),PTYIno:st.Ino,PTYRdev:uint64(st.Rdev),SocketIno:sockStat.Ino,DescendantPID:descPID,TermiosLflag:termios.Lflag}
 if s.child!=nil{state.ChildPID=s.child.pid}
 data,err:=json.Marshal(state);if err!=nil{return err}
 return os.WriteFile(path,data,0600)
}
func openSupervisorTestPTY(t *testing.T)(*os.File,*os.File){
 t.Helper()
 master,err:=os.OpenFile("/dev/ptmx",os.O_RDWR|unix.O_NOCTTY,0);if err!=nil{t.Fatal(err)}
 if err:=unix.IoctlSetPointerInt(int(master.Fd()),unix.TIOCSPTLCK,0);err!=nil{_=master.Close();t.Fatal(err)}
 n,err:=unix.IoctlGetInt(int(master.Fd()),unix.TIOCGPTN);if err!=nil{_=master.Close();t.Fatal(err)}
 slave,err:=os.OpenFile(fmt.Sprintf("/dev/pts/%d",n),os.O_RDWR|unix.O_NOCTTY,0);if err!=nil{_=master.Close();t.Fatal(err)}
 return master,slave
}
func readSupervisorPTYState(t *testing.T,path string)supervisorPTYTestState{
 t.Helper();deadline:=time.Now().Add(5*time.Second)
 for time.Now().Before(deadline){data,err:=os.ReadFile(path);if err==nil{var state supervisorPTYTestState;if json.Unmarshal(data,&state)==nil{return state}};time.Sleep(10*time.Millisecond)}
 t.Fatalf("timed out waiting for PTY state %s",path);return supervisorPTYTestState{}
}
func supervisorPTYRequest(t *testing.T,socket,command string)string{
 t.Helper();conn,err:=net.DialTimeout("unix",socket,time.Second);if err!=nil{t.Fatal(err)}
 defer conn.Close();_=conn.SetDeadline(time.Now().Add(5*time.Second))
 if _,err:=fmt.Fprintln(conn,command);err!=nil{t.Fatal(err)}
 line,err:=bufio.NewReader(conn).ReadString('\n');if err!=nil{t.Fatal(err)}
 return strings.TrimSpace(line)
}
func waitSupervisorPIDGone(pid int)bool{
 if pid<=0{return true};deadline:=time.Now().Add(2*time.Second)
 for time.Now().Before(deadline){if unix.Kill(pid,0)==unix.ESRCH{return true};time.Sleep(20*time.Millisecond)}
 return false
}

func TestShipSupervisorPTYChildRestartPreservesRunnerAndTerminal(t *testing.T){
 master,slave:=openSupervisorTestPTY(t);defer master.Close()
 dir:=t.TempDir();socketDir,err:=os.MkdirTemp("", "sfpty-");if err!=nil{t.Fatal(err)};defer os.RemoveAll(socketDir);socket:=filepath.Join(socketDir,"s.sock");statePath:=filepath.Join(dir,"state.json");descPath:=filepath.Join(dir,"descendant.pid")
 cmd:=exec.Command(os.Args[0],"-test.run=^TestShipSupervisorPTYHelper$")
 cmd.Env=append(os.Environ(),"STARFLEET_TEST_PTY_HELPER=1",
		"STARFLEET_TEST_DUMP_SCREEN=1","STARFLEET_TEST_PTY_SOCKET="+socket,"STARFLEET_TEST_PTY_STATE="+statePath,"STARFLEET_TEST_PTY_DESC="+descPath)
 cmd.Stdin,cmd.Stdout,cmd.Stderr=slave,slave,slave
 cmd.SysProcAttr=&syscall.SysProcAttr{Setsid:true,Setctty:true,Ctty:0}
 if err:=cmd.Start();err!=nil{t.Fatal(err)};_=slave.Close()
 defer func(){if cmd.Process!=nil{_=cmd.Process.Kill();_=cmd.Wait()}}()
 first:=readSupervisorPTYState(t,statePath)
 if first.RunnerPID!=cmd.Process.Pid||first.ChildPID==0||first.ForegroundPID!=first.ChildPID{t.Fatalf("initial PTY ownership invalid: %+v",first)}
 if first.SocketIno==0||first.PTYIno==0||first.DescendantPID==0{t.Fatalf("missing PTY/socket/descendant evidence: %+v",first)}
 if got:=supervisorPTYRequest(t,socket,"RESTART");got!="OK"{t.Fatalf("restart response=%q",got)}
 second:=readSupervisorPTYState(t,statePath)
 if second.RunnerPID!=first.RunnerPID||second.ChildPID==0||second.ChildPID==first.ChildPID{t.Fatalf("runner/child identity after restart: first=%+v second=%+v",first,second)}
 if second.ForegroundPID!=second.ChildPID{t.Fatalf("replacement child is not PTY foreground: %+v",second)}
 if second.PTYDev!=first.PTYDev||second.PTYIno!=first.PTYIno||second.PTYRdev!=first.PTYRdev||second.SocketIno!=first.SocketIno||second.TermiosLflag!=first.TermiosLflag{t.Fatalf("PTY/socket identity changed: first=%+v second=%+v",first,second)}
 if !waitSupervisorPIDGone(first.DescendantPID){t.Fatalf("old child descendant %d survived restart",first.DescendantPID)}
 if _,err:=master.Write([]byte{3});err!=nil{t.Fatal(err)}
 var after supervisorPTYTestState
 deadline:=time.Now().Add(5*time.Second)
 for time.Now().Before(deadline){
  _=supervisorPTYRequest(t,socket,"STATUS")
  after=readSupervisorPTYState(t,statePath)
  if after.ChildPID==0&&after.ForegroundPID==after.RunnerPID{break}
  time.Sleep(20*time.Millisecond)
 }
 if after.ChildPID!=0||after.ForegroundPID!=after.RunnerPID||after.TermiosLflag!=first.TermiosLflag{t.Fatalf("Ctrl-C did not restore runner foreground: %+v",after)}
 if !waitSupervisorPIDGone(second.DescendantPID){t.Fatalf("Ctrl-C left descendant %d alive",second.DescendantPID)}
 if got:=supervisorPTYRequest(t,socket,"EXIT");got!="BYE"{t.Fatalf("exit response=%q",got)}
 if err:=cmd.Wait();err!=nil{t.Fatalf("PTY helper exit: %v",err)}
}

func TestTermctlRunPTYHelper(t *testing.T) {
	if os.Getenv("STARFLEET_TEST_TERMCTL_HELPER") != "1" { return }
	root := os.Getenv("MPBT_WORKSPACE_ROOT")
	_ = os.Setenv("STARFLEET_TEST_PTY_HELPER", "1")
	self, err := os.Executable()
	if err != nil { t.Fatal(err) }
	shipID := "TestPTY"
	pipePath := PipePath(root, shipID)
	shellCmd := "cd " + shellQuote(root) + "; export STARFLEET_TEST_PTY_HELPER=1; exec " + shellQuote(self) + " " + shellQuote("-test.run=TestShipSupervisorPTYHelper")
	if code := RunTermctl(root, []string{shipID, pipePath, shellCmd}); code != 0 {
		t.Fatalf("RunTermctl returned %d", code)
	}
}

func readPTYScrollback(t *testing.T, remote *termctl.Remote, path string, markers ...string) []string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		_ = os.Remove(path)
		if err := remote.Send("dump-scrollback 200 " + path); err != nil {
			t.Fatalf("request scrollback dump: %v", err)
		}
		readDeadline := time.Now().Add(150 * time.Millisecond)
		for time.Now().Before(readDeadline) {
			data, err := os.ReadFile(path)
			if err == nil {
				var lines []string
				if json.Unmarshal(data, &lines) == nil {
					allPresent := true
					for _, marker := range markers {
						if !containsPTYLine(lines, marker) { allPresent = false; break }
					}
					if allPresent { return lines }
				}
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	t.Fatalf("timed out waiting for scrollback markers %v in %s", markers, path)
	return nil
}
func containsPTYLine(lines []string, marker string) bool {
	for _, line := range lines { if strings.Contains(line, marker) { return true } }
	return false
}

// This exercises the real termctl owner around the in-PTY runner: the outer
// process owns TermHandle, the FIFO control pipe and scrollback buffer while
// the inner runner replaces only its OpenCode child.

func supervisorPTYStateWhileRunning(t *testing.T, path string, waitDone <-chan struct{}, waitErr *error) supervisorPTYTestState {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-waitDone:
			t.Fatalf("termctl-run helper exited before PTY state was ready: %v", *waitErr)
		default:
		}
		if data, err := os.ReadFile(path); err == nil {
			var state supervisorPTYTestState
			if json.Unmarshal(data, &state) == nil { return state }
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for PTY state %s", path)
	return supervisorPTYTestState{}
}

func TestTermctlRunPreservesParentControlPipePTYAndScrollbackAcrossRestart(t *testing.T) {
	root := t.TempDir()
	shipID := "TestPTY"
	pipePath := PipePath(root, shipID)
	socketDir, err := os.MkdirTemp("", "sfpty-")
	if err != nil { t.Fatal(err) }
	defer os.RemoveAll(socketDir)
	socket := filepath.Join(socketDir, "s.sock")
	statePath := filepath.Join(root, "state.json")
	descPath := filepath.Join(root, "descendant.pid")
	if err := os.MkdirAll(filepath.Dir(pipePath), 0755); err != nil { t.Fatal(err) }

	self, err := os.Executable()
	if err != nil { t.Fatal(err) }
	cmd := exec.Command(self, "-test.run=^TestTermctlRunPTYHelper$")
	cmd.Env = append(os.Environ(),
		"STARFLEET_TEST_TERMCTL_HELPER=1",
		"STARFLEET_TEST_PTY_HELPER=1",
		"MPBT_WORKSPACE_ROOT="+root,
		"STARFLEET_TEST_PTY_SOCKET="+socket,
		"STARFLEET_TEST_PTY_STATE="+statePath,
		"STARFLEET_TEST_PTY_DESC="+descPath,
	)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil { t.Fatal(err) }
	waitDone := make(chan struct{})
	var waitErr error
	go func() { waitErr = cmd.Wait(); close(waitDone) }()
	waited := false
	defer func() {
		if !waited {
			if conn, err := net.DialTimeout("unix", socket, 300*time.Millisecond); err == nil {
				_, _ = fmt.Fprintln(conn, "EXIT")
				_ = conn.Close()
			}
			if remote, err := termctl.OpenPipe(pipePath); err == nil {
				_ = remote.Stop()
			}
			select {
			case <-waitDone:
			case <-time.After(3 * time.Second):
				_ = cmd.Process.Kill()
				<-waitDone
			}
		}
	}()
	first := supervisorPTYStateWhileRunning(t, statePath, waitDone, &waitErr)
	if first.RunnerPID == 0 || first.ChildPID == 0 || first.ForegroundPID != first.ChildPID { t.Fatalf("initial termctl PTY state invalid: %+v", first) }
	pidData, err := os.ReadFile(PidPath(root, shipID))
	if err != nil { t.Fatalf("read termctl-run PID file: %v", err) }
	termctlPID, err := strconv.Atoi(strings.TrimSpace(string(pidData)))
	if err != nil || termctlPID != cmd.Process.Pid { t.Fatalf("termctl-run PID file=%q process=%d err=%v", pidData, cmd.Process.Pid, err) }
	pipeInfo, err := os.Stat(pipePath)
	if err != nil { t.Fatalf("stat termctl control pipe: %v", err) }
	pipeIno := pipeInfo.Sys().(*syscall.Stat_t).Ino
	remote, err := termctl.OpenPipe(pipePath)
	if err != nil { t.Fatal(err) }
	dump1 := filepath.Join(root, "scrollback-before.json")
	lines1 := readPTYScrollback(t, remote, dump1, "SUPERVISOR-PTY-FIRST")
	if !containsPTYLine(lines1, "SUPERVISOR-PTY-FIRST") { t.Fatalf("initial marker missing from real termctl scrollback: %v", lines1) }

	if got := supervisorPTYRequest(t, socket, "RESTART"); got != "OK" { t.Fatalf("restart response=%q", got) }
	second := readSupervisorPTYState(t, statePath)
	if second.RunnerPID != first.RunnerPID || second.ChildPID == 0 || second.ChildPID == first.ChildPID { t.Fatalf("termctl PTY runner/child changed unexpectedly: first=%+v second=%+v", first, second) }
	if second.PTYDev != first.PTYDev || second.PTYIno != first.PTYIno || second.PTYRdev != first.PTYRdev || second.SocketIno != first.SocketIno { t.Fatalf("PTY or supervisor socket identity changed: first=%+v second=%+v", first, second) }
	pidData, err = os.ReadFile(PidPath(root, shipID))
	if err != nil { t.Fatalf("termctl-run PID file disappeared across restart: %v", err) }
	if got, _ := strconv.Atoi(strings.TrimSpace(string(pidData))); got != termctlPID { t.Fatalf("termctl-run parent PID changed across restart: before=%d after=%s", termctlPID, strings.TrimSpace(string(pidData))) }
	pipeInfo, err = os.Stat(pipePath)
	if err != nil { t.Fatalf("termctl control pipe disappeared across restart: %v", err) }
	if got := pipeInfo.Sys().(*syscall.Stat_t).Ino; got != pipeIno { t.Fatalf("termctl control pipe inode changed: before=%d after=%d", pipeIno, got) }
	dump2 := filepath.Join(root, "scrollback-after.json")
	lines2 := readPTYScrollback(t, remote, dump2, "SUPERVISOR-PTY-FIRST", "SUPERVISOR-PTY-SECOND")
	if !containsPTYLine(lines2, "SUPERVISOR-PTY-FIRST") || !containsPTYLine(lines2, "SUPERVISOR-PTY-SECOND") { t.Fatalf("scrollback was not preserved across child restart: %v", lines2) }
	if !waitSupervisorPIDGone(first.DescendantPID) { t.Fatalf("old child descendant %d survived real termctl restart", first.DescendantPID) }
	if got := supervisorPTYRequest(t, socket, "EXIT"); got != "BYE" { t.Fatalf("exit response=%q", got) }
	select {
	case <-waitDone:
		waited = true
		if waitErr != nil { t.Fatalf("termctl-run helper exit: %v", waitErr) }
	case <-time.After(5 * time.Second):
		t.Fatal("termctl-run owner did not exit after the PTY helper EXIT acknowledgement")
	}
	if _, err := os.Stat(pipePath); !os.IsNotExist(err) { t.Fatalf("termctl control pipe not cleaned after parent exit: %v", err) }
}

func TestSupervisorStatusReadyRequiresFreshCorrelatedPluginAndSession(t *testing.T) {
	startedAt := time.Now()
	observedAt := startedAt.Add(time.Second).Format(time.RFC3339Nano)
	child := &supervisedChild{pid: 4242, startedAt: startedAt}
	base := comms.StatusRecord{
		PID: 4242,
		PluginStartPID: 4242,
		PluginStartEventID: "plugin-start:Voyager:4242:2026-11-27T18:00:00Z",
		PluginStartAt: observedAt,
		PluginVersion: "test-version",
		PluginLastRun: observedAt,
		PluginHeartbeatMS: 5000,
		RuntimeSessionID: "session-123",
		Model: "model-x",
		Server: "provider-x",
	}
	if !supervisorStatusReady(base, child, "provider-x/model-x") {
		t.Fatal("fresh, process-correlated plugin/model/session record was rejected")
	}
	cases := []struct {
		name string
		mutate func(*comms.StatusRecord)
	}{
		{"wrong process pid", func(r *comms.StatusRecord) { r.PID++ }},
		{"wrong plugin pid", func(r *comms.StatusRecord) { r.PluginStartPID++ }},
		{"not a plugin-start event", func(r *comms.StatusRecord) { r.PluginStartEventID = "runtime:starting" }},
		{"missing plugin version", func(r *comms.StatusRecord) { r.PluginVersion = "" }},
		{"missing heartbeat", func(r *comms.StatusRecord) { r.PluginHeartbeatMS = 0 }},
		{"missing session id", func(r *comms.StatusRecord) { r.RuntimeSessionID = "" }},
		{"stale plugin start", func(r *comms.StatusRecord) { r.PluginStartAt = startedAt.Add(-3*time.Second).Format(time.RFC3339Nano) }},
		{"stale health", func(r *comms.StatusRecord) { r.PluginLastRun = startedAt.Add(-3*time.Second).Format(time.RFC3339Nano) }},
		{"invalid timestamp", func(r *comms.StatusRecord) { r.PluginStartAt = "not-a-timestamp" }},
		{"wrong effective model", func(r *comms.StatusRecord) { r.Model = "other-model" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := base
			tc.mutate(&rec)
			if supervisorStatusReady(rec, child, "provider-x/model-x") {
				t.Fatalf("accepted invalid readiness record: %+v", rec)
			}
		})
	}
}

func TestSupervisorRestartRefusesWithoutConfirmedSession(t *testing.T) {
	s := &shipSupervisor{currentModel: "provider-x/model-x"}
	if err := s.restart("provider-x/other-model"); err == nil || !strings.Contains(err.Error(), "session ID") {
		t.Fatalf("restart without a confirmed session ID returned %v", err)
	}
	if s.child != nil {
		t.Fatal("restart without a session ID touched a child process")
	}
}
