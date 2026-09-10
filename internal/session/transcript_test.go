// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package session

import (
	"strings"
	"testing"

	"github.com/metux/starfleetctl/internal/ocsessions"
)

func TestRenderTranscript(t *testing.T) {
	tr := ocsessions.Transcript{
		Session: ocsessions.Session{
			ID:              "ses_abc",
			Title:           "Enterprise",
			Mode:            "build",
			Model:           "big-pickle",
			Directory:       "/ws",
			Created:         1000,
			Updated:         2000,
			Cost:            0.25,
			TokensInput:     100,
			TokensOutput:    20,
			TokensReasoning: 5,
		},
		Messages: []ocsessions.Msg{
			{ID: "msg_1", Role: "user", Time: 1000, Parts: []ocsessions.Part{
				{MessageID: "msg_1", Type: "text", Text: "hello"},
			}},
			{ID: "msg_2", Role: "assistant", Finish: "tool-calls", Time: 2000, Tokens: 25, Parts: []ocsessions.Part{
				{MessageID: "msg_2", Type: "reasoning", Text: "think"},
				{MessageID: "msg_2", Type: "tool", Tool: "bash", Input: "ls", Output: "file1"},
				{MessageID: "msg_2", Type: "text", Text: "done", Truncated: true},
			}},
		},
	}

	out := renderTranscript(tr)
	for _, want := range []string{
		"=== Session ses_abc ===",
		"title:   Enterprise",
		"mode:    build",
		"model:   big-pickle",
		"[01] user (msg_1)",
		"  text: hello",
		"[02] assistant [tool-calls] (msg_2)",
		"  reasoning: think",
		"  tool bash:",
		"    input: ls",
		"    output: file1",
		"(truncated)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("renderTranscript missing %q in:\n%s", want, out)
		}
	}
}

func TestTs(t *testing.T) {
	if got := ts(0); got != "—" {
		t.Errorf("ts(0) = %q, want —", got)
	}
	if got := ts(1725900000000); got != "2024-09-09 16:40:00 UTC" {
		t.Errorf("ts(1725900000000) = %q, want 2024-09-09 16:40:00 UTC", got)
	}
}

func TestOrDash(t *testing.T) {
	if got := orDash(""); got != "—" {
		t.Errorf("orDash(\"\") = %q, want —", got)
	}
	if got := orDash("x"); got != "x" {
		t.Errorf("orDash(\"x\") = %q, want x", got)
	}
}
