// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
//
// Package shipnames is the Go port of scripts/ship-names — the Star Trek
// ship name registry each agent instance uses for its STARFLEET_SHIP_ID, so the
// agent-bus board and detached session list read like a fleet roster instead of
// a wall of PIDs. Enterprise is reserved for the control/flagship session.
// Reservations live in .starfleet-ai/var/agent-bus/ships/ (one file per active name,
// gitignored, SAME format/location as the bash original), so a Go and bash
// invocation share the registry transparently.
package shipnames

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/metux/starfleetctl/internal/config"
	"github.com/metux/starfleetctl/internal/fsutil"
)

const Flagship = "Enterprise"

// Registry holds one invocation's resolved locations.
type Registry struct {
	Root      string
	NamesFile string // <root>/.starfleet-ai/etc/ship-names.txt
	ShipsDir  string
	StatusDir string
}

func New(root string) *Registry {
	busDir := config.BusDir(root)
	return &Registry{
		Root:      root,
		NamesFile: filepath.Join(root, ".starfleet-ai", "etc", "ship-names.txt"),
		ShipsDir:  filepath.Join(busDir, "ships"),
		StatusDir: filepath.Join(busDir, "status"),
	}
}

func (r *Registry) shipFile(name string) (string, error) {
	safe, ok := fsutil.Safe(name)
	if !ok {
		return "", fmt.Errorf("ship-names: invalid name %q", name)
	}
	return filepath.Join(r.ShipsDir, safe), nil
}

// readNames returns the candidate ship names from NamesFile, in file order,
// skipping blank lines, '#' comments, and the flagship name — mirrors the
// bash `while IFS= read -r name || [ -n "$name" ]; case ... esac` loop.
func (r *Registry) readNames() ([]string, error) {
	data, err := os.ReadFile(r.NamesFile)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, line := range splitLines(string(data)) {
		if line == "" || line[0] == '#' || line == Flagship {
			continue
		}
		names = append(names, line)
	}
	return names, nil
}

func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}
