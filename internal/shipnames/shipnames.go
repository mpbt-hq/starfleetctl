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

// defaultNames is the compiled-in ship-name pool. Order matters: AssignName
// picks the first unused name, so more "important" ships come first.
// Users can override or extend this list via .starfleet-ai/conf/ship-names.yaml.
var defaultNames = []string{
	// Federation
	"Defiant", "Voyager", "Discovery", "Excelsior", "Reliant",
	"Hood", "Potemkin", "Endeavour", "Farragut", "Constellation",
	"Intrepid", "Pegasus", "Sutherland", "Agamemnon", "Saratoga",
	"Lexington", "Yamato", "Phoenix", "Prometheus", "Titan",
	"Thunderchild", "Odyssey", "Pasteur", "Bozeman", "Stargazer",
	"Hathaway", "Trieste", "Grissom", "Tsiolkovsky",
	// Klingon
	"Rotarran", "Bortas", "Negh-Var", "Klothos", "Gr-oth",
	"Pagh", "Orantho", "Toh-Kaht", "Maht-Ha", "Hegh-ta",
	// Romulan
	"Valdore", "Decius", "Haakona", "Devoras", "T-Met",
	// Cardassian
	"Groumall", "Trager", "Prakesh",
	// Bajoran / Maquis
	"Jas-Tor", "Val-Jean",
}

// Registry holds one invocation's resolved locations.
type Registry struct {
	Root      string
	ShipsDir  string
	StatusDir string
	ConfDir   string // <root>/.starfleet-ai/conf/
}

func New(root string) *Registry {
	busDir := config.BusDir(root)
	return &Registry{
		Root:      root,
		ShipsDir:  filepath.Join(busDir, "ships"),
		StatusDir: filepath.Join(busDir, "status"),
		ConfDir:   filepath.Join(root, ".starfleet-ai", "conf"),
	}
}

func (r *Registry) shipFile(name string) (string, error) {
	safe, ok := fsutil.Safe(name)
	if !ok {
		return "", fmt.Errorf("ship-names: invalid name %q", name)
	}
	return filepath.Join(r.ShipsDir, safe), nil
}

// readNames returns the candidate ship names: the compiled-in defaults,
// optionally overridden by .starfleet-ai/conf/ship-names.yaml.
// YAML format: a plain list of names (one per line) or a map with a "names" key.
// The flagship name ("Enterprise") is always excluded from the pool.
func (r *Registry) readNames() ([]string, error) {
	names, err := r.readNamesYAML()
	if err != nil || len(names) == 0 {
		// YAML unreadable, missing, or empty — fall back to compiled-in defaults
		names = make([]string, len(defaultNames))
		copy(names, defaultNames)
	}
	// Filter out the flagship
	var out []string
	for _, n := range names {
		if n != "" && n != Flagship {
			out = append(out, n)
		}
	}
	return out, nil
}

// readNamesYAML tries to load .starfleet-ai/conf/ship-names.yaml.
// Returns nil, nil if the file doesn't exist.
func (r *Registry) readNamesYAML() ([]string, error) {
	path := filepath.Join(r.ConfDir, "ship-names.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return parseShipNamesYAML(data)
}

// parseShipNamesYAML parses a simple YAML list of ship names.
// Supports two formats:
//   - Plain list (one name per line, possibly with "- " prefix)
//   - Map with "names:" key containing the list
func parseShipNamesYAML(data []byte) ([]string, error) {
	var names []string
	lines := splitLines(string(data))
	inNamesBlock := false
	for _, line := range lines {
		trimmed := trimSpace(line)
		if trimmed == "" || trimmed[0] == '#' {
			continue
		}
		// Check for "names:" block start
		if trimmed == "names:" || trimmed == "names :" {
			inNamesBlock = true
			continue
		}
		// If in a names block, collect indented lines
		if inNamesBlock {
			if len(line) > 0 && (line[0] == ' ' || line[0] == '\t') {
				name := parseYAMLListItem(trimmed)
				if name != "" {
					names = append(names, name)
				}
				continue
			}
			// Non-indented line ends the block
			inNamesBlock = false
		}
		// Plain list item: "- Name" or just "Name"
		name := parseYAMLListItem(trimmed)
		if name != "" {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("ship-names.yaml: no names found")
	}
	return names, nil
}

func parseYAMLListItem(s string) string {
	// Strip "- " prefix
	if len(s) >= 2 && s[0] == '-' && s[1] == ' ' {
		s = s[2:]
	}
	return trimSpace(s)
}

func trimSpace(s string) string {
	start, end := 0, len(s)
	for start < end && (s[start] == ' ' || s[start] == '\t') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t') {
		end--
	}
	return s[start:end]
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
