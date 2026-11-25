// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package comms

import "fmt"

const dispatchEventProtocolVersion = 1

// DispatchEvent is a transport-neutral event offered to the plugin.
// The event type describes the action; OpenCode-specific placement stays
// entirely in the plugin adapter.
type DispatchEvent struct {
	ID      string                `json:"id"`
	Type    string                `json:"type"`
	Source  DispatchEventSource   `json:"source"`
	Payload DispatchEventPayload  `json:"payload"`
	Actions DispatchEventActions  `json:"actions,omitempty"`
}

// DispatchEventSource identifies the originating Comms record without
// requiring the plugin to reconstruct sender or routing context.
type DispatchEventSource struct {
	Kind        string `json:"kind"`
	ID          string `json:"id"`
	From        string `json:"from,omitempty"`
	Target      string `json:"target,omitempty"`
	ReplyTo     string `json:"reply_to,omitempty"`
	MessageType string `json:"message_type,omitempty"`
}

// DispatchEventPayload carries the fully formatted prompt snippet. Prompt is
// deliberately string-or-string[] JSON; the plugin only places these parts.
type DispatchEventPayload struct {
	Prompt any `json:"prompt,omitempty"`
}

// DispatchEventActions describes generic delivery intent, not an OpenCode API
// operation. Future typed actions can be added without changing the poller.
type DispatchEventActions struct {
	StartTurn bool `json:"start_turn,omitempty"`
}

// formatCommsPrompt owns all Comms-specific composition: context header,
// sender prefix, body, grouping, and separators. The plugin must not recreate
// or modify this text.
func formatCommsPrompt(message Message) []string {
	return []string{
		"--- fleet directives (from other ships via comms) ---",
		"Process each directive and carry out the requested action.",
		"",
		fmt.Sprintf("Directive %s from %s:", message.ID, message.From),
		message.Text,
		"",
		"--- end fleet directives ---",
	}
}

// dispatchEventsPoll offers the unread inbox as a versioned generic event
// stream. Polling is non-destructive: events remain available until the
// consumer explicitly acknowledges them through the existing seen_mark path.
func (b *Bus) dispatchEventsPoll(req dispatchRequest) dispatchResponse {
	inbox := b.dispatchInbox(req)
	if !inbox.OK {
		return inbox
	}

	events := make([]DispatchEvent, 0, len(inbox.Messages))
	for _, message := range inbox.Messages {
		events = append(events, DispatchEvent{
			ID:   message.ID,
			Type: "prompt.inject",
			Source: DispatchEventSource{
				Kind:        "comms.message",
				ID:          message.ID,
				From:        message.From,
				Target:      message.Target,
				ReplyTo:     message.ReplyTo,
				MessageType: message.Type,
			},
			Payload: DispatchEventPayload{
				Prompt: formatCommsPrompt(message),
			},
			Actions: DispatchEventActions{
				StartTurn: true,
			},
		})
	}

	return dispatchResponse{
		OK:              true,
		ProtocolVersion: dispatchEventProtocolVersion,
		Events:           events,
	}
}
