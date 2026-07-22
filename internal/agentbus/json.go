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
	// Structured detail (status/<ship>.json); absent when the ship never
	// reported one. Mirrors the fields of StatusDetail.
	Task       string `json:"task,omitempty"`
	Progress   int    `json:"progress,omitempty"`
	Blocker    string `json:"blocker,omitempty"`
	ETA        string `json:"eta,omitempty"`
	Branch     string `json:"branch,omitempty"`
	LaunchType string `json:"launch_type,omitempty"`
	Parent     string `json:"parent,omitempty"`
	Provider   string `json:"provider,omitempty"`
	Model      string `json:"model,omitempty"`
	Updated    string `json:"updated,omitempty"`
}

// BoardEntries returns the same board data that `agent-bus board --json`
// prints, as a slice — for programmatic callers (e.g. task capture's free-ship
// picker) that need it without parsing stdout.
func (b *Bus) BoardEntries() []boardEntryJSON {
	recs := b.AllStatusRecords()
	out := make([]boardEntryJSON, 0, len(recs))
	for _, r := range recs {
		e := boardEntryJSON{
			Agent:      r.Agent,
			Project:    r.Project,
			State:      r.State,
			AgeSeconds: now() - r.Epoch,
			InboxCount: b.inboxCount(r.Agent),
			Attach:     r.Handle,
			Note:       r.Note,
			Stale:      b.stale(r.Epoch, r.State),
		}
		// Enrich with task/model fields from the unified record.
		if r.Task != "" {
			e.Task = r.Task
		}
		if r.Progress > 0 {
			e.Progress = r.Progress
		}
		if r.Blocker != "" {
			e.Blocker = r.Blocker
		}
		if r.ETA != "" {
			e.ETA = r.ETA
		}
		if r.Branch != "" {
			e.Branch = r.Branch
		}
		if r.LaunchType != "" {
			e.LaunchType = r.LaunchType
		}
		if r.Parent != "" {
			e.Parent = r.Parent
		}
		if r.Provider != "" {
			e.Provider = r.Provider
		}
		if r.Model != "" {
			e.Model = r.Model
		}
		if r.Updated != "" {
			e.Updated = r.Updated
		}
		out = append(out, e)
	}
	return out
}

// DoBoardJSON implements `agent-bus board --json`.
func (b *Bus) DoBoardJSON() error {
	return printJSON(b.BoardEntries())
}

type inboxEntryJSON struct {
	ID         string `json:"id"`
	AgeSeconds int64  `json:"age_seconds"`
	From       string `json:"from"`
	Acked      bool   `json:"acked"`
	Text       string `json:"text"`
	ReplyTo    string `json:"reply_to,omitempty"`
}

