// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
//
// Package logscan implements the automatic feedback loop: it scans the fleet's
// ship logs (var/ships/*.log) and the comms audit trail (events.log) for
// recurring failures and extracts them as structured "findings". Findings can
// be dumped (--dry-run) or captured as dashboard tasks so a ship picks them up.
//
// Why these sources: the ship logs are raw PTY streams that already contain the
// valuable signal — go-x11proto X11 errors, termctl/event-loop panics with
// file:line stack traces, and opencode's own stderr (API errors, quota/rate
// limits). opencode itself logs to a SQLite DB (opencode.db), which is opaque
// and heavy to query; its stderr is mirrored into the PTY log, so we get the
// same signal without touching the DB.
package logscan

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/metux/starfleetctl/internal/config"
)

// Finding is one extracted, de-duplicated failure signature.
type Finding struct {
	Category  string   `json:"category"`   // x11-error | panic | opencode-api | conn-closed
	Signature string   `json:"signature"`  // normalized, stable key for dedup
	Title     string   `json:"title"`      // human-readable one-liner
	Detail    string   `json:"detail"`     // sample context (first occurrence)
	Count     int      `json:"count"`      // number of occurrences across all sources
	Sources   []string `json:"sources"`    // ship names / "events" where seen
	FirstSeen string   `json:"first_seen"` // ISO
	LastSeen  string   `json:"last_seen"`  // ISO
	Component string   `json:"component"`  // go-x11proto | starfleetctl | opencode | ""
	Severity  int      `json:"severity"`   // 1=low .. 3=high
}

// patterns describe what we look for. Each captures a stable signature group.
type pattern struct {
	category string
	severity int
	re       *regexp.Regexp
	// sig extracts the dedup signature + title from a match.
	sig       func(groups []string) (signature, title string)
	component string
}

// anchored at line start to avoid matching inside unrelated text; the X11
// sequence numbers / timestamps are intentionally NOT part of the signature.
var patterns = []pattern{
	{
		category:  "x11-error",
		severity:  3,
		component: "go-x11proto",
		re:        regexp.MustCompile(`X11 Error: (\w+) \(code=(\d+)\), seq=\d+, opcode=(\d+\.\d+)`),
		sig: func(g []string) (string, string) {
			s := "x11:" + g[1] + ":code" + g[2] + ":op" + g[3]
			return s, fmt.Sprintf("X11 %s (code=%s, opcode=%s)", g[1], g[2], g[3])
		},
	},
	{
		category:  "conn-closed",
		severity:  2,
		component: "go-x11proto",
		re:        regexp.MustCompile(`X11 connection closed`),
		sig: func(g []string) (string, string) {
			return "x11:connection-closed", "X11 connection closed (termctl session dropped)"
		},
	},
	{
		category:  "panic",
		severity:  3,
		component: "go-x11proto",
		re:        regexp.MustCompile(`recovered from (?:event handler|draw) panic: (.+)`),
		sig: func(g []string) (string, string) {
			msg := strings.TrimSpace(g[1])
			// Normalize the panic message, stripping volatile bits.
			norm := regexp.MustCompile(`\b0x[0-9a-f]+\b`).ReplaceAllString(msg, "0x…")
			norm = regexp.MustCompile(`\d+`).ReplaceAllString(norm, "#")
			return "panic:" + norm, "panic: " + msg
		},
	},
	{
		category:  "opencode-api",
		severity:  2,
		component: "opencode",
		re:        regexp.MustCompile(`(?i)(production API error|noSuchModelError|tooManyRequests|429|rate limit|quota|model .* not found|context length|no provider available)`),
		sig: func(g []string) (string, string) {
			norm := strings.ToLower(g[1])
			norm = regexp.MustCompile(`\s+`).ReplaceAllString(norm, " ")
			return "ocapi:" + norm, "opencode API issue: " + g[1]
		},
	},
}

