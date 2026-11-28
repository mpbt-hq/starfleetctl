// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package session

import (
 "bufio"
 "encoding/json"
 "errors"
 "fmt"
 "io"
 "net"
 "os"
 "os/exec"
 "os/signal"
 "path/filepath"
 "strconv"
 "strings"
 "syscall"
 "time"

 "github.com/metux/starfleetctl/internal/comms"
 "golang.org/x/sys/unix"
)

const (
 supervisorReadyTimeout = 120 * time.Second
 supervisorControlLimit = 1024
 supervisorStopGrace = 350 * time.Millisecond
 supervisorShutdownGrace = 100 * time.Millisecond
 supervisorKillWait = 200 * time.Millisecond
)

type shipSupervisorOptions struct {
 shipID, model, launchType, parent, class string
 unrestricted, forceNew bool
}
type supervisedChild struct {
 cmd *exec.Cmd
 pid int
 startedAt time.Time
 done chan error
}
type shipSupervisor struct {
 root string
 opts shipSupervisorOptions
 ttyFD, runnerPGID int
 initialTermios *unix.Termios
 currentModel string
 currentSessionID string
 currentConfigPath string
 child *supervisedChild
 signals chan os.Signal
 shutdown bool
}

func RunShipSupervisor(root string, args []string) int {
 if len(args) != 7 {
  fmt.Fprintln(os.Stderr, "ship-supervisor: need <ship-id> <model> <launch-type> <parent> <class> <unrestricted> <new>")
  return 2
 }
 unrestricted, err := strconv.ParseBool(args[5]); if err != nil { fmt.Fprintln(os.Stderr, err); return 2 }
 forceNew, err := strconv.ParseBool(args[6]); if err != nil { fmt.Fprintln(os.Stderr, err); return 2 }
 if root == "" { root = os.Getenv("MPBT_WORKSPACE_ROOT") }
 if root == "" { fmt.Fprintln(os.Stderr, "ship-supervisor: MPBT_WORKSPACE_ROOT is not set"); return 1 }
 s := &shipSupervisor{root:root, ttyFD:int(os.Stdin.Fd()), signals:make(chan os.Signal,4),
  opts:shipSupervisorOptions{shipID:args[0],model:args[1],launchType:args[2],parent:args[3],class:args[4],unrestricted:unrestricted,forceNew:forceNew}}
 if err := s.run(); err != nil { fmt.Fprintln(os.Stderr,"ship-supervisor:",err); return 1 }
 return 0
}

// RunShipExec waits until the supervisor has transferred PTY foreground
// ownership, then replaces this helper with OpenCode without changing PID/PGID.
func RunShipExec(args []string) int {
 if len(args)<1 { fmt.Fprintln(os.Stderr,"ship-exec: need executable"); return 2 }
 ready:=os.NewFile(3,"ship-exec-ready")
 if ready==nil { fmt.Fprintln(os.Stderr,"ship-exec: missing release pipe"); return 1 }
 var one [1]byte
 if _,err:=io.ReadFull(ready,one[:]); err!=nil { _=ready.Close(); fmt.Fprintln(os.Stderr,err); return 1 }
 _=ready.Close()
 path:=args[0]
 if err:=syscall.Exec(path,append([]string{path},args[1:]...),os.Environ()); err!=nil { fmt.Fprintln(os.Stderr,"ship-exec:",err); return 127 }
 return 0
}

