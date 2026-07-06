// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package shipnames

import (
	"encoding/json"
	"os"
	"strconv"
	"strings"
)

type shipEntryJSON struct {
	Name     string `json:"name"`
	Flagship bool   `json:"flagship"`
	Active   bool   `json:"active"`
	PID      int    `json:"pid,omitempty"`
	Since    int64  `json:"since,omitempty"` // unix epoch the reservation was made
}

// DoListJSON implements `ship-names list --json`.
func (r *Registry) DoListJSON() error {
	var out []shipEntryJSON

	out = append(out, entryFor(r, Flagship, true))

	names, err := r.readNames()
	if err != nil {
		return err
	}
	for _, name := range names {
		out = append(out, entryFor(r, name, false))
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

func entryFor(r *Registry, name string, flagship bool) shipEntryJSON {
	e := shipEntryJSON{Name: name, Flagship: flagship}
	data, err := os.ReadFile(r.shipFile(name))
	if err != nil {
		return e
	}
	e.Active = true
	payload := firstLine(string(data))
	parts := strings.SplitN(payload, ":", 2)
	if len(parts) == 2 {
		if pid, err := strconv.Atoi(parts[0]); err == nil {
			e.PID = pid
		}
		if since, err := strconv.ParseInt(parts[1], 10, 64); err == nil {
			e.Since = since
		}
	}
	return e
}