// Scan walks the given log files and returns de-duplicated findings.
func Scan(files []string) []Finding {
	bySig := map[string]*Finding{}
	for _, f := range files {
		source := sourceName(f)
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimRight(line, "\r")
			if line == "" {
				continue
			}
			for _, p := range patterns {
				m := p.re.FindStringSubmatch(line)
				if m == nil {
					continue
				}
				sig, title := p.sig(m)
				key := p.category + "|" + sig
				fnd, ok := bySig[key]
				if !ok {
					fnd = &Finding{
						Category:  p.category,
						Signature: sig,
						Title:     title,
						Detail:    firstN(line, 400),
						Component: p.component,
						Severity:  p.severity,
						FirstSeen: tsFromLine(line),
						Sources:   []string{},
					}
					bySig[key] = fnd
				}
				fnd.Count++
				if fnd.LastSeen == "" || tsFromLine(line) > fnd.LastSeen {
					fnd.LastSeen = tsFromLine(line)
				}
				if !contains(fnd.Sources, source) {
					fnd.Sources = append(fnd.Sources, source)
				}
			}
		}
	}

	out := make([]Finding, 0, len(bySig))
	for _, f := range bySig {
		out = append(out, *f)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Severity != out[j].Severity {
			return out[i].Severity > out[j].Severity
		}
		return out[i].Count > out[j].Count
	})
	return out
}

// LogFiles returns the candidate input files: every var/ships/*.log plus the
// comms events.log. Missing files are silently skipped by Scan.
func LogFiles(root string) []string {
	var files []string
	shipDir := filepath.Join(root, ".starfleet-ai", "var", "ships")
	ents, _ := os.ReadDir(shipDir)
	for _, e := range ents {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".log") {
			files = append(files, filepath.Join(shipDir, e.Name()))
		}
	}
	ev := filepath.Join(config.LogDir(root), "events.log")
	if _, err := os.Stat(ev); err == nil {
		files = append(files, ev)
	}
	return files
}

func sourceName(f string) string {
	base := filepath.Base(f)
	if base == "events.log" {
		return "events"
	}
	return strings.TrimSuffix(base, ".log")
}

func firstN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// tsFromLine pulls a timestamp (YYYY/MM/DD HH:MM:SS or RFC3339) from a log
// line, used for FirstSeen/LastSeen. Returns "" when none is found.
func tsFromLine(line string) string {
	if m := regexp.MustCompile(`(\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2})`).FindStringSubmatch(line); m != nil {
		if t, err := time.Parse("2006/01/02 15:04:05", m[1]); err == nil {
			return t.Format(time.RFC3339)
		}
	}
	if m := regexp.MustCompile(`(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2})`).FindStringSubmatch(line); m != nil {
		return m[1]
	}
	return ""
}

// SeenStore records which finding signatures have already been captured, so a
// repeated scan doesn't re-open the same task. Stored as a JSON set under
// .starfleet-ai/var/logscan/seen.json (gitignored, like the ship logs).
type SeenStore struct {
	path string
	seen map[string]bool
}

func LoadSeen(root string) *SeenStore {
	dir := filepath.Join(root, ".starfleet-ai", "var", "logscan")
	_ = os.MkdirAll(dir, 0o755)
	path := filepath.Join(dir, "seen.json")
	s := &SeenStore{path: path, seen: map[string]bool{}}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &s.seen)
	}
	return s
}

// Contains reports whether a signature was already captured.
func (s *SeenStore) Contains(key string) bool { return s.seen[key] }

// SeenMap returns a copy of the captured-signature set (for --reset-seen).
func (s *SeenStore) SeenMap() map[string]bool {
	out := make(map[string]bool, len(s.seen))
	for k := range s.seen {
		out[k] = true
	}
	return out
}

// Forget removes a signature from the captured set (used by --reset-seen;
// callers persist via Mark/rewrite). It does not persist on its own.
func (s *SeenStore) Forget(key string) error {
	delete(s.seen, key)
	data, err := json.MarshalIndent(s.seen, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0o644)
}

// Mark records a signature as captured and persists the store.
func (s *SeenStore) Mark(key string) error {
	s.seen[key] = true
	data, err := json.MarshalIndent(s.seen, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0o644)
}

// Key returns the dedup key for a finding (category|signature).
func (f Finding) Key() string { return f.Category + "|" + f.Signature }
