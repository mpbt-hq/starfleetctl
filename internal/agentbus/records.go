// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package agentbus

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// StatusRecord mirrors one status/<agent>.tsv line:
// epoch \t isots \t agent \t project \t state \t pid \t handle \t note
type StatusRecord struct {
	Epoch   int64
	ISO     string
	Agent   string
	Project string
	State   string
	PID     string
	Handle  string
	Note    string
}

// StatusDetail is the richer, structured per-ship status written alongside the
// legacy TSV heartbeat (status/<agent>.json). Agents may report what they are
// currently working on, how far along, what blocks them, and an ETA — so a
// fleet console can show more than a one-line note. All fields are optional
// except State; a plain `agent-bus status <state>` still writes only the TSV
// and leaves the JSON untouched (or seeds it from the TSV on first report).
type StatusDetail struct {
	State    string `json:"state"`
	Task     string `json:"task,omitempty"`
	Progress int    `json:"progress,omitempty"` // 0-100, omitted when unknown
	Blocker  string `json:"blocker,omitempty"`
	ETA      string `json:"eta,omitempty"`   // free-form, e.g. "2026-07-17" or "~30m"
	Branch   string `json:"branch,omitempty"` // PR/branch the ship is on
	Note     string `json:"note,omitempty"`
	Updated  string `json:"updated"` // ISO timestamp, set on every write
}

// statusPatch carries only the fields a `status report` invocation wants to
// set. Progress < 0 means "not specified" so a caller can distinguish "leave
// unchanged" from "set to 0".
type StatusPatch struct {
	Task     string
	Progress int
	Blocker  string
	ETA      string
	Branch   string
	Note     string
}

// msgRecord mirrors one msgs/<id>.tsv line:
// epoch \t isots \t from \t target \t text
type msgRecord struct {
	ID      string
	Epoch   int64
	ISO     string
	From    string
	Target  string
	Text    string
	ReplyTo string // id of the message this one replies to (In-Reply-To), empty if none
}

func readFirstLine(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	line := string(data)
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	return line, nil
}

func parseStatusFile(path string) (StatusRecord, bool) {
	line, err := readFirstLine(path)
	if err != nil {
		return StatusRecord{}, false
	}
	f := strings.SplitN(line, "\t", 8)
	for len(f) < 8 {
		f = append(f, "")
	}
	epoch, _ := strconv.ParseInt(f[0], 10, 64)
	return StatusRecord{
		Epoch: epoch, ISO: f[1], Agent: f[2], Project: f[3],
		State: f[4], PID: f[5], Handle: f[6], Note: f[7],
	}, true
}

func parseMsgFile(id, path string) (msgRecord, bool) {
	line, err := readFirstLine(path)
	if err != nil {
		return msgRecord{}, false
	}
	f := strings.SplitN(line, "\t", 6)
	for len(f) < 6 {
		f = append(f, "")
	}
	epoch, _ := strconv.ParseInt(f[0], 10, 64)
	return msgRecord{ID: id, Epoch: epoch, ISO: f[1], From: f[2], Target: f[3], Text: f[4], ReplyTo: f[5]}, true
}

// globSortedTSV lists <dir>/<prefix>*.tsv basenames (without extension),
// sorted like bash glob expansion (plain lexicographic), matching the order
// scripts/agent-bus iterates status/msgs directories in.
func globSortedTSV(dir, prefix string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		n := e.Name()
		if !strings.HasSuffix(n, ".tsv") {
			continue
		}
		if prefix != "" && !strings.HasPrefix(n, prefix) {
			continue
		}
		names = append(names, strings.TrimSuffix(n, ".tsv"))
	}
	sort.Strings(names)
	return names
}

func (b *Bus) AllStatusRecords() []StatusRecord {
	var out []StatusRecord
	for _, agent := range globSortedTSV(b.StatusDir, "") {
		if r, ok := parseStatusFile(filepath.Join(b.StatusDir, agent+".tsv")); ok {
			out = append(out, r)
		}
	}
	return out
}

// ReadStatusDetail reads status/<agent>.json if present, else returns the
// zero value (callers treat a zero Task/Note as "no detail reported").
func (b *Bus) ReadStatusDetail(agent string) StatusDetail {
	var d StatusDetail
	data, err := os.ReadFile(b.dfile(agent))
	if err != nil {
		return d
	}
	_ = json.Unmarshal(data, &d)
	return d
}

// WriteStatusDetail merges the given partial patch into the on-disk JSON for
// agent, preserving any fields not explicitly overridden (so a later
// `status report --task X` doesn't wipe a previously-set blocker). State is
// always taken from the latest TSV heartbeat when the patch doesn't carry one.
// Progress is only overwritten when patch.Progress >= 0.
func (b *Bus) WriteStatusDetail(agent, state string, patch StatusPatch) error {
	cur := b.ReadStatusDetail(agent)
	if patch.Task != "" {
		cur.Task = patch.Task
	}
	if patch.Progress >= 0 {
		cur.Progress = patch.Progress
	}
	if patch.Blocker != "" {
		cur.Blocker = patch.Blocker
	}
	if patch.ETA != "" {
		cur.ETA = patch.ETA
	}
	if patch.Branch != "" {
		cur.Branch = patch.Branch
	}
	if patch.Note != "" {
		cur.Note = patch.Note
	}
	if cur.State == "" {
		cur.State = state
	}
	cur.Updated = isots()
	data, err := json.MarshalIndent(cur, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(b.dfile(agent), data, 0o644)
}

func (b *Bus) allMsgRecords() []msgRecord {
	var out []msgRecord
	for _, id := range globSortedTSV(b.MsgDir, "m") {
		if r, ok := parseMsgFile(id, filepath.Join(b.MsgDir, id+".tsv")); ok {
			out = append(out, r)
		}
	}
	// Reverse so newest messages appear first (lexicographic sort is oldest-first).
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// inboxCount counts unacked directives targeted at agent (explicit or "all").
func (b *Bus) inboxCount(agent string) int {
	cnt := 0
	for _, m := range b.allMsgRecords() {
		if m.Target != "all" && m.Target != agent {
			continue
		}
		if b.acked(m.ID, agent) {
			continue
		}
		cnt++
	}
	return cnt
}
