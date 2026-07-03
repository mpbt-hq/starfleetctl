// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package agentbus

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// nextID allocates the next monotonic zero-padded message id (m0001, m0002,
// …) from MsgDir/.seq. Caller must already hold the bus lock, matching
// bash's next_id() which only runs inside lock_bus'd callers.
func (b *Bus) nextID() (string, error) {
	seqFile := filepath.Join(b.MsgDir, ".seq")
	n := int64(0)
	if data, err := os.ReadFile(seqFile); err == nil {
		line := strings.TrimSpace(string(data))
		if v, err := strconv.ParseInt(line, 10, 64); err == nil {
			n = v
		}
	}
	n++
	if err := os.WriteFile(seqFile, []byte(fmt.Sprintf("%d\n", n)), 0o644); err != nil {
		return "", err
	}
	return fmt.Sprintf("m%04d", n), nil
}

// post writes a new directive/question record and returns its id. Caller
// must not hold the bus lock — post takes it itself, mirroring _post().
func (b *Bus) post(target, text string) (string, error) {
	lock, err := b.lockBus()
	if err != nil {
		return "", err
	}
	defer lock.Close()
	id, err := b.nextID()
	if err != nil {
		return "", err
	}
	line := fmt.Sprintf("%d\t%s\t%s\t%s\t%s\n", now(), isots(), b.AgentID, target, text)
	if err := os.WriteFile(b.mfile(id), []byte(line), 0o644); err != nil {
		return "", err
	}
	b.logEvent("directive", fmt.Sprintf("%s → %s: %s", id, target, text))
	return id, nil
}

// DoStatus implements `agent-bus status <state> [note]`.
func (b *Bus) DoStatus(state, note string) error {
	if state == "" {
		return usageErr("agent-bus: status needs a <state> (e.g. working|building|blocked|idle|done)")
	}
	b.warnID()
	lock, err := b.lockBus()
	if err != nil {
		return err
	}
	defer lock.Close()
	pp := clean(b.Project)
	if pp == "" {
		pp = "-"
	}
	hh := clean(b.Handle)
	if hh == "" {
		hh = "-"
	}
	line := fmt.Sprintf("%d\t%s\t%s\t%s\t%s\t%d\t%s\t%s\n",
		now(), isots(), b.AgentID, pp, clean(state), os.Getpid(), hh, clean(note))
	if err := os.WriteFile(b.sfile(b.AgentID), []byte(line), 0o644); err != nil {
		return err
	}
	proj := ""
	if b.Project != "" {
		proj = fmt.Sprintf("[%s] ", b.Project)
	}
	b.logEvent("status", fmt.Sprintf("%s %s%s", state, proj, note))
	suffix := ""
	if b.Project != "" {
		suffix = fmt.Sprintf(" (%s)", b.Project)
	}
	noteSuffix := ""
	if note != "" {
		noteSuffix = " — " + note
	}
	fmt.Printf("agent-bus: '%s'%s → %s%s\n", b.AgentID, suffix, state, noteSuffix)
	return nil
}

// DoClear implements `agent-bus clear`.
func (b *Bus) DoClear() error {
	lock, err := b.lockBus()
	if err != nil {
		return err
	}
	defer lock.Close()
	_ = os.Remove(b.sfile(b.AgentID))
	b.logEvent("clear", "")
	fmt.Printf("agent-bus: cleared heartbeat for '%s'\n", b.AgentID)
	return nil
}

// DoInbox implements `agent-bus inbox`.
func (b *Bus) DoInbox() error {
	found := false
	for _, m := range b.allMsgRecords() {
		if m.Target != "all" && m.Target != b.AgentID {
			continue
		}
		if !found {
			fmt.Printf("%-6s  %-7s  %-18s  %-4s  %s\n", "ID", "AGE", "FROM", "ACK", "WHAT")
			found = true
		}
		a := " "
		if b.acked(m.ID, b.AgentID) {
			a = "✓"
		}
		fmt.Printf("%-6s  %-7s  %-18s  %-4s  %s\n", m.ID, age(m.Epoch), m.From, a, m.Text)
	}
	if !found {
		fmt.Printf("(inbox empty for '%s')\n", b.AgentID)
	}
	return nil
}

