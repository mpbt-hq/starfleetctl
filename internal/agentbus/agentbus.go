// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
//
// Package agentbus is the Go port of scripts/agent-bus — the file-based
// status + directive bus for coordinating many independent agent sessions.
// It reads/writes the exact same _WORK_/agent-bus/ TSV file format as the
// bash original, so a Go and bash session can interoperate on one bus
// without either side knowing the other is a different implementation.
package agentbus

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"time"

	"github.com/metux/starfleetctl/internal/identity"
)

// Bus holds one invocation's resolved identity + storage locations, mirroring
// the environment-derived globals at the top of scripts/agent-bus.
type Bus struct {
	Root       string // workspace root (parent of the script's own dir, in bash; here just $BUS_DIR's parent's parent)
	BusDir     string
	BusTTL     int64
	ShipID     string
	ShipIDSet  bool
	Project    string
	Handle     string

	StatusDir string
	MsgDir    string
	AckDir    string
	AttachDir string
	Events    string
}

// New resolves a Bus from the environment exactly like the bash script's
// top-of-file variable setup (BUS_DIR, BUS_TTL, STARFLEET_SHIP_ID/AGENT_ID,
// XLIBRE_RELEASE/PROJECT, STARFLEET_AGENT_HANDLE/AGENT_HANDLE), given the workspace root.
func New(root string) (*Bus, error) {
	busDir := os.Getenv("BUS_DIR")
	if busDir == "" {
		busDir = filepath.Join(root, "_WORK_", "agent-bus")
	}
	ttl := int64(900)
	if v := os.Getenv("BUS_TTL"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			ttl = n
		}
	}
	shipID := identity.ShipID()
	shipIDSet := shipID != ""
	if !shipIDSet {
		shipID = defaultAgentID()
	}
	project := os.Getenv("XLIBRE_RELEASE")
	if project == "" {
		project = os.Getenv("PROJECT")
	}
	handle := os.Getenv("STARFLEET_AGENT_HANDLE")
	if handle == "" {
		handle = os.Getenv("AGENT_HANDLE")
	}

	b := &Bus{
		Root:       root,
		BusDir:     busDir,
		BusTTL:     ttl,
		ShipID:    shipID,
		ShipIDSet: shipIDSet,
		Project:    project,
		Handle:     handle,
		StatusDir:  filepath.Join(busDir, "status"),
		MsgDir:     filepath.Join(busDir, "msgs"),
		AckDir:     filepath.Join(busDir, "acks"),
		AttachDir:  filepath.Join(busDir, "attachments"),
		Events:     filepath.Join(busDir, "events.log"),
	}
	for _, d := range []string{b.StatusDir, b.MsgDir, b.AckDir, b.AttachDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, err
		}
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
	return filepath.Join(b.StatusDir, fsafe(agent)+".tsv")
}

func (b *Bus) mfile(id string) string {
	return filepath.Join(b.MsgDir, id+".tsv")
}

func (b *Bus) ackmark(id, agent string) string {
	return filepath.Join(b.AckDir, id+"__"+fsafe(agent))
}

func (b *Bus) acked(id, agent string) bool {
	_, err := os.Stat(b.ackmark(id, agent))
	return err == nil
}

func (b *Bus) stale(epoch int64) bool {
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
	m, ok := parseMsgFile(id, b.mfile(id))
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

func (b *Bus) warnID() {
	if !b.ShipIDSet {
		fmt.Fprintf(os.Stderr, "agent-bus: note: STARFLEET_SHIP_ID (or AGENT_ID) not set; using '%s' — set a unique ship ID per session.\n", b.ShipID)
	}
}
