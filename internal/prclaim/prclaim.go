// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
//
// Package prclaim is the Go port of scripts/pr-claim — the advisory,
// cross-clone PR work registry + lock. Each agent works in its own clone,
// but every clone pushes to the SAME GitHub PR branch, so this registry
// coordinates who is mutating which PR (claim before you touch it) and
// doubles as the shared "what is each agent working on" log (--list).
//
// ADVISORY ONLY, like the bash original: it only coordinates actors that
// also use pr-claim (Go or bash — same _WORK_/agent-claims/ file format).
package prclaim

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"time"

	"github.com/metux/starfleetctl/internal/config"
	"github.com/metux/starfleetctl/internal/fsutil"
	"github.com/metux/starfleetctl/internal/identity"
)

// Claims holds one invocation's resolved identity + storage locations.
type Claims struct {
	Root      string
	ClaimDir  string
	ClaimTTL  int64
	ShipID    string
	ShipIDSet bool
	Events    string
}

// New resolves Claims from the environment, mirroring scripts/pr-claim's
// top-of-file variable setup (CLAIM_DIR, CLAIM_TTL, STARFLEET_SHIP_ID).
func New(root string) (*Claims, error) {
	dir := os.Getenv("CLAIM_DIR")
	if dir == "" {
		dir = filepath.Join(config.WorkDir(root), "prclaims")
	}
	ttl := int64(3600)
	if v := os.Getenv("CLAIM_TTL"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			ttl = n
		}
	}
	shipID := identity.ShipID()
	shipIDSet := shipID != ""
	if !shipIDSet {
		shipID = defaultAgentID()
	}

	c := &Claims{
		Root:      root,
		ClaimDir:  dir,
		ClaimTTL:  ttl,
		ShipID:    shipID,
		ShipIDSet: shipIDSet,
		Events:    filepath.Join(config.LogDir(root), "events.log"),
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return c, nil
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

// clean mirrors bash's `tr '\t\n' '  '`.
func clean(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c == '\t' || c == '\n' {
			b[i] = ' '
		}
	}
	return string(b)
}

func (c *Claims) cfile(pr string) (string, error) {
	safe, ok := fsutil.Safe(pr)
	if !ok {
		return "", fmt.Errorf("pr-claim: invalid PR id %q", pr)
	}
	return filepath.Join(c.ClaimDir, "pr-"+safe+".tsv"), nil
}

func (c *Claims) stale(epoch int64) bool {
	return now()-epoch >= c.ClaimTTL
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

func (c *Claims) logEvent(kind, note string) {
	f, err := os.OpenFile(c.Events, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s\t%s\t%s\t%s\n", isots(), kind, c.ShipID, clean(note))
}

func (c *Claims) warnID() {
	if !c.ShipIDSet {
		fmt.Fprintf(os.Stderr, "pr-claim: note: STARFLEET_SHIP_ID not set; using '%s' — set a unique STARFLEET_SHIP_ID per agent.\n", c.ShipID)
	}
}
