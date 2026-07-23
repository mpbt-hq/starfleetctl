// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
//
// Package comms is the Go port of scripts/agent-bus — the file-based
// status + directive bus for coordinating many independent agent sessions.
// It reads/writes the exact same .starfleet-ai/var/agent-bus/ file format as the
// bash original, so a Go and bash session can interoperate on one bus
// without either side knowing the other is a different implementation.
package comms

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/metux/starfleetctl/internal/config"
	"github.com/metux/starfleetctl/internal/fsutil"
	"github.com/metux/starfleetctl/internal/identity"
)

// Bus holds one invocation's resolved identity + storage locations, mirroring
// the environment-derived globals at the top of scripts/agent-bus.
type Bus struct {
	Root      string // workspace root (parent of the script's own dir, in bash; here just $STARFLEET_BUS_DIR's parent's parent)
	BusDir    string
	BusTTL    int64
	ShipID    string
	ShipIDSet bool
	Project   string
	Handle    string

	StatusDir string
	MsgDir    string
	AttachDir string
	Events    string
}

// New resolves a Bus from the environment exactly like the bash script's
// top-of-file variable setup (STARFLEET_BUS_DIR, STARFLEET_BUS_TTL, STARFLEET_SHIP_ID,
// PROJECT, STARFLEET_AGENT_HANDLE), given the workspace root.
func New(root string) (*Bus, error) {
	busDir := os.Getenv("STARFLEET_BUS_DIR")
	if busDir == "" {
		busDir = config.BusDir(root)
	}
	ttl := int64(900)
	if v := os.Getenv("STARFLEET_BUS_TTL"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			ttl = n
		}
	}
	shipID := identity.ShipID()
	shipIDSet := shipID != ""
	if !shipIDSet {
		shipID = defaultAgentID()
	}
	project := os.Getenv("PROJECT")
	handle := os.Getenv("STARFLEET_AGENT_HANDLE")

	b := &Bus{
		Root:      root,
		BusDir:    busDir,
		BusTTL:    ttl,
		ShipID:    shipID,
		ShipIDSet: shipIDSet,
		Project:   project,
		Handle:    handle,
		StatusDir: filepath.Join(busDir, "status"),
		MsgDir:    filepath.Join(busDir, "msgs"),
		AttachDir: filepath.Join(busDir, "attachments"),
		Events:    filepath.Join(config.LogDir(root), "events.log"),
	}
	for _, d := range []string{b.StatusDir, b.MsgDir, b.AttachDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, err
		}
	}

	// Migration + backwards-compat symlink: move health/*.json → status/*.json,
	// then symlink health/ → status/ so the opencode plugin (which writes to
	// health/<ship>.json) continues to work transparently.
	healthDir := filepath.Join(busDir, "health")
	if info, err := os.Lstat(healthDir); err == nil && info.IsDir() {
		// Migrate existing health JSON files into status/.
		if hEntries, err := os.ReadDir(healthDir); err == nil {
			for _, he := range hEntries {
				if he.IsDir() || !strings.HasSuffix(he.Name(), ".json") {
					continue
				}
				src := filepath.Join(healthDir, he.Name())
				dst := filepath.Join(b.StatusDir, he.Name())
				if _, err := os.Stat(dst); os.IsNotExist(err) {
					os.Rename(src, dst)
				} else {
					os.Remove(src)
				}
			}
		}
		os.Remove(healthDir) // remove the real directory
	}
	if _, err := os.Lstat(healthDir); os.IsNotExist(err) {
		os.Symlink("status", healthDir)
	}

	return b, nil
}

func defaultAgentID() string {
	uname := "user"
	if u, err := user.Current(); err == nil && u.Username != "" {
		uname = u.Username
	}
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "host"
	}
	// bash uses `hostname -s` (short form); trim at the first dot to match.
	if i := indexByte(host, '.'); i >= 0 {
		host = host[:i]
	}
	return fmt.Sprintf("%s@%s", uname, host)
}

func indexByte(s string, c byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}

func now() int64 { return time.Now().Unix() }

func isots() string { return time.Now().Format(time.RFC3339) }

// clean mirrors bash's `tr '\t\n' '  '` — flattens embedded tabs/newlines to
// spaces so a value can't smuggle extra TSV fields or lines into a record.
func clean(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c == '\t' || c == '\n' {
			b[i] = ' '
		}
	}
	return string(b)
}

// fsafe mirrors bash's `tr -c 'A-Za-z0-9._-' '_'` for building filesystem-safe
// file names from an arbitrary agent id / message id.
func fsafe(s string) string {
	b := []byte(s)
	for i, c := range b {
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-') {
			b[i] = '_'
		}
	}
	return string(b)
}

func (b *Bus) sfile(agent string) string {
	return filepath.Join(b.StatusDir, fsafe(agent)+".json")
}