func (s *shipSupervisor) run() error {
 defer s.removeCurrentConfig()
 s.runnerPGID,_=unix.Getpgid(0)
 sid,sidErr:=unix.Getsid(0)
 if sidErr!=nil || s.runnerPGID!=os.Getpid() || sid!=os.Getpid() {
  return fmt.Errorf("runner must be PTY session/process-group leader (pid=%d pgid=%d sid=%d err=%v)",os.Getpid(),s.runnerPGID,sid,sidErr)
 }
 if _,err:=unix.IoctlGetInt(s.ttyFD,unix.TIOCGPGRP); err!=nil { return fmt.Errorf("stdin is not a controlling PTY: %w",err) }
 initialTermios,termErr:=unix.IoctlGetTermios(s.ttyFD,unix.TCGETS)
 if termErr!=nil{return fmt.Errorf("read initial PTY termios: %w",termErr)}
 s.initialTermios=initialTermios
 signal.Ignore(syscall.SIGTTOU)
 signal.Notify(s.signals,syscall.SIGTERM,syscall.SIGHUP,syscall.SIGINT)
 defer signal.Stop(s.signals)
 if err:=s.startChild(s.opts.model,s.opts.forceNew); err!=nil { return err }
 if err:=s.waitReady(supervisorReadyTimeout); err!=nil { _=s.stopChild(); return fmt.Errorf("initial OpenCode readiness failed: %w",err) }
 s.opts.forceNew=false
 socketPath:=SocketPath(s.root,s.opts.shipID)
 if err:=os.MkdirAll(filepath.Dir(socketPath),0755); err!=nil { _=s.stopChild(); return err }
 _=os.Remove(socketPath)
 listener,err:=net.Listen("unix",socketPath); if err!=nil { _=s.stopChild(); return err }
 defer func(){ _=listener.Close(); _=os.Remove(socketPath) }()
 requests:=make(chan net.Conn,1)
 go func(){ for { c,e:=listener.Accept(); if e!=nil{return}; requests<-c } }()
 for {
  select {
  case sig:=<-s.signals:
   fmt.Fprintf(os.Stderr,"ship-supervisor: received %s; stopping child group %d\n",sig,s.childPID())
   _=s.stopChildGrace(supervisorShutdownGrace); return nil
  case childErr:=<-s.childDone():
   s.finishExitedChild()
   if processExitedBySignal(childErr,syscall.SIGINT) {
    // The terminal sends Ctrl-C to the foreground child process group, not
    // this background supervisor. Keep the PTY owner and control socket
    // alive with the runner foreground so STATUS/RESTART/STOP remain usable.
    fmt.Fprintln(os.Stderr,"ship-supervisor: OpenCode interrupted; supervisor remains available")
    continue
   }
   if childErr!=nil { return fmt.Errorf("OpenCode exited: %w",childErr) }; return nil
  case c:=<-requests: s.handleControl(c); if s.shutdown { return nil }
  }
 }
}
func(s *shipSupervisor) childPID()int{if s.child==nil{return 0};return s.child.pid}
func(s *shipSupervisor) childDone()<-chan error{if s.child==nil{return nil};return s.child.done}

