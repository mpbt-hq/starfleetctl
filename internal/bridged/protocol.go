// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
//
// Package bridged is the first concrete step of the "Starbase" long-term
// architecture vision (DASHBOARD.md): a Unix-domain-socket daemon offering
// agent-bus and dashboard as a persistent, connection-based alternative to
// today's one-shot-process-per-call file/flock model. Deliberately additive
// — the file-based model is untouched and remains the sole thing any
// existing caller (hooks, Monitor-loop scripts, direct CLI/file use) relies
// on; nothing here is wired into production yet.
//
// See DASHBOARD.md's "Long-term fleet architecture vision" row for the full
// design rationale (wire protocol shape, lifecycle/crash-safety, the
// TCP-extensibility requirement, and why command *execution* is serialized
// behind a mutex even though connections are accepted concurrently).
package bridged

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
)

// maxFrameSize bounds a single frame's declared length, protecting against
// a malformed/hostile length prefix causing an oversized allocation. Well
// above any realistic payload (DASHBOARD.md itself is ~150KB today).
const maxFrameSize = 64 * 1024 * 1024

// Request is one bridged call: cmd selects the command family (only
// "agent-bus" and "dashboard" are dispatchable in this step; "ping" is
// handled specially as a liveness check, not forwarded to either), args are
// the same argv a CLI invocation of that family would receive.
//
// Env carries per-request identity overrides (AGENT_ID and friends — see
// allowedEnvOverrides) so one daemon instance can serve many different
// agents' identities instead of reporting everything under whatever
// environment the daemon process itself happened to start with. Omitting
// Env (nil/empty) is fully backward compatible: the request runs against
// the daemon's own ambient environment exactly like v1 did.
type Request struct {
	Cmd  string            `json:"cmd"`
	Args []string          `json:"args,omitempty"`
	Env  map[string]string `json:"env,omitempty"`
}

// Response mirrors what a CLI invocation would have produced: its exit
// code plus captured stdout/stderr. A request rejected by policy (e.g. a
// disallowed agent-bus subcommand) or malformed comes back the same shape
// (ExitCode 2, an explanatory Stderr) rather than a separate error channel
// — one schema for both "ran and failed" and "refused to run".
type Response struct {
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout,omitempty"`
	Stderr   string `json:"stderr,omitempty"`
}

// writeFrame writes a length-prefixed JSON frame: <4-byte big-endian
// uint32 length><payload>. This framing (not newline-delimited JSON) is
// the transport-agnostic choice — it works unchanged if a future TCP
// listener is added alongside the Unix-socket one, per the Starbase design
// requirement, and doesn't rely on JSON payloads never containing a raw
// newline byte.
func writeFrame(w io.Writer, v any) error {
	payload, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if len(payload) > maxFrameSize {
		return fmt.Errorf("bridged: outgoing frame too large: %d bytes", len(payload))
	}
	var lenBuf [4]byte
	binary.BigEndian.PutUint32(lenBuf[:], uint32(len(payload)))
	if _, err := w.Write(lenBuf[:]); err != nil {
		return err
	}
	_, err = w.Write(payload)
	return err
}

// readFrame reads one length-prefixed frame and decodes it as JSON into v.
func readFrame(r io.Reader, v any) error {
	var lenBuf [4]byte
	if _, err := io.ReadFull(r, lenBuf[:]); err != nil {
		return err
	}
	n := binary.BigEndian.Uint32(lenBuf[:])
	if n > maxFrameSize {
		return fmt.Errorf("bridged: incoming frame too large: %d bytes", n)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return err
	}
	return json.Unmarshal(buf, v)
}
