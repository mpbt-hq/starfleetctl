// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
//
// Wormhole message handling — incoming/outgoing message routing via comms bus.
package wormhole

import (
	"fmt"
	"os"
	"path/filepath"
)

// MessageQueue is an in-memory queue of pending wormhole messages.
type MessageQueue struct {
	messages map[string]*Message
}

// NewMessageQueue creates a new empty message queue.
func NewMessageQueue() *MessageQueue {
	return &MessageQueue{
		messages: make(map[string]*Message),
	}
}

// Enqueue adds a message to the queue.
func (q *MessageQueue) Enqueue(msg *Message) {
	q.messages[msg.ID] = msg
}

// Dequeue removes and returns the next pending message.
func (q *MessageQueue) Dequeue() *Message {
	for _, msg := range q.messages {
		if msg.State == "pending" {
			msg.State = "delivered"
			return msg
		}
	}
	return nil
}

// ListPending returns all pending messages.
func (q *MessageQueue) ListPending() []*Message {
	var pending []*Message
	for _, msg := range q.messages {
		if msg.State == "pending" {
			pending = append(pending, msg)
		}
	}
	return pending
}

// RouteMessage routes a message to the appropriate ship's comms inbox.
func RouteMessage(root string, msg *Message) error {
	targetDir := filepath.Join(root, ".starfleet-ai", "var", "comms", "msgs", msg.To, "unseen")
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return fmt.Errorf("wormhole message dir: %w", err)
	}

	msgFile := filepath.Join(targetDir, msg.ID+".json")
	msgData := fmt.Sprintf(`{"id":"%s","from":"%s","to":"%s","content":"%s","timestamp":"%s","state":"%s"}`,
		msg.ID, msg.From, msg.To, msg.Content, msg.Timestamp.Format(timeRFC3339), msg.State)

	if err := os.WriteFile(msgFile, []byte(msgData), 0o644); err != nil {
		return fmt.Errorf("wormhole message write: %w", err)
	}

	msg.State = "delivered"
	return nil
}

const timeRFC3339 = "2006-01-02T15:04:05Z07:00"