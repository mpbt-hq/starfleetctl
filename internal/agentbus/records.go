// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package agentbus

import (
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

// StatusPatch carries the fields a `status report` invocation wants to
// set. Progress < 0 means "not specified" so a caller can distinguish "leave
// unchanged" from "set to 0". Written to health/<ship>.json via DoHealthUpdate.
type StatusPatch struct {
	Task       string
	Progress   int
	Blocker    string
	ETA        string
	Branch     string
	Note       string
	LaunchType string
	Parent     string
	Provider   string
	Model      string
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