func(s *shipSupervisor) prepareChild(model string)(string,string,error){
 if model==""{model=s.currentModel};if model==""{model=s.opts.model};if model==""{model="nvidia/nemotron-3-ultra-550b-a55b"}
 if err:=validateModelAvailable(s.root,model);err!=nil{return "","",fmt.Errorf("model validation failed: %w",err)}
 configPath:=filepath.Join(s.root,".starfleet-ai","var","ships",fmt.Sprintf("%s.opencode.%d.%d.json",s.opts.shipID,os.Getpid(),time.Now().UnixNano()))
 path,err:=generateOpencodeConfigAt(s.root,s.opts.shipID,s.opts.launchType,s.opts.unrestricted,model,s.opts.class,configPath);if err!=nil{return "","",err}
 data,err:=os.ReadFile(path);if err!=nil{_=os.Remove(path);return "","",err}
 var cfg map[string]any;if err:=json.Unmarshal(data,&cfg);err!=nil{_=os.Remove(path);return "","",err}
 effective,_:=cfg["model"].(string);if effective==""{_=os.Remove(path);return "","",errors.New("generated config has no effective model")}
 return path,effective,nil
}
func(s *shipSupervisor) startChild(model string,forceNew bool)error{
 configPath,effective,err:=s.prepareChild(model);if err!=nil{return err}
 sessionID:=""
 if !forceNew {
  sessionID,err=findLastSessionForShip(s.root,s.opts.shipID)
  if err!=nil{_=os.Remove(configPath);return fmt.Errorf("resolve existing OpenCode session: %w",err)}
 }
 if err:=s.startPreparedChild(configPath,effective,sessionID);err!=nil{_=os.Remove(configPath);return err};return nil
}
func(s *shipSupervisor) startChildWithSession(model,sessionID string)error{
 if sessionID==""{return errors.New("cannot resume OpenCode without a confirmed session ID")}
 configPath,effective,err:=s.prepareChild(model);if err!=nil{return err}
 if err:=s.startPreparedChild(configPath,effective,sessionID);err!=nil{_=os.Remove(configPath);return err};return nil
}
func(s *shipSupervisor) startPreparedChild(configPath,effective,sessionID string)error{
 self,err:=os.Executable();if err!=nil{return err}
 clientPath:=resolveClientPath("opencode")
 args:=[]string{"--model",effective}
 if sessionID!="" {args=append(args,"--session",sessionID)} else {
  parent:=s.opts.parent;if parent==""{parent="Enterprise"}
  prompt:="You are fleet ship "+s.opts.shipID+", report to flagship "+parent+"."
  if s.opts.launchType=="background"||s.opts.launchType=="auto"{prompt+=" You were launched in "+s.opts.launchType+" mode (detached, no human at your console): never prompt for input on your console and never block waiting for stdin — communicate only via the agent bus (starfleetctl comms tell/ask). Act autonomously and report results back over the bus."}
  args=append(args,"--prompt",prompt)
 }
 readyR,readyW,err:=os.Pipe();if err!=nil{return err}
 cmd:=exec.Command(self,append([]string{"ship-exec",clientPath},args...)...)
 cmd.ExtraFiles=[]*os.File{readyR}
 cmd.Stdin,cmd.Stdout,cmd.Stderr=os.Stdin,os.Stdout,os.Stderr
 cmd.SysProcAttr=&syscall.SysProcAttr{Setpgid:true}
 env:=replaceSupervisorEnv(os.Environ(),"OPENCODE_CONFIG",configPath)
 env=replaceSupervisorEnv(env,"OPENCODE_DISABLE_CLAUDE_CODE_PROMPT","1")
 cmd.Env=env
 child, startErr:=startForegroundProcess(s.ttyFD,cmd,func()error{
  _=readyR.Close()
  _,writeErr:=readyW.Write([]byte{1})
  _=readyW.Close()
  return writeErr
 })
 if startErr!=nil{_=readyR.Close();_=readyW.Close();_=s.restoreForeground();return startErr}
 s.child=child;s.currentModel=effective;s.currentSessionID=sessionID;s.currentConfigPath=configPath
 fmt.Fprintf(os.Stderr,"ship-supervisor: OpenCode child pid=%d model=%s session=%s\n",child.pid,effective,sessionID)
 return nil
}

func startForegroundProcess(ttyFD int, cmd *exec.Cmd, release func() error) (*supervisedChild,error) {
 if cmd.SysProcAttr==nil { cmd.SysProcAttr=&syscall.SysProcAttr{} }
 cmd.SysProcAttr.Setpgid=true
 if err:=cmd.Start();err!=nil{return nil,err}
 pid:=cmd.Process.Pid
 if err:=unix.IoctlSetPointerInt(ttyFD,unix.TIOCSPGRP,pid);err!=nil{
  _=unix.Kill(-pid,unix.SIGKILL);_=cmd.Wait()
  return nil,fmt.Errorf("foreground handoff to process group %d: %w",pid,err)
 }
 if release!=nil { if err:=release();err!=nil{_=unix.Kill(-pid,unix.SIGKILL);_=cmd.Wait();return nil,fmt.Errorf("release child after foreground handoff: %w",err)} }
 child:=&supervisedChild{cmd:cmd,pid:pid,startedAt:time.Now(),done:make(chan error,1)}
 go func(){child.done<-cmd.Wait()}()
 return child,nil
}