// DoAck implements `agent-bus ack <id> [note]`.
func (b *Bus) DoAck(id, note string) error {
	if id == "" {
		return usageErr("agent-bus: ack needs <id> (see 'agent-bus inbox')")
	}
	if _, err := os.Stat(b.mfile(id)); err != nil {
		return usageErr(fmt.Sprintf("agent-bus: no such directive '%s'", id))
	}
	lock, err := b.lockBus()
	if err != nil {
		return err
	}
	defer lock.Close()
	f, err := os.Create(b.ackmark(id, b.AgentID))
	if err != nil {
		return err
	}
	f.Close()
	b.logEvent("ack", fmt.Sprintf("%s %s", id, note))
	noteSuffix := ""
	if note != "" {
		noteSuffix = " — " + note
	}
	fmt.Printf("agent-bus: '%s' acked %s%s\n", b.AgentID, id, noteSuffix)
	return nil
}

// DoBoard implements `agent-bus board`.
func (b *Bus) DoBoard() error {
	recs := b.allStatusRecords()
	if len(recs) == 0 {
		fmt.Println("(no agents reporting — none have run 'agent-bus status' yet)")
		return nil
	}
	fmt.Printf("%-18s  %-12s  %-10s  %-6s  %-5s  %-22s  %s\n", "AGENT", "PROJECT", "STATE", "AGE", "INBOX", "ATTACH", "NOTE")
	for _, r := range recs {
		p := r.Project
		if p == "" {
			p = "-"
		}
		h := r.Handle
		if h == "" {
			h = "-"
		}
		mark := ""
		if b.stale(r.Epoch) {
			mark = " [STALE]"
		}
		fmt.Printf("%-18s  %-12s  %-10s  %-6s  %-5d  %-22s  %s%s\n",
			r.Agent, p, r.State, age(r.Epoch), b.inboxCount(r.Agent), h, r.Note, mark)
	}
	return nil
}

// DoPost implements `agent-bus tell <agent> <text…>` / `broadcast <text…>`.
func (b *Bus) DoPost(target string, words []string) error {
	text := clean(strings.Join(words, " "))
	if text == "" {
		return usageErr("agent-bus: directive needs text")
	}
	b.warnID()
	id, err := b.post(target, text)
	if err != nil {
		return err
	}
	if target == "all" {
		fmt.Printf("agent-bus: broadcast %s from '%s' → ALL: %s\n", id, b.AgentID, text)
	} else {
		fmt.Printf("agent-bus: directive %s from '%s' → %s: %s\n", id, b.AgentID, target, text)
	}
	return nil
}

const defaultController = "control"

func controllerOf() string {
	if v := os.Getenv("AGENT_CONTROLLER"); v != "" {
		return v
	}
	return defaultController
}

// DoAsk implements `agent-bus ask "<q>" [--to <ctrl>] [--timeout <secs>]`.
func (b *Bus) DoAsk(args []string) error {
	ctrl := controllerOf()
	timeout := int64(600)
	poll := 5 * time.Second
	var words []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--to":
			if i+1 >= len(args) {
				return usageErr("agent-bus: --to needs a value")
			}
			ctrl = args[i+1]
			i++
		case "--timeout":
			if i+1 >= len(args) {
				return usageErr("agent-bus: --timeout needs a value")
			}
			v, err := strconv.ParseInt(args[i+1], 10, 64)
			if err != nil {
				return usageErr("agent-bus: --timeout needs a number of seconds")
			}
			timeout = v
			i++
		default:
			words = append(words, args[i])
		}
	}
	q := clean(strings.Join(words, " "))
	if q == "" {
		return usageErr("agent-bus: ask needs a <question…>")
	}
	b.warnID()
	qid, err := b.post(ctrl, "[ask] "+q)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "agent-bus: asked '%s' (%s) — waiting up to %ds for a reply…\n", ctrl, qid, timeout)
	prefix := fmt.Sprintf("[re %s] ", qid)
	deadline := time.Now().Add(time.Duration(timeout) * time.Second)
	for {
		for _, m := range b.allMsgRecords() {
			if m.Target != b.AgentID {
				continue
			}
			if strings.HasPrefix(m.Text, prefix) {
				lock, err := b.lockBus()
				if err != nil {
					return err
				}
				f, ferr := os.Create(b.ackmark(m.ID, b.AgentID))
				if ferr == nil {
					f.Close()
				}
				lock.Close()
				fmt.Println(strings.TrimPrefix(m.Text, prefix))
				return nil
			}
		}
		if time.Now().After(deadline) {
			fmt.Fprintf(os.Stderr, "agent-bus: no reply to %s within %ds\n", qid, timeout)
			os.Exit(3)
		}
		time.Sleep(poll)
	}
}

