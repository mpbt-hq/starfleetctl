// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package comms

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
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

	// Toast fields (from plugin commands, shown in web UI).
	ToastVariant string `json:"toast_variant,omitempty"`
	ToastTitle   string `json:"toast_title,omitempty"`
	ToastMessage string `json:"toast_message,omitempty"`

	// Task-status fields (written by Go CLI / status report).
	Task       string `json:"task,omitempty"`
	Progress   int    `json:"progress,omitempty"`
	Blocker    string `json:"blocker,omitempty"`
	ETA        string `json:"eta,omitempty"`
	Branch     string `json:"branch,omitempty"`
	LaunchType string `json:"launch_type,omitempty"`
	Parent     string `json:"parent,omitempty"`
	Provider   string `json:"provider,omitempty"`
	Class      string `json:"class,omitempty"`
	Updated    string `json:"updated,omitempty"`
	// Unattached indicates a working/building status without a task reference.
	// Set when a ship reports working/building but provides neither --task nor --note.
	Unattached bool `json:"unattached,omitempty"`
}

// StatusPatch carries the fields a `status report` invocation wants to
// set. Progress < 0 means "not specified" so a caller can distinguish "leave
// unchanged" from "set to 0".
type StatusPatch struct {
	Task         string
	TaskSet      bool // explicitly set Task even to "" (clear) — status report leaves it unset
	Progress     int
	Blocker      string
	ETA          string
	Branch       string
	Note         string
	LaunchType   string
	Parent       string
	Provider     string
	Class        string
	Model        string
	ToastVariant string
	ToastTitle   string
	ToastMessage string
	Unattached   bool
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
	Type    string `json:"type,omitempty"`   // "ship", "user", "control"
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
	data, err := os.ReadFile(path)
	if err != nil {
		return msgRecord{}, false
	}

	var msg msgRecord
	if err := json.Unmarshal(data, &msg); err == nil && msg.ID != "" {
		return msg, true
	}

	return msgRecord{}, false
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
		path := filepath.Join(b.StatusDir, agent+".json")
		if r, ok := parseStatusFile(path); ok {
			// If Agent field is empty, use the filename (without .json)
			if r.Agent == "" {
				r.Agent = agent
			}
			out = append(out, r)
		}
	}
	return out
}

func (b *Bus) allMsgRecords() []msgRecord {
	var out []msgRecord

	// Scan legacy flat structure (for migration compat)
	for _, id := range globSortedFiles(b.MsgDir, "m", ".json") {
		if r, ok := parseMsgFile(id, filepath.Join(b.MsgDir, id+".json")); ok {
			out = append(out, r)
		}
	}
	for _, id := range globSortedFiles(b.MsgDir, "m", ".tsv") {
		jsonPath := filepath.Join(b.MsgDir, id+".json")
		if _, err := os.Stat(jsonPath); err == nil {
			continue
		}
		if r, ok := parseMsgFile(id, filepath.Join(b.MsgDir, id+".tsv")); ok {
			out = append(out, r)
		}
	}

	// Scan new per-target directory structure: <target>/unseen/ and <target>/seen/
	entries, err := os.ReadDir(b.MsgDir)
	if err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			target := e.Name()
			// Skip hidden dirs
			if strings.HasPrefix(target, ".") {
				continue
			}
			// Scan unseen
			unseenDir := filepath.Join(b.MsgDir, target, "unseen")
			for _, id := range globSortedFiles(unseenDir, "m", ".json") {
				if r, ok := parseMsgFile(id, filepath.Join(unseenDir, id+".json")); ok {
					out = append(out, r)
				}
			}
			// Scan seen
			seenDir := filepath.Join(b.MsgDir, target, "seen")
			for _, id := range globSortedFiles(seenDir, "m", ".json") {
				if r, ok := parseMsgFile(id, filepath.Join(seenDir, id+".json")); ok {
					out = append(out, r)
				}
			}
		}
	}

	// Reverse so newest messages appear first (lexicographic sort is oldest-first).
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// RecentMsgRecords returns up to max of the newest message records (largest
// ids) without parsing the whole store. Message ids are globbed (readdir only,
// no file parsing) across every target directory, the newest max files are
// selected, and only those get parsed. The store can hold tens of thousands
// of records, so the full allMsgRecords() scan is far too slow for a
// polling web view.
func (b *Bus) RecentMsgRecords(max int) []msgRecord {
	if max <= 0 {
		return nil
	}
	files := b.conversationFiles()
	if len(files) > max {
		files = files[:max]
	}
	var out []msgRecord
	for _, f := range files {
		if r, ok := parseMsgFile(f.id, f.path); ok {
			out = append(out, r)
		}
	}
	return out
}

