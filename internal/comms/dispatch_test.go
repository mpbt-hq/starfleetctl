// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package comms

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestDecideAction_retryTransient(t *testing.T) {
	cases := []struct {
		tag         string
		hasFallback bool
	}{
		{"nim-overload", false},
		{"nim-overload", true},
		{"streaming-response-failed", false},
		{"streaming-response-failed", true},
		{"resource-exhausted", false},
		{"resource-exhausted", true},
		{"no-provider", false},
		{"no-provider", true},
	}
	for _, tc := range cases {
		name := tc.tag
		if tc.hasFallback {
			name += "+fallback"
		}
		t.Run(name, func(t *testing.T) {
			action, reason := decideAction(tc.tag, tc.hasFallback, "retry-poll", "")
			if action != "retry" {
				t.Errorf("decideAction(%q, %v, \"retry-poll\") = %q, want \"retry\"; reason=%q",
					tc.tag, tc.hasFallback, action, reason)
			}
			if reason == "" {
				t.Error("decideAction returned empty reason")
			}
		})
	}
}

func TestDecideAction_switchModel(t *testing.T) {
	// Create temp dir with fallback model configured
	tmpDir := t.TempDir()
	confDir := filepath.Join(tmpDir, ".starfleet-ai", "conf")
	if err := os.MkdirAll(confDir, 0755); err != nil {
		t.Fatal(err)
	}
	commsYaml := `comms:
  fallback_model: "opencode/test-fallback"
`
	if err := os.WriteFile(filepath.Join(confDir, "comms.yaml"), []byte(commsYaml), 0644); err != nil {
		t.Fatal(err)
	}

	action, reason := decideAction("zen-ratelimit", true, "retry-poll", tmpDir)
	if action != "switch-model" {
		t.Errorf("decideAction(\"zen-ratelimit\", true, \"retry-poll\", tmpDir) = %q, want \"switch-model\"; reason=%q",
			action, reason)
	}
	if reason == "" {
		t.Error("decideAction returned empty reason")
	}
}

func TestDecideAction_zenRatelimitNoFallback(t *testing.T) {
	action, reason := decideAction("zen-ratelimit", false, "retry-poll", "")
	if action != "retry" {
		t.Errorf("decideAction(\"zen-ratelimit\", false, \"retry-poll\") = %q, want \"retry\"; reason=%q",
			action, reason)
	}
	if reason == "" {
		t.Error("decideAction returned empty reason")
	}
}

func TestDecideAction_ignoreUnknown(t *testing.T) {
	action, reason := decideAction("", false, "session-error", "")
	if action != "ignore" {
		t.Errorf("decideAction(\"\", false, \"session-error\") = %q, want \"ignore\"; reason=%q",
			action, reason)
	}
	if reason == "" {
		t.Error("decideAction returned empty reason")
	}
}

func TestDecideAction_retryLogMonitorUnknown(t *testing.T) {
	action, reason := decideAction("", false, "log-monitor", "")
	if action != "retry" {
		t.Errorf("decideAction(\"\", false, \"log-monitor\") = %q, want \"retry\"; reason=%q",
			action, reason)
	}
	if reason == "" {
		t.Error("decideAction returned empty reason")
	}
}

