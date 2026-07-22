// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package agentbus

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
// msgType specifies the message type: "ship", "user", "control" (defaults to "ship").
func (b *Bus) post(target, summary, payload, basename, replyTo, msgType string) (string, error) {
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
	var attachName string
	if payload != "" {
		sha := sha256.Sum256([]byte(payload))
		shx := hex.EncodeToString(sha[:])
		aname := id + "__" + fsafe(basename)
		if err := os.WriteFile(filepath.Join(b.AttachDir, aname), []byte(payload), 0o644); err != nil {
			return "", err
		}
		attachName = aname
		if text == "" {
			text = fmt.Sprintf("payload attached (%d bytes)", len(payload))
		}
		text += fmt.Sprintf(" — fetch: agent-bus get %s %s%s:%d:%s]]",
			id, attachPrefix, fsafe(basename), len(payload), shx)
	}

	if msgType == "" {
		msgType = "ship"
	}

	msg := msgRecord{
		ID:      id,
		Epoch:   now(),
		ISO:     isots(),
		From:    b.ShipID,
		Target:  target,
		Text:    text,
		ReplyTo: replyTo,
		Type:    msgType,
		Attach:  attachName,
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return "", err
	}
	mpath, err := b.mfile(id, target)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(mpath, data, 0o644); err != nil {
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

// DoStatus implements `agent-bus status <state> [note]` — writes the unified
// status/<ship>.json (single source of truth for heartbeat + health).
func (b *Bus) DoStatus(state, note string, patch StatusPatch) error {
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

	// Read existing record to preserve plugin-liveness fields.
	prev, _ := parseStatusFile(b.sfile(b.ShipID))

	rec := StatusRecord{
		Epoch:           now(),
		ISO:             isots(),
		Agent:           b.ShipID,
		Project:         pp,
		State:           clean(state),
		PID:             os.Getpid(),
		Handle:          hh,
		Note:            clean(note),
		PluginLastRun:   prev.PluginLastRun,
		ModelLastAction: prev.ModelLastAction,
		Model:           prev.Model,
		Server:          prev.Server,
		ErrorTag:        prev.ErrorTag,
		Task:            prev.Task,
		Progress:        prev.Progress,
		Blocker:         prev.Blocker,
		ETA:             prev.ETA,
		Branch:          prev.Branch,
		LaunchType:      prev.LaunchType,
		Parent:          prev.Parent,
		Provider:        prev.Provider,
		Updated:         prev.Updated,
	}

	// Merge structured detail fields from patch.
	if patch.Task != "" {
		rec.Task = patch.Task
	}
	if patch.Progress >= 0 {
		rec.Progress = patch.Progress
	}
	if patch.Blocker != "" {
		rec.Blocker = patch.Blocker
	}
	if patch.ETA != "" {
		rec.ETA = patch.ETA
	}
	if patch.Branch != "" {
		rec.Branch = patch.Branch
	}
	if patch.Note != "" {
		rec.Note = clean(patch.Note)
	}
	if patch.LaunchType != "" {
		rec.LaunchType = patch.LaunchType
	}
	if patch.Parent != "" {
		rec.Parent = patch.Parent
	}
	if patch.Provider != "" {
		rec.Provider = patch.Provider
	}
	if patch.Model != "" {
		rec.Model = patch.Model
	}

	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(b.sfile(b.ShipID), append(data, '\n'), 0o644); err != nil {
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
	rec.Epoch = now()
	rec.ISO = isots()
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(b.sfile(b.ShipID), append(data, '\n'), 0o644)
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

// DoExit implements `agent-bus exit` — clean ship shutdown. Updates the
// health record to "offline" (intentional, not a crash), deletes the
// heartbeat TSV, and logs the event. Single atomic operation for the
// plugin's process.on('exit') handler.
func (b *Bus) DoExit(reason string) error {
	lock, err := b.lockBus()
	if err != nil {
		return err
	}
	defer lock.Close()
	// Update health to offline — the watchdog treats this as intentional.
	_ = b.DoHealthUpdate([]string{"--state", "offline"})
	// Remove heartbeat TSV so the board no longer shows this ship.
	_ = os.Remove(b.sfile(b.ShipID))
	if reason == "" {
		reason = "exit"
	}
	b.logEvent("exit", reason)
	return nil
}

// DoInit consolidates the plugin's startup sequence into a single bus call:
// ack all unacked inbox, load seen set, prune stale data, set status to idle.
// Returns the acked messages and seen set so the plugin can populate its state.
func (b *Bus) DoInit(note string) ([]inboxMsg, []string, error) {
	lock, err := b.lockBus()
	if err != nil {
		return nil, nil, err
	}
	defer lock.Close()

	// 1. Ack all unacked inbox for this ship.
	var acked []inboxMsg
	for _, m := range b.allMsgRecords() {
		if m.Target != "all" && m.Target != b.ShipID {
			continue
		}
		if b.acked(m.ID, b.ShipID) {
			continue
		}
		// Mark as seen by moving to seen/ (no ack file needed)
		seenPath := filepath.Join(b.MsgDir, fsafe(b.ShipID), "seen", fsafe(m.ID)+".json")
		if err := os.MkdirAll(filepath.Dir(seenPath), 0o755); err == nil {
			if _, err := os.Stat(filepath.Join(b.MsgDir, fsafe(b.ShipID), "unseen", fsafe(m.ID)+".json")); err == nil {
				os.Rename(filepath.Join(b.MsgDir, fsafe(b.ShipID), "unseen", fsafe(m.ID)+".json"), seenPath)
			} else if _, err := os.Stat(filepath.Join(b.MsgDir, fsafe(m.ID)+".json")); err == nil {
				os.Rename(filepath.Join(b.MsgDir, fsafe(m.ID)+".json"), filepath.Join(b.MsgDir, fsafe(b.ShipID), "seen", fsafe(m.ID)+".json"))
			}
		}
		acked = append(acked, inboxMsg{ID: m.ID, From: m.From, Text: m.Text})
		b.logEvent("ack", m.ID+" init-seen")
	}

	// 2. Load seen set.
	seenMap, _ := b.loadSeen(b.ShipID)
	seen := make([]string, 0, len(seenMap))
	for id := range seenMap {
		seen = append(seen, id)
	}

	// 3. Prune stale heartbeats + old directives.
	statusCnt := 0
	live := make(map[string]bool)
	for _, r := range b.AllStatusRecords() {
		if b.stale(r.Epoch, r.State) {
			os.Remove(filepath.Join(b.StatusDir, fsafe(r.Agent)+".json"))
			statusCnt++
			continue
		}
		live[r.Agent] = true
	}
	msgCnt := 0
	for _, m := range b.allMsgRecords() {
		if !b.stale(m.Epoch, "") {
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
			if mpath, err := b.mfile(m.ID, m.Target); err == nil {
				os.Remove(mpath)
			}
			msgCnt++
		}
	}
	if statusCnt+msgCnt > 0 {
		b.logEvent("prune", fmt.Sprintf("%d stale heartbeats, %d directives", statusCnt, msgCnt))
	}

	// 4. Set status to idle.
	if note == "" {
		note = "opencode ship"
	}
	pp := clean(b.Project)
	if pp == "" {
		pp = "-"
	}
	hh := clean(b.Handle)
	if hh == "" {
		hh = "-"
	}
	rec := StatusRecord{
		Epoch:   now(),
		ISO:     isots(),
		Agent:   b.ShipID,
		Project: pp,
		State:   "idle",
		PID:     os.Getpid(),
		Handle:  hh,
		Note:    clean(note),
	}
	data, _ := json.MarshalIndent(rec, "", "  ")
	_ = os.WriteFile(b.sfile(b.ShipID), append(data, '\n'), 0o644)

	return acked, seen, nil
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

	// First, find the message to get its target
	found := false
	for _, m := range b.allMsgRecords() {
		if m.ID == id {
			found = true
			break
		}
	}
	if !found {
		return usageErr(fmt.Sprintf("agent-bus: no such directive '%s'", id))
	}

	lock, err := b.lockBus()
	if err != nil {
		return err
	}
	defer lock.Close()

	// Move from unseen to seen for this ship
	seenPath, err := b.mfileSeen(b.ShipID, id)
	if err != nil {
		return err
	}
	// Also try old location for migration compat
	oldPath := filepath.Join(b.MsgDir, fsafe(id)+".json")
	newPath := filepath.Join(b.MsgDir, fsafe(b.ShipID), "unseen", fsafe(id)+".json")

	// Move message from unseen to seen
	var srcPath string
	if _, err := os.Stat(newPath); err == nil {
		srcPath = newPath
	} else if _, err := os.Stat(oldPath); err == nil {
		srcPath = oldPath
	} else {
		return usageErr(fmt.Sprintf("agent-bus: no such directive '%s'", id))
	}

	// Ensure seen dir exists
	seenPath, err = b.mfileSeen(b.ShipID, id)
	if err != nil {
		return err
	}
	if err := os.Rename(srcPath, seenPath); err != nil {
		return err
	}

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
		if b.stale(r.Epoch, r.State) {
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
func (b *Bus) Tell(target, text, replyTo string) (string, error) {
	return b.post(target, text, "", "tell", replyTo, "ship")
}

func (b *Bus) DoPost(target string, words []string, useStdin bool, attachPath, replyTo, msgType string) error {
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
	if msgType == "" {
		msgType = "ship"
	}
	id, err := b.post(target, summary, payload, basename, replyTo, msgType)
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

// Command posts a command directive (type="command") to the target.
// Commands are structurally different from regular directives: they carry
// a verb (setModel, /quit, /reset, etc.) and are handled by the plugin's
// command dispatch, not injected as system prompts.
func (b *Bus) Command(target, verb, args string) (string, error) {
	text := verb
	if args != "" {
		text = verb + " " + args
	}
	return b.post(target, text, "", "cmd", "", "command")
}

// DoCommand implements `agent-bus cmd <target> <verb> [args...]`.
func (b *Bus) DoCommand(args []string) error {
	if len(args) < 2 {
		return usageErr("agent-bus: cmd needs <target> <verb> [args...]")
	}
	target := args[0]
	verb := args[1]
	extra := ""
	if len(args) > 2 {
		extra = strings.Join(args[2:], " ")
	}
	b.warnID()
	id, err := b.Command(target, verb, extra)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "agent-bus: command %s from '%s' → %s (%s)\n", id, b.ShipID, target, verb)
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
	qpath, err := b.mfile(qid, "all")
	if err != nil {
		return usageErr(fmt.Sprintf("agent-bus: %v", err))
	}
	qm, ok := parseMsgFile(qid, qpath)
	if !ok {
		return usageErr(fmt.Sprintf("agent-bus: no such question '%s'", qid))
	}
	b.warnID()
	rid, err := b.post(qm.From, fmt.Sprintf("[re %s] %s", qid, ans), "", "", qid, "ship")
	if err != nil {
		return err
	}
	lock, err := b.lockBus()
	if err != nil {
		return err
	}
	// Mark as seen by moving to seen/ (no ackmark needed)
	seenPath, err := b.mfileSeen(b.ShipID, qid)
	if err == nil {
		_ = os.Rename(filepath.Join(b.MsgDir, fsafe(b.ShipID), "unseen", fsafe(qid)+".json"), seenPath)
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
		nacks := b.ackedCount(m.ID)
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
		if b.stale(r.Epoch, r.State) {
			os.Remove(filepath.Join(b.StatusDir, fsafe(r.Agent)+".json"))
			statusCnt++
			continue
		}
		live[r.Agent] = true
	}

	msgCnt := 0
	for _, m := range b.allMsgRecords() {
		if !b.stale(m.Epoch, "") {
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
			if mpath, err := b.mfile(m.ID, m.Target); err == nil {
				os.Remove(mpath)
			}
			msgCnt++
		}
	}

	b.logEvent("prune", fmt.Sprintf("%d stale heartbeats, %d directives", statusCnt, msgCnt))
	fmt.Printf("agent-bus: pruned %d stale heartbeat(s), %d spent directive(s)\n", statusCnt, msgCnt)
	return nil
}

// DoMigrate converts all legacy .tsv message files to .json format.
// This is a one-time migration that can be run safely multiple times.
func (b *Bus) DoMigrate() error {
	lock, err := b.lockBus()
	if err != nil {
		return err
	}
	defer lock.Close()

	migrated := 0
	for _, id := range globSortedFiles(b.MsgDir, "m", ".tsv") {
		tsvPath := filepath.Join(b.MsgDir, id+".tsv")
		jsonPath := filepath.Join(b.MsgDir, id+".json")

		// Skip if JSON already exists
		if _, err := os.Stat(jsonPath); err == nil {
			continue
		}

		// Parse TSV
		msg, ok := parseMsgFileTSV(id, tsvPath)
		if !ok {
			fmt.Printf("agent-bus: warning: failed to parse %s, skipping\n", tsvPath)
			continue
		}

		// Ensure type is set
		if msg.Type == "" {
			msg.Type = "ship"
		}

		// Write JSON
		data, err := json.Marshal(msg)
		if err != nil {
			fmt.Printf("agent-bus: warning: failed to marshal %s: %v\n", id, err)
			continue
		}

		if err := os.WriteFile(jsonPath, data, 0o644); err != nil {
			fmt.Printf("agent-bus: warning: failed to write %s: %v\n", id, err)
			continue
		}

		migrated++
	}

	if migrated > 0 {
		b.logEvent("migrate", fmt.Sprintf("migrated %d TSV message(s) to JSON", migrated))
		fmt.Printf("agent-bus: migrated %d TSV message(s) to JSON format\n", migrated)
	} else {
		fmt.Println("agent-bus: no TSV messages to migrate")
	}
	return nil
}

// AskAndWait posts a question (tagged "[ask]") to the specified controller,
// polls for the reply (tagged "[re <qid>]") addressed to us, acks it, and
// returns the raw answer text.  This is the same logic as DoAsk but returns
// (string, error) instead of calling os.Exit(3) on timeout — suitable for
// use from hook subcommands that must not blow away the whole process.
func (b *Bus) AskAndWait(question, ctrl string, timeoutSec int64) (string, error) {
	b.warnID()
	qid, err := b.post(ctrl, "[ask] "+question, "", "", "", "ship")
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
			// Mark reply as seen (move to seen/)
			seenPath := filepath.Join(b.MsgDir, fsafe(b.ShipID), "seen", fsafe(m.ID)+".json")
			if err := os.MkdirAll(filepath.Dir(seenPath), 0o755); err == nil {
				if _, err := os.Stat(filepath.Join(b.MsgDir, fsafe(b.ShipID), "unseen", fsafe(m.ID)+".json")); err == nil {
					os.Rename(filepath.Join(b.MsgDir, fsafe(b.ShipID), "unseen", fsafe(m.ID)+".json"), seenPath)
				} else if _, err := os.Stat(filepath.Join(b.MsgDir, fsafe(m.ID)+".json")); err == nil {
					os.Rename(filepath.Join(b.MsgDir, fsafe(m.ID)+".json"), filepath.Join(b.MsgDir, fsafe(b.ShipID), "seen", fsafe(m.ID)+".json"))
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
