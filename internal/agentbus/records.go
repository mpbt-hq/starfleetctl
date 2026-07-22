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

// StatusRecord is the unified per-ship status file (status/<agent>.json).
// It carries both the legacy heartbeat fields (Epoch, ISO, Agent, Project,
// State, PID, Handle, Note) and the plugin-liveness / task-status fields
// that were previously in a separate health/<ship>.json.
type StatusRecord struct {
	// Legacy heartbeat fields (always present when written by Go code).
	Epoch   int64  `json:"epoch"`
	ISO     string `json:"iso"`
	Agent   string `json:"agent"`
	Project string `json:"project"`
	State   string `json:"state"`
	PID     int    `json:"pid"`
	Handle  string `json:"handle"`
	Note    string `json:"note"`

	// Plugin-liveness fields (written by the opencode plugin).
	PluginLastRun   string `json:"plugin_last_run,omitempty"`
	ModelLastAction string `json:"model_last_action,omitempty"`
	Model           string `json:"model,omitempty"`
	Server          string `json:"server,omitempty"`
	ErrorTag        string `json:"error_tag,omitempty"`

	// Task-status fields (written by Go CLI / status report).
	Task       string `json:"task,omitempty"`
	Progress   int    `json:"progress,omitempty"`
	Blocker    string `json:"blocker,omitempty"`
	ETA        string `json:"eta,omitempty"`
	Branch     string `json:"branch,omitempty"`
	LaunchType string `json:"launch_type,omitempty"`
	Parent     string `json:"parent,omitempty"`
	Provider   string `json:"provider,omitempty"`
	Updated    string `json:"updated,omitempty"`
}

// StatusPatch carries the fields a `status report` invocation wants to
// set. Progress < 0 means "not specified" so a caller can distinguish "leave
// unchanged" from "set to 0".
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

// msgRecord mirrors one msgs/<id>.json line (new format) or legacy .tsv line.
// Message type: "ship" (fleet ship), "user" (web frontend/user), "control" (automation/CLI).
type msgRecord struct {
	ID      string `json:"id"`
	Epoch   int64  `json:"epoch"`
	ISO     string `json:"iso"`
	From    string `json:"from"`
	Target  string `json:"target"`
	Text    string `json:"text"`
	ReplyTo string `json:"reply_to,omitempty"`
	Type    string `json:"type,omitempty"` // "ship", "user", "control"
	Attach  string `json:"attach,omitempty"` // attachment filename if present
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

// parseStatusFile reads a unified status/<agent>.json and returns it.
func parseStatusFile(path string) (StatusRecord, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return StatusRecord{}, false
	}
	var rec StatusRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return StatusRecord{}, false
	}
	return rec, true
}

func parseMsgFile(id, path string) (msgRecord, bool) {
	// Try JSON first (new format)
	data, err := os.ReadFile(path)
	if err != nil {
		// Fallback to TSV (legacy format)
		return parseMsgFileTSV(id, path)
	}

	var msg msgRecord
	if err := json.Unmarshal(data, &msg); err == nil && msg.ID != "" {
		return msg, true
	}

	// If JSON parsing failed, try TSV fallback
	return parseMsgFileTSV(id, path)
}

// parseMsgFileTSV parses the legacy TSV format
func parseMsgFileTSV(id, path string) (msgRecord, bool) {
	line, err := readFirstLine(path)
	if err != nil {
		return msgRecord{}, false
	}
	f := strings.SplitN(line, "\t", 6)
	for len(f) < 6 {
		f = append(f, "")
	}
	epoch, _ := strconv.ParseInt(f[0], 10, 64)
	// TSV format doesn't have Type field, default to "ship"
	return msgRecord{ID: id, Epoch: epoch, ISO: f[1], From: f[2], Target: f[3], Text: f[4], ReplyTo: f[5], Type: "ship"}, true
}

// globSortedFiles lists <dir>/<prefix>*.ext basenames (without extension),
// sorted lexicographically.
func globSortedFiles(dir, prefix, ext string) []string {
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
		if !strings.HasSuffix(n, ext) {
			continue
		}
		if prefix != "" && !strings.HasPrefix(n, prefix) {
			continue
		}
		names = append(names, strings.TrimSuffix(n, ext))
	}
	sort.Strings(names)
	return names
}

func (b *Bus) AllStatusRecords() []StatusRecord {
	var out []StatusRecord
	for _, agent := range globSortedFiles(b.StatusDir, "", ".json") {
		if r, ok := parseStatusFile(filepath.Join(b.StatusDir, agent+".json")); ok {
			out = append(out, r)
		}
	}
	return out
}

func (b *Bus) allMsgRecords() []msgRecord {
	var out []msgRecord
	for _, id := range globSortedFiles(b.MsgDir, "m", ".json") {
		if r, ok := parseMsgFile(id, filepath.Join(b.MsgDir, id+".json")); ok {
			out = append(out, r)
		}
	}
	// Also check for legacy .tsv files for migration
	for _, id := range globSortedFiles(b.MsgDir, "m", ".tsv") {
		// Skip if we already have the .json version
		jsonPath := filepath.Join(b.MsgDir, id+".json")
		if _, err := os.Stat(jsonPath); err == nil {
			continue
		}
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

// MigrateTSVMessages converts all existing .tsv message files to .json format.
// Returns the number of messages migrated and any error encountered.
func (b *Bus) MigrateTSVMessages() (int, error) {
	count := 0
	for _, id := range globSortedFiles(b.MsgDir, "m", ".tsv") {
		tsvPath := filepath.Join(b.MsgDir, id+".tsv")
		jsonPath := filepath.Join(b.MsgDir, id+".json")
		// Skip if JSON already exists
		if _, err := os.Stat(jsonPath); err == nil {
			continue
		}
		msg, ok := parseMsgFile(id, tsvPath)
		if !ok {
			continue
		}
		// Ensure Type is set for legacy messages
		if msg.Type == "" {
			msg.Type = "ship"
		}
		data, err := json.Marshal(msg)
		if err != nil {
			return count, err
		}
		if err := os.WriteFile(jsonPath, data, 0o644); err != nil {
			return count, err
		}
		// Optionally remove the old TSV file after successful migration
		// os.Remove(tsvPath) // Keep TSV as backup for now
		count++
	}
	return count, nil
}

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
