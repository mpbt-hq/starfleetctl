// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package agentbus

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
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

// attachThreshold is the inline directive size (in bytes) above which DoPost
// automatically spills the body into an attachment and leaves only a short
// fetch pointer inline. This keeps large directives (e.g. a full hard-reset
// broadcast) from being silently truncated by an agent display that caps
// inline text length — the agent fetches the full payload via
// `agent-bus get <id>` instead.
const attachThreshold = 768

// attachPrefix marks the inline pointer that references an attachment file.
// Format: [[attach:<fsafe-basename>:<size-bytes>:<sha256hex>]]
// It survives clean() (no tabs/newlines) and the TSV text field intact.
const attachPrefix = "[[attach:"

// post writes a new directive/question record and returns its id. If payload
// is non-empty it is stored as an attachment under AttachDir and the inline
// text gains a fetch pointer, so the full body is retrievable via DoGet.
// Caller must not hold the bus lock — post takes it itself, mirroring _post().
func (b *Bus) post(target, summary, payload, basename string) (string, error) {
	lock, err := b.lockBus()
	if err != nil {
		return "", err
	}
	defer lock.Close()
	id, err := b.nextID()
	if err != nil {
		return "", err
	}

	text := clean(summary)
	if payload != "" {
		sha := sha256.Sum256([]byte(payload))
		shx := hex.EncodeToString(sha[:])
		aname := id + "__" + fsafe(basename)
		if err := os.WriteFile(filepath.Join(b.AttachDir, aname), []byte(payload), 0o644); err != nil {
			return "", err
		}
		if text == "" {
			text = fmt.Sprintf("payload attached (%d bytes)", len(payload))
		}
		text += fmt.Sprintf(" — fetch: agent-bus get %s %s%s:%d:%s]]",
			id, attachPrefix, fsafe(basename), len(payload), shx)
	}

	line := fmt.Sprintf("%d\t%s\t%s\t%s\t%s\n", now(), isots(), b.ShipID, target, text)
	mpath, err := b.mfile(id)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(mpath, []byte(line), 0o644); err != nil {
		return "", err
	}
	b.logEvent("directive", fmt.Sprintf("%s → %s: %s", id, target, text))
	return id, nil
}

// DoGet prints (or writes to outPath) the attachment payload of message id.
// If the message has no attachment, it returns an error.
func (b *Bus) DoGet(id, outPath string) error {
	if id == "" {
		return usageErr("agent-bus: get needs <id> [--out <path>]")
	}
	matches, err := filepath.Glob(filepath.Join(b.AttachDir, fsafe(id)+"__*"))
	if err != nil {
		return err
	}
	if len(matches) == 0 {
		return fmt.Errorf("agent-bus: no attachment for %s", id)
	}
	data, err := os.ReadFile(matches[0])
	if err != nil {
		return err
	}
	if outPath != "" {
		return os.WriteFile(outPath, data, 0o644)
	}
	_, err = os.Stdout.Write(data)
	return err
}

// hasAttachment reports whether a message's inline text references an
// attachment (see attachPrefix).
func hasAttachment(text string) bool {
	return strings.Contains(text, attachPrefix)
}

// dispText returns the human-readable directive body, appending a "[ATT]"
// marker when the full payload lives in an attachment the agent should fetch.
func dispText(m msgRecord) string {
	t := m.Text
	if hasAttachment(t) {
		t += " [ATT]"
	}
	return t
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
		now(), isots(), b.ShipID, pp, clean(state), os.Getpid(), hh, clean(note))
	if err := os.WriteFile(b.sfile(b.ShipID), []byte(line), 0o644); err != nil {
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
	fmt.Printf("agent-bus: '%s'%s → %s%s\n", b.ShipID, suffix, state, noteSuffix)
	return nil
}

// DoTouch implements `agent-bus touch`: refresh MY OWN heartbeat's
// timestamp without changing state/note — for a periodic auto-refresh (see
// agent-bus-monitor-loop) so a ship deep in a long task that never calls
// DoStatus itself doesn't fall out of BusTTL and read as dead/pruned on the
// board while the session is very much alive.
//
// Race-safety: this does NOT cache a state+note value anywhere — it
// re-reads whatever is CURRENTLY on disk for my own status file, under the
// same lock a real DoStatus write uses, and rewrites only the timestamp
// fields. If a real DoStatus call happened since the last touch, this picks
// up that new value (the file IS the single source of truth) and refreshes
// its timestamp too; there's no stale copy anywhere that could overwrite a
// fresh real post.
//
// Deliberately does NOT call logEvent: a pure timestamp bump every few
// minutes forever isn't a state transition worth an audit-trail entry, and
// would clutter events.log with noise indistinguishable from real status
// changes. Silent no-op if there's no existing heartbeat to refresh.
func (b *Bus) DoTouch() error {
	lock, err := b.lockBus()
	if err != nil {
		return err
	}
	defer lock.Close()

	rec, ok := parseStatusFile(b.sfile(b.ShipID))
	if !ok {
		return nil
	}
	line := fmt.Sprintf("%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
		now(), isots(), rec.Agent, rec.Project, rec.State, rec.PID, rec.Handle, rec.Note)
	return os.WriteFile(b.sfile(b.ShipID), []byte(line), 0o644)
}

