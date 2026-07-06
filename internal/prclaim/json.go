// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package prclaim

import (
	"encoding/json"
	"os"
)

type claimEntryJSON struct {
	PR         string `json:"pr"`
	Agent      string `json:"agent"`
	AgeSeconds int64  `json:"age_seconds"`
	Note       string `json:"note"`
	Stale      bool   `json:"stale"`
}

// DoListJSON implements `pr-claim --list --json`.
func (c *Claims) DoListJSON() error {
	claims := c.allClaims()
	out := make([]claimEntryJSON, 0, len(claims))
	for _, r := range claims {
		out = append(out, claimEntryJSON{
			PR: r.PR, Agent: r.Agent, AgeSeconds: now() - r.Epoch,
			Note: r.Note, Stale: c.stale(r.Epoch),
		})
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}