func (b *Bus) mfile(id string, target string) (string, error) {
	safe, ok := fsutil.Safe(id)
	if !ok {
		return "", fmt.Errorf("comms: invalid message id %q", id)
	}
	if target == "" {
		// For backwards compat during migration: try to find the message in any target subdir
		return filepath.Join(b.MsgDir, safe+".json"), nil
	}
	// New structure: target/unseen/
	targetSafe := fsafe(target)
	targetDir := filepath.Join(b.MsgDir, targetSafe, "unseen")
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return "", err
	}
	return filepath.Join(targetDir, safe+".json"), nil
}

// mfileSeen returns the path for a message in a specific target's seen dir
func (b *Bus) mfileSeen(target, id string) (string, error) {
	safe, ok := fsutil.Safe(id)
	if !ok {
		return "", fmt.Errorf("comms: invalid message id %q", id)
	}
	targetSafe := fsafe(target)
	seenDir := filepath.Join(b.MsgDir, targetSafe, "seen")
	if err := os.MkdirAll(seenDir, 0o755); err != nil {
		return "", err
	}
	return filepath.Join(seenDir, safe+".json"), nil
}

func (b *Bus) acked(id, agent string) bool {
	// A message is acked if it exists in the seen/ directory for that agent
	seenPath := filepath.Join(b.MsgDir, fsafe(agent), "seen", fsafe(id)+".json")
	_, err := os.Stat(seenPath)
	return err == nil
}

func (b *Bus) stale(epoch int64, state string) bool {
	// A ship that is intentionally idle (client still alive, just nothing to
	// do) must not read as STALE — only ships that stopped heartbeating while
	// in an active state (working/blocked/…) are considered dead.
	if state == "idle" {
		return false
	}
	return now()-epoch >= b.BusTTL
}

// age mirrors bash's age() formatting: Ns / Nm / NhNm.
func age(epoch int64) string {
	s := now() - epoch
	if s < 0 {
		s = 0
	}
	switch {
	case s >= 3600:
		return fmt.Sprintf("%dh%dm", s/3600, (s%3600)/60)
	case s >= 60:
		return fmt.Sprintf("%dm", s/60)
	default:
		return fmt.Sprintf("%ds", s)
	}
}

// allTargetsAcked returns true if every live agent that is a target of
// the message has acknowledged it. Used to auto-delete messages when
// all live targets have acked (so old messages don't resurface on restart).
func (b *Bus) allTargetsAcked(id string, live map[string]bool) bool {
	// We need to find the message to get its target
	var msg msgRecord
	found := false
	for _, m := range b.allMsgRecords() {
		if m.ID == id {
			msg = m
			found = true
			break
		}
	}
	if !found {
		return false
	}
	path, err := b.mfile(id, msg.Target)
	if err != nil {
		return false
	}
	m, ok := parseMsgFile(id, path)
	if !ok {
		return false
	}
	for agent := range live {
		if m.Target != "all" && m.Target != agent {
			continue
		}
		if !b.acked(m.ID, agent) {
			return false
		}
	}
	return true
}

func (b *Bus) logEvent(kind, note string) {
	f, err := os.OpenFile(b.Events, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return // best-effort, matches bash's `|| true`
	}
	defer f.Close()
	fmt.Fprintf(f, "%s\t%s\t%s\t%s\n", isots(), kind, b.ShipID, clean(note))
}

// ackedCount returns the number of agents who have acked (seen) a message
func (b *Bus) ackedCount(id string) int {
	count := 0
	entries, err := os.ReadDir(b.MsgDir)
	if err != nil {
		return 0
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		seenPath := filepath.Join(b.MsgDir, e.Name(), "seen", fsafe(id)+".json")
		if _, err := os.Stat(seenPath); err == nil {
			count++
		}
	}
	return count
}

// warnID warns if STARFLEET_SHIP_ID is not set (uses fallback "anon-<pid>").
func (b *Bus) warnID() {
	if !b.ShipIDSet {
		fmt.Fprintf(os.Stderr, "comms: note: STARFLEET_SHIP_ID not set; using '%s' — set a unique STARFLEET_SHIP_ID per agent.\n", b.ShipID)
	}
}

// findMsgFile searches for a message file by ID across all target directories.
// Returns the path and the target if found.
func (b *Bus) findMsgFile(id string) (string, string, bool) {
	safe, ok := fsutil.Safe(id)
	if !ok {
		return "", "", false
	}

	// First check old flat location (migration compat)
	oldPath := filepath.Join(b.MsgDir, safe+".json")
	if _, err := os.Stat(oldPath); err == nil {
		// Try to read to get target
		if m, ok := parseMsgFile(id, oldPath); ok {
			return oldPath, m.Target, true
		}
		return oldPath, "", true
	}

	// Search in target subdirectories
	entries, err := os.ReadDir(b.MsgDir)
	if err != nil {
		return "", "", false
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		target := e.Name()
		// Check unseen
		path := filepath.Join(b.MsgDir, target, "unseen", safe+".json")
		if _, err := os.Stat(path); err == nil {
			return path, target, true
		}
		// Check seen
		path = filepath.Join(b.MsgDir, target, "seen", safe+".json")
		if _, err := os.Stat(path); err == nil {
			return path, target, true
		}
	}
	return "", "", false
}