// DoReply implements `agent-bus reply <qid> <answer…>`.
func (b *Bus) DoReply(qid string, words []string) error {
	ans := clean(strings.Join(words, " "))
	if qid == "" || ans == "" {
		return usageErr("agent-bus: reply needs <qid> <answer…>")
	}
	qm, ok := parseMsgFile(qid, b.mfile(qid))
	if !ok {
		return usageErr(fmt.Sprintf("agent-bus: no such question '%s'", qid))
	}
	b.warnID()
	rid, err := b.post(qm.From, fmt.Sprintf("[re %s] %s", qid, ans))
	if err != nil {
		return err
	}
	lock, err := b.lockBus()
	if err != nil {
		return err
	}
	f, ferr := os.Create(b.ackmark(qid, b.AgentID))
	if ferr == nil {
		f.Close()
	}
	lock.Close()
	fmt.Printf("agent-bus: replied to %s (asker '%s') via %s\n", qid, qm.From, rid)
	return nil
}

// DoAsks implements `agent-bus asks`.
func (b *Bus) DoAsks() error {
	found := false
	for _, m := range b.allMsgRecords() {
		if m.Target != b.AgentID {
			continue
		}
		if !strings.HasPrefix(m.Text, "[ask] ") {
			continue
		}
		if b.acked(m.ID, b.AgentID) {
			continue
		}
		if !found {
			fmt.Printf("%-6s  %-7s  %-18s  %s\n", "QID", "AGE", "FROM", "QUESTION")
			found = true
		}
		fmt.Printf("%-6s  %-7s  %-18s  %s\n", m.ID, age(m.Epoch), m.From, strings.TrimPrefix(m.Text, "[ask] "))
	}
	if !found {
		fmt.Printf("(no pending questions for '%s')\n", b.AgentID)
	}
	return nil
}

// DoMsgs implements `agent-bus msgs`.
func (b *Bus) DoMsgs() error {
	msgs := b.allMsgRecords()
	if len(msgs) == 0 {
		fmt.Println("(no directives)")
		return nil
	}
	fmt.Printf("%-6s  %-7s  %-16s  %-12s  %-6s  %s\n", "ID", "AGE", "FROM", "TARGET", "ACKS", "WHAT")
	for _, m := range msgs {
		entries, _ := os.ReadDir(b.AckDir)
		nacks := 0
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), m.ID+"__") {
				nacks++
			}
		}
		fmt.Printf("%-6s  %-7s  %-16s  %-12s  %-6d  %s\n", m.ID, age(m.Epoch), m.From, m.Target, nacks, m.Text)
	}
	return nil
}

// DoEvents implements `agent-bus events [N]`.
func (b *Bus) DoEvents(n int) error {
	data, err := os.ReadFile(b.Events)
	if err != nil {
		fmt.Println("(no events)")
		return nil
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		fmt.Println("(no events)")
		return nil
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	for _, l := range lines {
		fmt.Println(l)
	}
	return nil
}

// DoPrune implements `agent-bus prune`.
func (b *Bus) DoPrune() error {
	lock, err := b.lockBus()
	if err != nil {
		return err
	}
	defer lock.Close()

	statusCnt := 0
	live := make(map[string]bool)
	for _, r := range b.allStatusRecords() {
		if b.stale(r.Epoch) {
			os.Remove(filepath.Join(b.StatusDir, fsafe(r.Agent)+".tsv"))
			statusCnt++
			continue
		}
		live[r.Agent] = true
	}

	msgCnt := 0
	for _, m := range b.allMsgRecords() {
		if !b.stale(m.Epoch) {
			continue
		}
		keep := false
		for agent := range live {
			if m.Target != "all" && m.Target != agent {
				continue
			}
			if !b.acked(m.ID, agent) {
				keep = true
				break
			}
		}
		if !keep {
			os.Remove(b.mfile(m.ID))
			entries, _ := os.ReadDir(b.AckDir)
			for _, e := range entries {
				if strings.HasPrefix(e.Name(), m.ID+"__") {
					os.Remove(filepath.Join(b.AckDir, e.Name()))
				}
			}
			msgCnt++
		}
	}

	b.logEvent("prune", fmt.Sprintf("%d stale heartbeats, %d directives", statusCnt, msgCnt))
	fmt.Printf("agent-bus: pruned %d stale heartbeat(s), %d spent directive(s)\n", statusCnt, msgCnt)
	return nil
}

// usageErr is a sentinel error type whose message is already fully-formatted
// for stderr; Run() prints it as-is and exits 2, matching bash's usage exits.
type UsageError string

func (e UsageError) Error() string { return string(e) }

func usageErr(msg string) error { return UsageError(msg) }
