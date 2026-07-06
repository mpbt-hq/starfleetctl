// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
//
// JSON output variants (--json flag) for the list-shaped agent-bus
// subcommands (board/inbox/msgs/asks), so agents consuming this output can
// parse it directly instead of grep/awk/cut-ing the human-formatted table.
// Field names/values mirror the text columns exactly (age as a computed
// integer of seconds rather than the "3m"/"1h2m" string, so callers don't
// have to re-derive it) — same underlying data, just structured.
package agentbus

import (
	"encoding/json"
	"os"
	"strings"
)

type boardEntryJSON struct {
	Agent      string `json:"agent"`
	Project    string `json:"project"`
	State      string `json:"state"`
	AgeSeconds int64  `json:"age_seconds"`
	InboxCount int    `json:"inbox_count"`
	Attach     string `json:"attach"`
	Note       string `json:"note"`
	Stale      bool   `json:"stale"`
}

// DoBoardJSON implements `agent-bus board --json`.
func (b *Bus) DoBoardJSON() error {
	recs := b.allStatusRecords()
	out := make([]boardEntryJSON, 0, len(recs))
	for _, r := range recs {
		out = append(out, boardEntryJSON{
			Agent:      r.Agent,
			Project:    r.Project,
			State:      r.State,
			AgeSeconds: now() - r.Epoch,
			InboxCount: b.inboxCount(r.Agent),
			Attach:     r.Handle,
			Note:       r.Note,
			Stale:      b.stale(r.Epoch),
		})
	}
	return printJSON(out)
}

type inboxEntryJSON struct {
	ID         string `json:"id"`
	AgeSeconds int64  `json:"age_seconds"`
	From       string `json:"from"`
	Acked      bool   `json:"acked"`
	Text       string `json:"text"`
}

// DoInboxJSON implements `agent-bus inbox --json`.
func (b *Bus) DoInboxJSON() error {
	var out []inboxEntryJSON
	for _, m := range b.allMsgRecords() {
		if m.Target != "all" && m.Target != b.AgentID {
			continue
		}
		out = append(out, inboxEntryJSON{
			ID:         m.ID,
			AgeSeconds: now() - m.Epoch,
			From:       m.From,
			Acked:      b.acked(m.ID, b.AgentID),
			Text:       m.Text,
		})
	}
	return printJSON(orEmpty(out))
}

type msgEntryJSON struct {
	ID         string `json:"id"`
	AgeSeconds int64  `json:"age_seconds"`
	From       string `json:"from"`
	Target     string `json:"target"`
	Acks       int    `json:"acks"`
	Text       string `json:"text"`
}

// DoMsgsJSON implements `agent-bus msgs --json`.
func (b *Bus) DoMsgsJSON() error {
	msgs := b.allMsgRecords()
	out := make([]msgEntryJSON, 0, len(msgs))
	entries, _ := os.ReadDir(b.AckDir)
	for _, m := range msgs {
		nacks := 0
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), m.ID+"__") {
				nacks++
			}
		}
		out = append(out, msgEntryJSON{
			ID: m.ID, AgeSeconds: now() - m.Epoch, From: m.From,
			Target: m.Target, Acks: nacks, Text: m.Text,
		})
	}
	return printJSON(out)
}

type askEntryJSON struct {
	ID         string `json:"id"`
	AgeSeconds int64  `json:"age_seconds"`
	From       string `json:"from"`
	Question   string `json:"question"`
}

// DoAsksJSON implements `agent-bus asks --json`.
func (b *Bus) DoAsksJSON() error {
	var out []askEntryJSON
	for _, m := range b.allMsgRecords() {
		if m.Target != b.AgentID || !strings.HasPrefix(m.Text, "[ask] ") || b.acked(m.ID, b.AgentID) {
			continue
		}
		out = append(out, askEntryJSON{
			ID: m.ID, AgeSeconds: now() - m.Epoch, From: m.From,
			Question: strings.TrimPrefix(m.Text, "[ask] "),
		})
	}
	return printJSON(orEmpty(out))
}

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// orEmpty turns a nil slice into json.Marshal-friendly "[]" instead of
// "null" — callers should always get a parseable array, never a null.
func orEmpty[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
