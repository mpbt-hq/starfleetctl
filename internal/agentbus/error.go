// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package agentbus

import (
	"fmt"
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
	return ""
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
	userAbortRe = regexp.MustCompile(`(^|\W)(abort|cancel|interrupt|signal|sigint|econnaborted|context (deadline|canceled))`)
)

// DoErrorRun dispatches `agent-bus error <subcommand>`.
func (b *Bus) DoErrorRun(args []string) error {
	if len(args) == 0 {
		return usageErr("agent-bus error: need <subcommand> (classify|is-abort)")
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
	default:
		return usageErr("agent-bus error: unknown subcommand: " + args[0])
	}
}
