// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package comms

import "testing"

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
			action, reason := decideAction(tc.tag, tc.hasFallback, "retry-poll")
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
	action, reason := decideAction("zen-ratelimit", true, "retry-poll")
	if action != "switch-model" {
		t.Errorf("decideAction(\"zen-ratelimit\", true, \"retry-poll\") = %q, want \"switch-model\"; reason=%q",
			action, reason)
	}
	if reason == "" {
		t.Error("decideAction returned empty reason")
	}
}

func TestDecideAction_zenRatelimitNoFallback(t *testing.T) {
	action, reason := decideAction("zen-ratelimit", false, "retry-poll")
	if action != "retry" {
		t.Errorf("decideAction(\"zen-ratelimit\", false, \"retry-poll\") = %q, want \"retry\"; reason=%q",
			action, reason)
	}
	if reason == "" {
		t.Error("decideAction returned empty reason")
	}
}

func TestDecideAction_ignoreUnknown(t *testing.T) {
	action, reason := decideAction("", false, "session-error")
	if action != "ignore" {
		t.Errorf("decideAction(\"\", false, \"session-error\") = %q, want \"ignore\"; reason=%q",
			action, reason)
	}
	if reason == "" {
		t.Error("decideAction returned empty reason")
	}
}

func TestDecideAction_retryLogMonitorUnknown(t *testing.T) {
	action, reason := decideAction("", false, "log-monitor")
	if action != "retry" {
		t.Errorf("decideAction(\"\", false, \"log-monitor\") = %q, want \"retry\"; reason=%q",
			action, reason)
	}
	if reason == "" {
		t.Error("decideAction returned empty reason")
	}
}