// conversationFiles returns all message files (id+path) across every target
// dir plus legacy flat files, newest id first. Shared by the per-ship
// conversation scans.
func (b *Bus) conversationFiles() []struct {
	id   string
	path string
} {
	var files []struct {
		id   string
		path string
	}
	addDir := func(dir string) {
		for _, id := range globSortedFiles(dir, "m", ".json") {
			files = append(files, struct {
				id   string
				path string
			}{id, filepath.Join(dir, id+".json")})
		}
	}
	addDir(b.MsgDir)
	entries, err := os.ReadDir(b.MsgDir)
	if err == nil {
		for _, e := range entries {
			if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			target := e.Name()
			addDir(filepath.Join(b.MsgDir, target, "unseen"))
			addDir(filepath.Join(b.MsgDir, target, "seen"))
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].id > files[j].id })
	return files
}

// ConversationRecentRecords returns up to max of the newest messages that
// involve ship (From==ship or Target==ship), parsing files newest-first and
// stopping early — the store can hold tens of thousands of records, so a
// full scan is far too slow for a polling web view.
func (b *Bus) ConversationRecentRecords(ship string, max int) []msgRecord {
	if max <= 0 {
		return nil
	}
	var out []msgRecord
	for _, f := range b.conversationFiles() {
		if len(out) >= max {
			break
		}
		r, ok := parseMsgFile(f.id, f.path)
		if !ok {
			continue
		}
		if r.From != ship && r.Target != ship {
			continue
		}
		out = append(out, r)
	}
	return out
}

func (b *Bus) inboxCount(agent string) int {
	cnt := 0
	for _, m := range b.allMsgRecords() {
		if m.Target != agent {
			continue
		}
		// Count only unseen messages (not in seen/)
		if b.acked(m.ID, agent) {
			continue
		}
		cnt++
	}
	return cnt
}

// InboxCounts returns the number of unseen (not-yet-acked) messages per
// recipient ship, computed in a single directory pass. This is equivalent to
// calling inboxCount(agent) for every agent, but avoids re-scanning (and
// re-parsing) the whole message store once per ship — with many ships that
// made the board endpoint O(ships × msgs) instead of O(msgs).
func (b *Bus) InboxCounts() map[string]int {
	counts := make(map[string]int)
	entries, err := os.ReadDir(b.MsgDir)
	if err != nil {
		return counts
	}
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		agent := e.Name()
		unseenDir := filepath.Join(b.MsgDir, agent, "unseen")
		unseen, err := os.ReadDir(unseenDir)
		if err != nil {
			continue
		}
		n := 0
		for _, f := range unseen {
			if !f.IsDir() {
				n++
			}
		}
		if n > 0 {
			counts[agent] = n
		}
	}
	return counts
}

// knownShips returns the sorted set of every ship ID known to the fleet: the
// union of the per-ship message dirs under MsgDir and the agents that have a
// status record on the board. Broadcasts are fanned out to exactly this set
// at post time (no shared "all" pseudo-target anymore).
func (b *Bus) knownShips() []string {
	known := make(map[string]bool)
	entries, err := os.ReadDir(b.MsgDir)
	if err == nil {
		for _, e := range entries {
			if !e.IsDir() || strings.HasPrefix(e.Name(), ".") || e.Name() == "all" {
				continue
			}
			known[e.Name()] = true
		}
	}
	for _, r := range b.AllStatusRecords() {
		if r.Agent != "" && !strings.HasPrefix(r.Agent, ".") && r.Agent != "all" {
			known[r.Agent] = true
		}
	}
	out := make([]string, 0, len(known))
	for s := range known {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// broadcastRecipients returns the sorted recipient set for a broadcast:
// every known ship plus the sender itself, so a ship always sees its own
// broadcasts in its inbox (matching the old shared "all"-dir behavior).
func (b *Bus) broadcastRecipients() []string {
	recs := b.knownShips()
	found := false
	for _, r := range recs {
		if r == b.ShipID {
			found = true
			break
		}
	}
	if !found {
		recs = append(recs, b.ShipID)
		sort.Strings(recs)
	}
	return recs
}

// shipExists reports whether a status file exists for the given ship name.
func (b *Bus) shipExists(name string) bool {
	path := filepath.Join(b.StatusDir, name+".json")
	_, err := os.Stat(path)
	return err == nil
}