// DoInboxJSON implements `agent-bus inbox --json`.
func (b *Bus) DoInboxJSON() error {
	var out []inboxEntryJSON
	for _, m := range b.allMsgRecords() {
		if m.Target != "all" && m.Target != b.ShipID {
			continue
		}
		out = append(out, inboxEntryJSON{
			ID:         m.ID,
			AgeSeconds: now() - m.Epoch,
			From:       m.From,
			Acked:      b.acked(m.ID, b.ShipID),
			Text:       m.Text,
			ReplyTo:    m.ReplyTo,
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
	ReplyTo    string `json:"reply_to,omitempty"`
}

// DoMsgsJSON implements `agent-bus msgs --json`.
func (b *Bus) DoMsgsJSON() error {
	msgs := b.allMsgRecords()
	out := make([]msgEntryJSON, 0, len(msgs))
	for _, m := range msgs {
		nacks := b.ackedCount(m.ID)
		out = append(out, msgEntryJSON{
			ID: m.ID, AgeSeconds: now() - m.Epoch, From: m.From,
			Target: m.Target, Acks: nacks, Text: m.Text, ReplyTo: m.ReplyTo,
		})
	}
	return printJSON(out)
}

// Conversation returns the messages involving a single ship (sent by it,
// addressed to it, or a broadcast to all) — used by the web console's
// per-ship chat view. Each entry is identical in shape to DoMsgsJSON.
func (b *Bus) Conversation(ship string) []msgEntryJSON {
	msgs := b.allMsgRecords()
	out := make([]msgEntryJSON, 0, len(msgs))
	for _, m := range msgs {
		if m.From != ship && m.Target != ship && m.Target != "all" {
			continue
		}
		out = append(out, msgEntryJSON{
			ID: m.ID, AgeSeconds: now() - m.Epoch, From: m.From,
			Target: m.Target, Acks: b.ackedCount(m.ID), Text: m.Text, ReplyTo: m.ReplyTo,
		})
	}
	return out
}

// ConversationWithViewer is like Conversation(ship) but additionally keeps
// any message addressed to or sent by the web viewer (b.ShipID). This is what
// makes a ship's reply visible in the web Funk tab when the user chats with a
// ship over the web console: the ship replies to the viewer's identity (often
// a direct tell, not a broadcast), so the reply's target is the viewer — not
// the chat partner and not "all". Without this, such replies would be filtered
// out and the user would never see the answer they asked for in the UI.
func (b *Bus) ConversationWithViewer(ship, viewer string) []msgEntryJSON {
	msgs := b.allMsgRecords()
	out := make([]msgEntryJSON, 0, len(msgs))
	for _, m := range msgs {
		involved := m.From == ship || m.Target == ship || m.Target == "all" ||
			(viewer != "" && (m.From == viewer || m.Target == viewer))
		if !involved {
			continue
		}
		out = append(out, msgEntryJSON{
			ID: m.ID, AgeSeconds: now() - m.Epoch, From: m.From,
			Target: m.Target, Acks: b.ackedCount(m.ID), Text: m.Text, ReplyTo: m.ReplyTo,
		})
	}
	return out
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
		if m.Target != b.ShipID || !strings.HasPrefix(m.Text, "[ask] ") || b.acked(m.ID, b.ShipID) {
			continue
		}
		out = append(out, askEntryJSON{
			ID: m.ID, AgeSeconds: now() - m.Epoch, From: m.From,
			Question: strings.TrimPrefix(m.Text, "[ask] "),
		})
	}
	return printJSON(orEmpty(out))
}

// AllMsgRecordsJSON returns every directive on the bus as a JSON-shaped slice,
// for programmatic callers (e.g. the web UI) that need the full list without
// parsing stdout or re-deriving ack counts.
func (b *Bus) AllMsgRecordsJSON() []msgEntryJSON {
	msgs := b.allMsgRecords()
	out := make([]msgEntryJSON, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, msgEntryJSON{
			ID: m.ID, AgeSeconds: now() - m.Epoch, From: m.From,
			Target: m.Target, Acks: b.ackedCount(m.ID), Text: m.Text,
		})
	}
	return out
}

// AllInboxRecordsJSON returns the inbox (directives addressed to my ship or
// to all) as a JSON-shaped slice.
func (b *Bus) AllInboxRecordsJSON() []inboxEntryJSON {
	var out []inboxEntryJSON
	for _, m := range b.allMsgRecords() {
		if m.Target != "all" && m.Target != b.ShipID {
			continue
		}
		out = append(out, inboxEntryJSON{
			ID: m.ID, AgeSeconds: now() - m.Epoch, From: m.From,
			Acked: b.acked(m.ID, b.ShipID), Text: m.Text,
		})
	}
	return out
}

// AllAskRecordsJSON returns the pending questions addressed to my ship as a
// JSON-shaped slice.
func (b *Bus) AllAskRecordsJSON() []askEntryJSON {
	var out []askEntryJSON
	for _, m := range b.allMsgRecords() {
		if m.Target != b.ShipID || !strings.HasPrefix(m.Text, "[ask] ") || b.acked(m.ID, b.ShipID) {
			continue
		}
		out = append(out, askEntryJSON{
			ID: m.ID, AgeSeconds: now() - m.Epoch, From: m.From,
			Question: strings.TrimPrefix(m.Text, "[ask] "),
		})
	}
	return out
}

// TailEvents returns the last n lines of the audit log (or an empty slice when
// there is no log yet) — for the web UI's live event feed.
func (b *Bus) TailEvents(n int) []string {
	data, err := os.ReadFile(b.Events)
	if err != nil {
		return []string{}
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines
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