func TestDispatchEventsPollReturnsFormattedPromptEvents(t *testing.T) {
	b := newTestBus(t, "Enterprise")

	firstID, err := b.post(b.ShipID, "first directive", "", "", "", "ship")
	if err != nil {
		t.Fatal(err)
	}
	secondID, err := b.post(b.ShipID, "second directive", "", "", "", "user")
	if err != nil {
		t.Fatal(err)
	}

	inbox := b.dispatchInbox(dispatchRequest{})
	resp := b.dispatch(dispatchRequest{Cmd: "events.poll"})
	if !resp.OK {
		t.Fatalf("events.poll failed: %s", resp.Error)
	}
	if resp.ProtocolVersion != dispatchEventProtocolVersion {
		t.Fatalf("protocol version = %d, want %d", resp.ProtocolVersion, dispatchEventProtocolVersion)
	}
	if len(resp.Events) != len(inbox.Messages) || len(resp.Events) != 2 {
		t.Fatalf("events = %d, inbox messages = %d, want 2", len(resp.Events), len(inbox.Messages))
	}
	if resp.Messages != nil {
		t.Fatalf("events.poll must not return legacy messages: %#v", resp.Messages)
	}

	wire, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	var envelope map[string]any
	if err := json.Unmarshal(wire, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope["protocol_version"] != float64(dispatchEventProtocolVersion) {
		t.Fatalf("JSON protocol_version = %#v, want %d", envelope["protocol_version"], dispatchEventProtocolVersion)
	}
	wireEvents, ok := envelope["events"].([]any)
	if !ok || len(wireEvents) != 2 {
		t.Fatalf("JSON events = %#v, want 2-element array", envelope["events"])
	}
	wireEvent, ok := wireEvents[0].(map[string]any)
	if !ok || wireEvent["type"] != "prompt.inject" {
		t.Fatalf("JSON event[0] = %#v, want prompt.inject object", wireEvents[0])
	}
	wirePayload, ok := wireEvent["payload"].(map[string]any)
	if !ok {
		t.Fatalf("JSON event[0].payload = %#v, want object", wireEvent["payload"])
	}
	if _, ok := wirePayload["prompt"].([]any); !ok {
		t.Fatalf("JSON event[0].payload.prompt = %#v, want string array", wirePayload["prompt"])
	}
	wireActions, ok := wireEvent["actions"].(map[string]any)
	if !ok || wireActions["start_turn"] != true {
		t.Fatalf("JSON event[0].actions = %#v, want start_turn=true", wireEvent["actions"])
	}

	for i, message := range inbox.Messages {
		event := resp.Events[i]
		if event.ID != message.ID {
			t.Errorf("event[%d].ID = %q, want %q (preserve inbox order)", i, event.ID, message.ID)
		}
		if event.Type != "prompt.inject" {
			t.Errorf("event[%d].Type = %q, want prompt.inject", i, event.Type)
		}
		if event.Source.Kind != "comms.message" || event.Source.ID != message.ID {
			t.Errorf("event[%d].Source = %#v, want Comms source for %s", i, event.Source, message.ID)
		}
		if event.Source.From != message.From || event.Source.Target != message.Target || event.Source.MessageType != message.Type {
			t.Errorf("event[%d].Source metadata = %#v, want from/target/type from message %#v", i, event.Source, message)
		}
		prompt, ok := event.Payload.Prompt.([]string)
		if !ok {
			t.Fatalf("event[%d].payload.prompt type = %T, want []string", i, event.Payload.Prompt)
		}
		if want := formatCommsPrompt(message); !reflect.DeepEqual(prompt, want) {
			t.Errorf("event[%d].prompt = %#v, want Go-formatted prompt %#v", i, prompt, want)
		}
		if !event.Actions.StartTurn {
			t.Errorf("event[%d].actions.start_turn = false, want true", i)
		}
	}

	// Polling is non-destructive: a second poll returns the same unread events.
	again := b.dispatch(dispatchRequest{Cmd: "events.poll"})
	if len(again.Events) != 2 || again.Events[0].ID != resp.Events[0].ID || again.Events[1].ID != resp.Events[1].ID {
		t.Fatalf("second poll changed unread events: first=%#v second=%#v", resp.Events, again.Events)
	}

	// Acknowledging one event removes only that source message from later polls.
	if err := b.DoAck(secondID, "test submitted"); err != nil {
		t.Fatal(err)
	}
	afterAck := b.dispatch(dispatchRequest{Cmd: "events.poll"})
	if len(afterAck.Events) != 1 || afterAck.Events[0].ID != firstID {
		t.Fatalf("after ack events = %#v, want only %s", afterAck.Events, firstID)
	}
}

func TestDispatchEventsPollEmpty(t *testing.T) {
	b := newTestBus(t, "Enterprise")
	resp := b.dispatch(dispatchRequest{Cmd: "events.poll"})
	if !resp.OK {
		t.Fatalf("events.poll failed: %s", resp.Error)
	}
	if resp.ProtocolVersion != dispatchEventProtocolVersion {
		t.Fatalf("protocol version = %d, want %d", resp.ProtocolVersion, dispatchEventProtocolVersion)
	}
	if len(resp.Events) != 0 {
		t.Fatalf("events = %#v, want empty", resp.Events)
	}
}
