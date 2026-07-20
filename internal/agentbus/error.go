// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package agentbus

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
)

// DoErrorClassify implements `agent-bus error classify <detail>` —
// classifies a model-API error detail string into a short tag.
//
// Tags:
//   zen-ratelimit      — 429 / quota / usage limit / rate limit
//   resource-exhausted — worker capacity / token quota / context length
//   nim-overload       — NVIDIA inference microservice overload (5xx / conn reset)
func (b *Bus) DoErrorClassify(detail string) error {
	tag := ClassifyModelError(detail)
	if tag != "" {
		fmt.Println(tag)
	} else {
		fmt.Println("(none)")
	}
	return nil
}

// DoErrorIsAbort implements `agent-bus error is-abort <detail>` —
// prints "true" if the detail is a user-initiated abort, "false" otherwise.
func (b *Bus) DoErrorIsAbort(detail string) error {
	if IsUserAbort(detail) {
		fmt.Println("true")
	} else {
		fmt.Println("false")
	}
	return nil
}

// ClassifyModelError detects model-API failure modes. Returns a short tag
// the health record and flagship notification carry, or "" if the error
// does not look model-API related.
func ClassifyModelError(detail string) string {
	d := strings.ToLower(detail)
	// ZEN rate-limit / usage cap / request limit
	if ratelimitRe.MatchString(d) {
		return "zen-ratelimit"
	}
	// ResourceExhausted: worker capacity / token quota / context length
	if resourceExhaustedRe.MatchString(d) {
		return "resource-exhausted"
	}
	// NIM overload: server-side 5xx or connection-level failures
	if nimOverloadRe.MatchString(d) {
		return "nim-overload"
	}
	// Streaming response failed: mid-stream disconnect from the model API.
	// Transient — the ship can simply resume the previous prompt.
	if streamingFailedRe.MatchString(d) {
		return "streaming-response-failed"
	}
	return ""
}

// isAutoRestartTag reports whether a model-error tag is transient and the
// affected ship should be told to simply re-run its last prompt (resume),
// rather than only notifying the flagship.
func isAutoRestartTag(tag string) bool {
	return tag == "streaming-response-failed" || tag == "nim-overload"
}

// IsUserAbort reports whether a session.error detail is a user-initiated
// abort (Ctrl-C / SIGINT / context cancelled) rather than a genuine fault.
// opencode surfaces those with an empty or generic detail — there is no
// structured code we can trust, so we match the textual fingerprint.
func IsUserAbort(detail string) bool {
	d := strings.ToLower(detail)
	if d == "" || d == "unknown error" {
		return true
	}
	return userAbortRe.MatchString(d)
}

var (
	ratelimitRe = regexp.MustCompile(`(429|rate[ -_]?limit|too many requests|usage limit|usage cap|quota|exceeded|access denied|temporarily blocked|try again later|toomanyrequests|request limit reached|request limit)`)
	resourceExhaustedRe = regexp.MustCompile(`(resourceexhausted|resource exhausted|request limit reached|context length|maximum context|context window|token.{0,12}(limit|quota)|too many tokens|input.{0,12}too long)`)
	nimOverloadRe = regexp.MustCompile(`(nim|5\d\d|overload|bad gateway|connection reset|econnreset|econnrefused|upstream)`)
	streamingFailedRe = regexp.MustCompile(`(streaming (response|request) failed|stream interrupted|response stream|connection closed|broken pipe|unexpected eof|stream closed)`)
	userAbortRe = regexp.MustCompile(`(^|\W)(abort|cancel|interrupt|signal|sigint|econnaborted|context (deadline|canceled))`)
)

// errorHandlePayload is the JSON the opencode plugin pipes via stdin.
type errorHandlePayload struct {
	Detail string `json:"detail"`
	Ship   string `json:"ship,omitempty"`
	PID    int    `json:"pid,omitempty"`
}

// DoErrorHandle implements `agent-bus error handle --stdin` — the single
// entry point for session.error handling. Reads a JSON payload from stdin,
// then performs the full pipeline: abort check → classify → health update →
// log → tell flagship. Designed so the plugin can delegate all error
// handling to Go with one subprocess call.
func (b *Bus) DoErrorHandle(args []string) error {
	useStdin := false
	for _, a := range args {
		if a == "--stdin" {
			useStdin = true
		}
	}
	if !useStdin {
		return usageErr("agent-bus error handle: requires --stdin")
	}

	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		return fmt.Errorf("agent-bus error handle: reading stdin: %w", err)
	}

	var payload errorHandlePayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return fmt.Errorf("agent-bus error handle: invalid JSON: %w", err)
	}

	detail := payload.Detail
	if detail == "" {
		detail = "unknown error"
	}

	shipID := b.ShipID
	if payload.Ship != "" {
		shipID = payload.Ship
	}

	// User-initiated aborts are expected, not actionable fleet events.
	// Suppress: log locally only, do not notify the flagship.
	if IsUserAbort(detail) {
		b.logEvent("plugin", fmt.Sprintf("error (user abort, suppressed): %s", detail))
		return nil
	}

	tag := ClassifyModelError(detail)

	// Update health record so the fleet console can distinguish a
	// throttled ship from a hard crash.
	if err := b.DoHealthUpdate([]string{
		"--state", "blocked",
		"--error-tag", tag,
		"--pid", fmt.Sprintf("%d", coalesceInt(payload.PID, os.Getpid())),
	}); err != nil {
		// best-effort: health write failure must not block error handling
	}

	label := ""
	if tag != "" {
		label = " [" + tag + "]"
	}
	b.logEvent("plugin", fmt.Sprintf("error%s: %s", label, detail))

	// Transient model errors: tell the affected ship to simply resume its
	// last prompt (re-run with an empty/synthetic prompt) instead of only
	// notifying the flagship. The plugin picks this directive up on its
	// next poll and re-issues session.promptAsync — no human in the loop.
	if isAutoRestartTag(tag) {
		restartMsg := fmt.Sprintf(
			"⚡ Model-Fehler [%s] erkannt. Setze fort: starte den vorigen Prompt erneut (leeres Prompt reicht, um einfach weiterzumachen). Fehlerdetail: %s",
			tag, detail,
		)
		if _, err := b.Tell(shipID, restartMsg, ""); err != nil {
			b.logEvent("plugin", fmt.Sprintf("error: failed to queue restart directive to %s: %v", shipID, err))
		} else {
			b.logEvent("plugin", fmt.Sprintf("error: queued auto-restart directive → %s [%s]", shipID, tag))
		}
	}

	// Tell the CONTROL agent (flagship) only, never broadcast — a broadcast
	// would land in the errored ship's own inbox and restart the self-loop.
	_ = b.DoPost("Enterprise", []string{
		fmt.Sprintf("⚠️ %s session.error%s: %s", shipID, label, detail),
	}, false, "", "")

	return nil
}

// DoErrorRun dispatches `agent-bus error <subcommand>`.
func (b *Bus) DoErrorRun(args []string) error {
	if len(args) == 0 {
		return usageErr("agent-bus error: need <subcommand> (classify|is-abort|handle)")
	}
	switch args[0] {
	case "classify":
		if len(args) < 2 {
			return usageErr("agent-bus error classify needs <detail>")
		}
		return b.DoErrorClassify(args[1])
	case "is-abort":
		if len(args) < 2 {
			return usageErr("agent-bus error is-abort needs <detail>")
		}
		return b.DoErrorIsAbort(args[1])
	case "handle":
		return b.DoErrorHandle(args[1:])
	default:
		return usageErr("agent-bus error: unknown subcommand: " + args[0])
	}
}
