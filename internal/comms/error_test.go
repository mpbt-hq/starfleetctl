// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package comms

import (
	"testing"
)

func TestClassifyModelError_zenRatelimit(t *testing.T) {
	cases := []struct {
		name   string
		detail string
	}{
		{"rate limit explicit", "rate limit exceeded"},
		{"429", "429 Too Many Requests"},
		{"quota", "quota exceeded"},
		{"usage limit", "usage limit reached"},
		{"usage cap", "usage cap hit"},
		{"too many requests", "Too Many Requests"},
		{"access denied", "access denied for free plan"},
		{"temporarily blocked", "temporarily blocked due to high usage"},
		{"try again later", "try again later"},
		{"toomanyrequests", "toomanyrequests error"},
		{"subscription", "upgrade your subscription"},
		{"free usage", "free usage limit exhausted"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tag := ClassifyModelError(tc.detail); tag != "zen-ratelimit" {
				t.Errorf("ClassifyModelError(%q) = %q, want %q", tc.detail, tag, "zen-ratelimit")
			}
		})
	}
}

func TestClassifyModelError_resourceExhausted(t *testing.T) {
	cases := []struct {
		name   string
		detail string
	}{
		{"resourceexhausted", "ResourceExhausted: worker capacity"},
		{"resource exhausted", "resource exhausted"},
		{"request limit reached (resource)", "request limit reached"},
		{"context length", "context length exceeded"},
		{"maximum context", "maximum context window reached"},
		{"context window", "context window full"},
		{"token limit", "token limit reached"},
		{"token quota", "token quota exhausted"},
		{"too many tokens", "too many tokens in input"},
		{"input too long", "input too long"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tag := ClassifyModelError(tc.detail); tag != "resource-exhausted" {
				t.Errorf("ClassifyModelError(%q) = %q, want %q", tc.detail, tag, "resource-exhausted")
			}
		})
	}
}

func TestClassifyModelError_nimOverload(t *testing.T) {
	cases := []struct {
		name   string
		detail string
	}{
		{"nim", "NIM overloaded"},
		{"500", "500 internal server error"},
		{"502", "502 bad gateway"},
		{"503", "503 service unavailable"},
		{"bad gateway", "Bad Gateway"},
		{"connection reset", "connection reset by peer"},
		{"econnreset", "ECONNRESET"},
		{"econnrefused", "ECONNREFUSED"},
		{"upstream", "upstream connect error"},
		{"overload", "server overload"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tag := ClassifyModelError(tc.detail); tag != "nim-overload" {
				t.Errorf("ClassifyModelError(%q) = %q, want %q", tc.detail, tag, "nim-overload")
			}
		})
	}
}

func TestClassifyModelError_streamingFailed(t *testing.T) {
	cases := []struct {
		name   string
		detail string
	}{
		{"streaming response failed", "Streaming response failed"},
		{"streaming request failed", "Streaming request failed"},
		{"stream interrupted", "stream interrupted"},
		{"response stream", "response stream closed"},
		{"connection closed", "connection closed"},
		{"broken pipe", "broken pipe"},
		{"unexpected eof", "unexpected EOF"},
		{"stream closed", "stream closed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tag := ClassifyModelError(tc.detail); tag != "streaming-response-failed" {
				t.Errorf("ClassifyModelError(%q) = %q, want %q", tc.detail, tag, "streaming-response-failed")
			}
		})
	}
}

func TestClassifyModelError_noProvider(t *testing.T) {
	cases := []struct {
		name   string
		detail string
	}{
		{"no provider available", "no provider available for model claude-3"},
		{"provider unavailable", "provider is currently unavailable"},
		{"no provider found", "no provider found for requested model"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tag := ClassifyModelError(tc.detail); tag != "no-provider" {
				t.Errorf("ClassifyModelError(%q) = %q, want %q", tc.detail, tag, "no-provider")
			}
		})
	}
}

func TestClassifyModelError_unrecognized(t *testing.T) {
	cases := []string{
		"",
		"something completely different",
		"user cancelled",
		"signal: interrupt",
	}
	for _, detail := range cases {
		t.Run(detail, func(t *testing.T) {
			if tag := ClassifyModelError(detail); tag != "" {
				t.Errorf("ClassifyModelError(%q) = %q, want \"\"", detail, tag)
			}
		})
	}
}

func TestClassifyModelError_resourceExhaustedPriority(t *testing.T) {
	// "request limit reached" matches both ratelimitRe and resourceExhaustedRe,
	// but resourceExhaustedRe is checked first → must be resource-exhausted.
	detail := "request limit reached"
	if tag := ClassifyModelError(detail); tag != "resource-exhausted" {
		t.Errorf("ClassifyModelError(%q) = %q, want \"resource-exhausted\"", detail, tag)
	}
}

func TestIsUserAbort(t *testing.T) {
	cases := []struct {
		detail string
		abort  bool
	}{
		{"", true},
		{"unknown error", true},
		{"abort", true},
		{"cancel", true},
		{"interrupt", true},
		{"signal: interrupt", true},
		{"SIGINT", true},
		{"context canceled", true},
		{"context deadline exceeded", true},
		{"ECONNABORTED", true},
		{"ResourceExhausted: token quota", false},
		{"Streaming response failed", false},
		{"something else", false},
	}
	for _, tc := range cases {
		t.Run(tc.detail, func(t *testing.T) {
			if got := IsUserAbort(tc.detail); got != tc.abort {
				t.Errorf("IsUserAbort(%q) = %v, want %v", tc.detail, got, tc.abort)
			}
		})
	}
}

func TestIsAutoRestartTag(t *testing.T) {
	cases := []struct {
		tag  string
		want bool
	}{
		{"streaming-response-failed", true},
		{"nim-overload", true},
		{"resource-exhausted", true},
		{"no-provider", true},
		{"zen-ratelimit", false},
		{"something-else", false},
		{"", false},
	}
	for _, tc := range cases {
		t.Run(tc.tag, func(t *testing.T) {
			if got := isAutoRestartTag(tc.tag); got != tc.want {
				t.Errorf("isAutoRestartTag(%q) = %v, want %v", tc.tag, got, tc.want)
			}
		})
	}
}