func(s *shipSupervisor) waitReady(timeout time.Duration)error{
 if s.child==nil{return errors.New("no OpenCode child")}
 pid:=s.child.pid;timer:=time.NewTimer(timeout);defer timer.Stop();tick:=time.NewTicker(100*time.Millisecond);defer tick.Stop()
 for {select{
 case err:=<-s.child.done:s.finishExitedChild();if err==nil{return errors.New("OpenCode exited before plugin readiness")};return fmt.Errorf("OpenCode exited before plugin readiness: %w",err)
 case sig:=<-s.signals:s.shutdown=true;_=s.stopChildGrace(supervisorShutdownGrace);return fmt.Errorf("interrupted by %s",sig)
 case <-timer.C:return fmt.Errorf("timed out waiting for plugin/model readiness for pid %d",pid)
 case <-tick.C:
  bus,err:=comms.New(s.root);if err!=nil{continue}
  for _,rec:=range bus.AllStatusRecords(){
   if !supervisorStatusReady(rec,s.child,s.currentModel){continue}
   s.currentSessionID=rec.RuntimeSessionID
   fmt.Fprintf(os.Stderr,"ship-supervisor: ready pid=%d plugin=%s model=%s session=%s\n",pid,rec.PluginVersion,s.currentModel,s.currentSessionID)
   return nil
  }
 }}
}

func processExitedBySignal(err error,want syscall.Signal)bool{
 var exitErr *exec.ExitError
 if !errors.As(err,&exitErr){return false}
 status,ok:=exitErr.Sys().(syscall.WaitStatus)
 return ok&&status.Signaled()&&status.Signal()==want
}

func supervisorStatusReady(rec comms.StatusRecord,child *supervisedChild,expectedModel string)bool{
 if child==nil||rec.PID!=child.pid||rec.PluginStartPID!=child.pid{return false}
 if !strings.HasPrefix(rec.PluginStartEventID,"plugin-start:")||rec.PluginVersion==""||rec.PluginHeartbeatMS<=0||rec.RuntimeSessionID==""{return false}
 freshnessFloor:=child.startedAt.Add(-2*time.Second)
 pluginStartAt,err:=time.Parse(time.RFC3339Nano,rec.PluginStartAt);if err!=nil||pluginStartAt.Before(freshnessFloor){return false}
 pluginLastRun,err:=time.Parse(time.RFC3339Nano,rec.PluginLastRun);if err!=nil||pluginLastRun.Before(freshnessFloor){return false}
 return supervisorModelMatches(rec,expectedModel)
}

