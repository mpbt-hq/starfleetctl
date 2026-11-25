// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package comms

import (
	"fmt"
	"strings"
)

const dispatchEventProtocolVersion = 1

// DispatchEvent is a transport-neutral event offered to the plugin.
// The event type describes the action; OpenCode-specific placement stays
// entirely in the plugin adapter.
type DispatchEvent struct {
	ID      string               `json:"id"`
	Type    string               `json:"type"`
	Source  DispatchEventSource  `json:"source"`
	Payload DispatchEventPayload `json:"payload"`
	Actions DispatchEventActions `json:"actions,omitempty"`
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

// DispatchEventPayload carries either a fully formatted prompt snippet or a
// command for the transitional command.execute event. Prompt is deliberately
// string-or-string[] JSON; the plugin only places these parts.
type DispatchEventPayload struct {
	Prompt  any    `json:"prompt,omitempty"`
	Command string `json:"command,omitempty"`
}

// DispatchEventActions describes generic delivery intent, not an OpenCode API
// operation. Future typed actions can be added without changing the poller.
type DispatchEventActions struct {
	StartTurn bool `json:"start_turn,omitempty"`
}

// formatCommsPrompt owns all Comms-specific composition: context header,
// sender prefix, body, grouping, and separators. The plugin must not recreate
// or modify this text.
func formatCommsPrompt(message Message) string {
	return strings.Join([]string{
		"--- fleet directives (from other ships via comms) ---",
		"Process each directive and carry out the requested action.",
		"",
		fmt.Sprintf("Directive %s from %s:", message.ID, message.From),
		message.Text,
		"",
		"--- end fleet directives ---",
		"",
	}, "\n")
}

func isModelSwitchMessage(message Message, messageType string) bool {
	if messageType != "ship" && messageType != "user" && messageType != "control" {
		return false
	}
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(message.Text)), "model ")
}

// eventsFromMessages converts source records into typed, stable-ID events.
// Prompt composition stays in Go; existing local commands are represented as
// command.execute until their individual typed actions are introduced.
func eventsFromMessages(messages []Message) []DispatchEvent {
	events := make([]DispatchEvent, 0, len(messages))
	for _, message := range messages {
		messageType := message.Type
		if messageType == "" {
			messageType = "ship"
		}

		event := DispatchEvent{
			ID:   message.ID,
			Type: "unsupported",
			Source: DispatchEventSource{
				Kind:        "comms.message",
				ID:          message.ID,
				From:        message.From,
				Target:      message.Target,
				ReplyTo:     message.ReplyTo,
				MessageType: messageType,
			},
		}

		switch {
		case messageType == "command" || isModelSwitchMessage(message, messageType):
			event.Type = "command.execute"
			event.Payload.Command = message.Text
		case messageType == "ship" || messageType == "user" ||
			messageType == "control" || messageType == "message":
			event.Type = "prompt.inject"
			event.Payload.Prompt = formatCommsPrompt(message)
			event.Actions.StartTurn = true
		}

		events = append(events, event)
	}
	return events
}

// dispatchEventsPoll offers the unread inbox as a versioned generic event
// stream. Polling is non-destructive: events remain available until the
// consumer explicitly acknowledges them through the existing seen_mark path.
func (b *Bus) dispatchEventsPoll(req dispatchRequest) dispatchResponse {
	inbox := b.dispatchInbox(req)
	if !inbox.OK {
		return inbox
	}

	events := eventsFromMessages(inbox.Messages)
	return dispatchResponse{
		OK:              true,
		ProtocolVersion: dispatchEventProtocolVersion,
		Events:          &events,
	}
}