// DoClear implements `agent-bus clear`.
func (b *Bus) DoClear() error {
	lock, err := b.lockBus()
	if err != nil {
		return err
	}
	defer lock.Close()
	_ = os.Remove(b.sfile(b.ShipID))
	b.logEvent("clear", "")
	fmt.Printf("agent-bus: cleared heartbeat for '%s'\n", b.ShipID)
	return nil
}

// DoInbox implements `agent-bus inbox`.
func (b *Bus) DoInbox() error {
	found := false
	for _, m := range b.allMsgRecords() {
		if m.Target != "all" && m.Target != b.ShipID {
			continue
		}
		if !found {
			fmt.Printf("%-6s  %-7s  %-18s  %-4s  %s\n", "ID", "AGE", "FROM", "ACK", "WHAT")
			found = true
		}
		a := " "
		if b.acked(m.ID, b.ShipID) {
			a = "✓"
		}
		fmt.Printf("%-6s  %-7s  %-18s  %-4s  %s\n", m.ID, age(m.Epoch), m.From, a, dispText(m))
	}
	if !found {
		fmt.Printf("(inbox empty for '%s')\n", b.ShipID)
	}
	return nil
}

// DoAck implements `agent-bus ack <id> [note]`.
func (b *Bus) DoAck(id, note string) error {
	if id == "" {
		return usageErr("agent-bus: ack needs <id> (see 'agent-bus inbox')")
	}
	mpath, err := b.mfile(id)
	if err != nil {
		return usageErr(fmt.Sprintf("agent-bus: %v", err))
	}
	if _, err := os.Stat(mpath); err != nil {
		return usageErr(fmt.Sprintf("agent-bus: no such directive '%s'", id))
	}
	lock, err := b.lockBus()
	if err != nil {
		return err
	}
	defer lock.Close()
	apath, err := b.ackmark(id, b.ShipID)
	if err != nil {
		return err
	}
	f, err := os.Create(apath)
	if err != nil {
		return err
	}
	f.Close()
	b.logEvent("ack", fmt.Sprintf("%s %s", id, note))
	noteSuffix := ""
	if note != "" {
		noteSuffix = " — " + note
	}
	fmt.Printf("agent-bus: '%s' acked %s%s\n", b.ShipID, id, noteSuffix)
	return nil
}

// DoBoard implements `agent-bus board`.
func (b *Bus) DoBoard() error {
	recs := b.AllStatusRecords()
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
// When useStdin is true, words are ignored and the message body is read from
// os.Stdin instead — this bypasses the OS ARG_MAX limit that constrains
// command-line delivery of large directives (see the size-limit test,
// 2026-07-09: tell works up to ~100KB via argv, fails at ~1MB with E2BIG;
// the storage layer itself has no limit and handles 20MB+ fine).
// Tell queues a directive for target with the given inline text — the
// programmatic equivalent of `agent-bus tell <target> <text…>`, for callers
// (e.g. task capture's commission-a-ship step) that shouldn't shell out. It
// takes the bus lock itself and auto-spills oversized bodies into an
// attachment, exactly like DoPost.
func (b *Bus) Tell(target, text string) (string, error) {
	return b.post(target, text, "", "tell")
}

func (b *Bus) DoPost(target string, words []string, useStdin bool, attachPath string) error {
	var summary, payload, basename string

	if attachPath != "" {
		data, err := os.ReadFile(attachPath)
		if err != nil {
			return fmt.Errorf("agent-bus: reading attachment: %w", err)
		}
		payload = string(data)
		basename = filepath.Base(attachPath)
		if useStdin {
			s, err := io.ReadAll(os.Stdin)
			if err != nil {
				return fmt.Errorf("agent-bus: reading stdin: %w", err)
			}
			summary = clean(string(s))
		} else {
			summary = clean(strings.Join(words, " "))
		}
	} else {
		var text string
		if useStdin {
			data, err := io.ReadAll(os.Stdin)
			if err != nil {
				return fmt.Errorf("agent-bus: reading stdin: %w", err)
			}
			text = clean(string(data))
		} else {
			text = clean(strings.Join(words, " "))
		}
		if len([]byte(text)) > attachThreshold && strings.TrimSpace(text) != "" {
			// auto-spill: keep only a short fetch pointer inline, move the
			// body into an attachment so an agent display can't truncate it.
			payload = text
			basename = "payload.txt"
			summary = ""
		} else {
			summary = text
		}
	}

	if summary == "" && payload == "" {
		return usageErr("agent-bus: directive needs text (via args or stdin)")
	}
	b.warnID()
	id, err := b.post(target, summary, payload, basename)
	if err != nil {
		return err
	}
	// id goes to stdout (captureable, as before); the human description
	// goes to stderr so it doesn't pollute the captured id.
	if target == "all" {
		fmt.Fprintf(os.Stderr, "agent-bus: broadcast %s from '%s' → ALL\n", id, b.ShipID)
	} else {
		fmt.Fprintf(os.Stderr, "agent-bus: directive %s from '%s' → %s\n", id, b.ShipID, target)
	}
	fmt.Printf("%s\n", id)
	return nil
}

const defaultController = "control"

// Controller returns the agent ID of the control agent from $AGENT_CONTROLLER
// (default "control").
func Controller() string {
	if v := os.Getenv("AGENT_CONTROLLER"); v != "" {
		return v
	}
	return defaultController
}

// DoAsk implements `agent-bus ask "<q>" [--to <ctrl>] [--timeout <secs>]`.
func (b *Bus) DoAsk(args []string) error {
	ctrl := Controller()
	timeout := int64(600)
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
	ans, err := b.AskAndWait(q, ctrl, timeout)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(3)
	}
	fmt.Println(ans)
	return nil
}