func supervisorModelMatches(rec comms.StatusRecord,expected string)bool{
 model:=strings.TrimSpace(rec.Model);server:=strings.TrimSpace(rec.Server)
 if model==expected{return true};return server!=""&&server+"/"+strings.TrimPrefix(model,server+"/")==expected
}
func(s *shipSupervisor)restoreTerminal()error{if s.initialTermios==nil{return nil};return unix.IoctlSetTermios(s.ttyFD,unix.TCSETS,s.initialTermios)}
func(s *shipSupervisor)restoreForeground()error{if err:=s.restoreTerminal();err!=nil{return err};return unix.IoctlSetPointerInt(s.ttyFD,unix.TIOCSPGRP,s.runnerPGID)}
func(s *shipSupervisor)stopChild()error{return s.stopChildGrace(supervisorStopGrace)}
func(s *shipSupervisor)stopChildGrace(grace time.Duration)error{
 child:=s.child;if child==nil{s.removeCurrentConfig();return nil};pgid:=child.pid
 _=unix.Kill(-pgid,unix.SIGTERM)
 select{case <-child.done:case <-time.After(grace):
  _=unix.Kill(-pgid,unix.SIGKILL)
  select{case <-child.done:case <-time.After(supervisorKillWait):return fmt.Errorf("child process group %d did not exit",pgid)}
 }
 terminateProcessGroup(pgid,supervisorShutdownGrace);s.child=nil;s.removeCurrentConfig();return s.restoreForeground()
}
func terminateProcessGroup(pgid int,grace time.Duration){
 if pgid<=0{return};_=unix.Kill(-pgid,unix.SIGTERM);deadline:=time.Now().Add(grace)
 for time.Now().Before(deadline){if unix.Kill(-pgid,0)==unix.ESRCH{return};time.Sleep(10*time.Millisecond)}
 _=unix.Kill(-pgid,unix.SIGKILL)
}
func replaceSupervisorEnv(base []string,key,value string)[]string{
 prefix:=key+"=";out:=make([]string,0,len(base)+1)
 for _,entry:=range base{if !strings.HasPrefix(entry,prefix){out=append(out,entry)}}
 return append(out,prefix+value)
}
func(s *shipSupervisor)finishExitedChild(){
 if s.child==nil{s.removeCurrentConfig();return};pgid:=s.child.pid;terminateProcessGroup(pgid,supervisorShutdownGrace);s.child=nil;s.removeCurrentConfig()
 if err:=s.restoreForeground();err!=nil{fmt.Fprintln(os.Stderr,"ship-supervisor:",err)}
}
func(s *shipSupervisor)handleControl(conn net.Conn){
 defer conn.Close();_=conn.SetReadDeadline(time.Now().Add(5*time.Second));_=conn.SetWriteDeadline(time.Now().Add(supervisorReadyTimeout+10*time.Second))
 reader:=bufio.NewReaderSize(conn,supervisorControlLimit);line,err:=reader.ReadSlice('\n')
 if err!=nil{_,_=fmt.Fprintf(conn,"ERROR invalid request: %v\n",err);return}
 if len(line)>supervisorControlLimit{_,_=io.WriteString(conn,"ERROR request too large\n");return}
 fields:=strings.Fields(string(line));if len(fields)==0{_,_=io.WriteString(conn,"ERROR empty request\n");return}
 switch strings.ToUpper(fields[0]){
 case "STATUS":if s.child==nil{_,_=io.WriteString(conn,"STOPPED\n")}else{_,_=fmt.Fprintf(conn,"RUNNING pid=%d model=%s\n",s.child.pid,s.currentModel)}
 case "RESTART":
  model:="";for _,f:=range fields[1:]{if strings.HasPrefix(f,"model="){model=strings.TrimPrefix(f,"model=")}}
  if err:=s.restart(model);err!=nil{_,_=fmt.Fprintf(conn,"ERROR %s\n",strings.ReplaceAll(err.Error(),"\n"," "));return}
  _,_=fmt.Fprintf(conn,"OK model=%s pid=%d\n",s.currentModel,s.childPID())
 default:_,_=io.WriteString(conn,"ERROR unsupported command\n")
 }
}
func(s *shipSupervisor)restart(model string)error{
 oldModel:=s.currentModel
 sessionID:=s.currentSessionID
 if sessionID==""{return errors.New("no confirmed OpenCode session ID; refusing restart to avoid duplicating the initial prompt")}
 // Do all fallible model validation/config generation before touching the old child.
 configPath,effective,err:=s.prepareChild(model);if err!=nil{return err}
 if err:=s.stopChild();err!=nil{_=os.Remove(configPath);return fmt.Errorf("stop old child group: %w",err)}
 if err:=s.startPreparedChild(configPath,effective,sessionID);err!=nil{
  _=os.Remove(configPath)
  if !s.shutdown&&oldModel!="" { if rollbackErr:=s.startChildWithSession(oldModel,sessionID);rollbackErr==nil { rollbackErr=s.waitReady(supervisorReadyTimeout);if rollbackErr==nil{return fmt.Errorf("replacement start failed; restored previous model %s: %w",oldModel,err)};_=s.stopChild() } }
  return fmt.Errorf("start replacement child: %w",err)
 }
 if err:=s.waitReady(supervisorReadyTimeout);err!=nil{
  _=s.stopChild()
  if s.shutdown { return err }
  if oldModel!="" {
   if rollbackErr:=s.startChildWithSession(oldModel,sessionID);rollbackErr==nil {
    rollbackErr=s.waitReady(supervisorReadyTimeout)
    if rollbackErr==nil{return fmt.Errorf("replacement readiness failed; restored previous model %s: %w",oldModel,err)}
    _=s.stopChild()
    return fmt.Errorf("replacement readiness failed (%v); rollback to %s failed: %w",err,oldModel,rollbackErr)
   }
  }
  return fmt.Errorf("replacement readiness failed: %w",err)
 }
 return nil
}
func(s *shipSupervisor)removeCurrentConfig(){
 if s.currentConfigPath!=""{_=os.Remove(s.currentConfigPath);s.currentConfigPath=""}
}


