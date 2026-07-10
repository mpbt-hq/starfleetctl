// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package prclaim

import (
	"os"
	"sort"
	"strconv"
	"strings"
)

// claimRecord mirrors one pr-<N>.tsv line: epoch \t isots \t agent \t note
type claimRecord struct {
	PR    string
	Epoch int64
	ISO   string
	Agent string
	Note  string
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

func (c *Claims) readClaim(pr string) (claimRecord, bool) {
	line, err := readFirstLine(c.cfile(pr))
	if err != nil {
		return claimRecord{}, false
	}
	f := strings.SplitN(line, "\t", 4)
	for len(f) < 4 {
		f = append(f, "")
	}
	epoch, _ := strconv.ParseInt(f[0], 10, 64)
	return claimRecord{PR: pr, Epoch: epoch, ISO: f[1], Agent: f[2], Note: f[3]}, true
}

func (c *Claims) writeClaim(pr, note string) error {
	line := now()
	content := strconv.FormatInt(line, 10) + "\t" + isots() + "\t" + c.ShipID + "\t" + clean(note) + "\n"
	return os.WriteFile(c.cfile(pr), []byte(content), 0o644)
}

// allClaims lists pr-*.tsv claims sorted like bash glob expansion.
func (c *Claims) allClaims() []claimRecord {
	entries, err := os.ReadDir(c.ClaimDir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasPrefix(n, "pr-") || !strings.HasSuffix(n, ".tsv") {
			continue
		}
		names = append(names, n)
	}
	sort.Strings(names)

	var out []claimRecord
	for _, n := range names {
		pr := strings.TrimSuffix(strings.TrimPrefix(n, "pr-"), ".tsv")
		if r, ok := c.readClaim(pr); ok {
			out = append(out, r)
		}
	}
	return out
}

func (c *Claims) removeClaim(pr string) error {
	return os.Remove(c.cfile(pr))
}