// DoReply implements `agent-bus reply <qid> <answer…>`.
func (b *Bus) DoReply(qid string, words []string) error {
	ans := clean(strings.Join(words, " "))
	if qid == "" || ans == "" {
		return usageErr("agent-bus: reply needs <qid> <answer…>")
	}
	qpath, err := b.mfile(qid)
	if err != nil {
		return usageErr(fmt.Sprintf("agent-bus: %v", err))
	}
	qm, ok := parseMsgFile(qid, qpath)
	if !ok {
		return usageErr(fmt.Sprintf("agent-bus: no such question '%s'", qid))
	}
	b.warnID()
	rid, err := b.post(qm.From, fmt.Sprintf("[re %s] %s", qid, ans), "", "")
	if err != nil {
		return err
	}
	lock, err := b.lockBus()
	if err != nil {
		return err
	}
	apath, aerr := b.ackmark(qid, b.ShipID)
	if aerr == nil {
		f, ferr := os.Create(apath)
		if ferr == nil {
			f.Close()
		}
	}
	lock.Close()
	fmt.Printf("agent-bus: replied to %s (asker '%s') via %s\n", qid, qm.From, rid)
	return nil
}

// DoAsks implements `agent-bus asks`.
func (b *Bus) DoAsks() error {
	found := false
	for _, m := range b.allMsgRecords() {
		if m.Target != b.ShipID {
			continue
		}
		if !strings.HasPrefix(m.Text, "[ask] ") {
			continue
		}
		if b.acked(m.ID, b.ShipID) {
			continue
		}
		if !found {
			fmt.Printf("%-6s  %-7s  %-18s  %s\n", "QID", "AGE", "FROM", "QUESTION")
			found = true
		}
		fmt.Printf("%-6s  %-7s  %-18s  %s\n", m.ID, age(m.Epoch), m.From, strings.TrimPrefix(m.Text, "[ask] "))
	}
	if !found {
		fmt.Printf("(no pending questions for '%s')\n", b.ShipID)
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
		fmt.Printf("%-6s  %-7s  %-16s  %-12s  %-6d  %s\n", m.ID, age(m.Epoch), m.From, m.Target, nacks, dispText(m))
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
	for _, r := range b.AllStatusRecords() {
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
			if mpath, err := b.mfile(m.ID); err == nil {
				os.Remove(mpath)
			}
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

// AskAndWait posts a question (tagged "[ask]") to the specified controller,
// polls for the reply (tagged "[re <qid>]") addressed to us, acks it, and
// returns the raw answer text.  This is the same logic as DoAsk but returns
// (string, error) instead of calling os.Exit(3) on timeout — suitable for
// use from hook subcommands that must not blow away the whole process.
func (b *Bus) AskAndWait(question, ctrl string, timeoutSec int64) (string, error) {
	b.warnID()
	qid, err := b.post(ctrl, "[ask] "+question, "", "")
	if err != nil {
		return "", fmt.Errorf("post: %w", err)
	}
	fmt.Fprintf(os.Stderr, "agent-bus: asked '%s' (%s) — waiting up to %ds for a reply…\n", ctrl, qid, timeoutSec)
	prefix := "[re " + qid + "] "
	deadline := time.Now().Add(time.Duration(timeoutSec) * time.Second)
	for {
		for _, m := range b.allMsgRecords() {
			if m.Target != b.ShipID {
				continue
			}
			if strings.HasPrefix(m.Text, prefix) {
			lock, err := b.lockBus()
			if err != nil {
				return "", err
			}
			apath, aerr := b.ackmark(m.ID, b.ShipID)
			if aerr == nil {
				f, ferr := os.Create(apath)
				if ferr == nil {
					f.Close()
				}
			}
			lock.Close()
			return strings.TrimPrefix(m.Text, prefix), nil
			}
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("no reply to %s within %ds", qid, timeoutSec)
		}
		time.Sleep(5 * time.Second)
	}
}

// usageErr is a sentinel error type whose message is already fully-formatted
// for stderr; Run() prints it as-is and exits 2, matching bash's usage exits.
type UsageError string

func (e UsageError) Error() string { return string(e) }

func usageErr(msg string) error { return UsageError(msg) }
