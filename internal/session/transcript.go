// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
//
// Implements `session transcript <id>` — extracts the opencode session
// transcript from the SQLite DB (via internal/ocsessions) and renders it
// either as a human-readable text dump on stdout or as the raw JSON payload
// (the same shape the web frontend consumes), writing to stdout or a file.
package session

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/metux/starfleetctl/internal/ocsessions"
)

const transcriptUsage = `session transcript <id> [options]

Extract an opencode session transcript from the SQLite DB and write it to
stdout or a file. <id> is the opencode session id (ses_…) or a unique title
substring; the newest matching session is used when several match.

Options:
  --limit <n>      max messages in the window (1..500, default 50)
  --offset <n>     skip the n newest messages (default 0)
  --output <file>  write to file instead of stdout ('-' = stdout)
  --json           emit the raw JSON payload (web-frontend shape)
  --text           emit a human-readable rendering (default)
`

// runTranscript implements `session transcript <id>`.
func runTranscript(root string, args []string) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		fmt.Print(transcriptUsage)
		if len(args) == 0 {
			return 2
		}
		return 0
	}

	id := args[0]
	limit, offset := 50, 0
	output := "-"
	asJSON := false
	for i := 1; i < len(args); i++ {
		switch {
		case args[i] == "--limit" || args[i] == "--offset" || args[i] == "--output":
			opt, val := args[i], ""
			if i+1 < len(args) {
				val = args[i+1]
				i++
			}
			switch opt {
			case "--limit":
				n, err := strconv.Atoi(val)
				if err != nil || n <= 0 {
					fmt.Fprintln(os.Stderr, "session transcript: --limit needs a positive number")
					return 2
				}
				limit = n
			case "--offset":
				n, err := strconv.Atoi(val)
				if err != nil || n < 0 {
					fmt.Fprintln(os.Stderr, "session transcript: --offset needs a non-negative number")
					return 2
				}
				offset = n
			case "--output":
				output = val
			}
		case args[i] == "--json":
			asJSON = true
		case args[i] == "--text":
			asJSON = false
		default:
			fmt.Fprintf(os.Stderr, "session transcript: unknown option '%s'\n", args[i])
			return 2
		}
	}

	id, ok := resolveTranscriptID(id)
	if !ok {
		fmt.Fprintf(os.Stderr, "session transcript: no session matches '%s'\n", id)
		return 1
	}

	tr, err := ocsessions.SessionTranscript(id, limit, offset)
	if err != nil {
		fmt.Fprintf(os.Stderr, "session transcript: %v\n", err)
		return 1
	}

	var out string
	if asJSON {
		payload, err := json.MarshalIndent(tr, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "session transcript: JSON encode: %v\n", err)
			return 1
		}
		out = string(payload) + "\n"
	} else {
		out = renderTranscript(*tr)
	}
	if output == "-" || output == "" {
		fmt.Print(out)
		return 0
	}
	if err := os.WriteFile(output, []byte(out), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "session transcript: write %s: %v\n", output, err)
		return 1
	}
	return 0
}

// resolveTranscriptID maps a user-provided id to a real opencode session id.
// It returns the exact id when it matches a session verbatim, otherwise it
// matches by title substring and picks the most recently updated hit.
func resolveTranscriptID(id string) (string, bool) {
	sessions, err := ocsessions.List(ocsessions.ListOpts{Limit: ocsessions.MaxListLimit()})
	if err != nil {
		return id, false
	}
	for _, s := range sessions {
		if s.ID == id {
			return s.ID, true
		}
	}
	var best string
	var bestUpdated int64 = -1
	for _, s := range sessions {
		if strings.Contains(s.Title, id) && s.Updated > bestUpdated {
			best, bestUpdated = s.ID, s.Updated
		}
	}
	if best != "" {
		return best, true
	}
	return id, false
}

// renderTranscript returns a human-readable text dump of the transcript.
func renderTranscript(tr ocsessions.Transcript) string {
	var b strings.Builder
	s := tr.Session
	fmt.Fprintf(&b, "=== Session %s ===\n", s.ID)
	fmt.Fprintf(&b, "title:   %s\n", orDash(s.Title))
	fmt.Fprintf(&b, "mode:    %s\n", orDash(s.Mode))
	fmt.Fprintf(&b, "model:   %s\n", orDash(s.Model))
	fmt.Fprintf(&b, "dir:     %s\n", orDash(s.Directory))
	fmt.Fprintf(&b, "created: %s\n", ts(s.Created))
	fmt.Fprintf(&b, "updated: %s\n", ts(s.Updated))
	fmt.Fprintf(&b, "cost:    %.4f\n", s.Cost)
	fmt.Fprintf(&b, "tokens:  in=%d out=%d reasoning=%d\n",
		s.TokensInput, s.TokensOutput, s.TokensReasoning)
	b.WriteString("\n")

	for i, m := range tr.Messages {
		role := m.Role
		if role == "" {
			role = "?"
		}
		if m.Finish != "" {
			role += fmt.Sprintf(" [%s]", m.Finish)
		}
		hdr := fmt.Sprintf("[%02d] %s (%s) %s", i+1, role, m.ID, ts(m.Time))
		if m.Tokens > 0 {
			hdr += fmt.Sprintf(" tokens=%d", m.Tokens)
		}
		fmt.Fprintln(&b, hdr)

		// Parts come back from the DB already ordered by time within a
		// message (SQL orders by message_id, time_created, id).
		for _, p := range m.Parts {
			switch p.Type {
			case "text":
				fmt.Fprintf(&b, "  text: %s\n", p.Text)
			case "reasoning":
				fmt.Fprintf(&b, "  reasoning: %s\n", p.Text)
			case "tool":
				fmt.Fprintf(&b, "  tool %s:\n", orDash(p.Tool))
				if p.Input != "" {
					fmt.Fprintf(&b, "    input: %s\n", p.Input)
				}
				if p.Output != "" {
					fmt.Fprintf(&b, "    output: %s\n", p.Output)
				}
			default:
				if p.Text != "" {
					fmt.Fprintf(&b, "  %s: %s\n", orDash(p.Type), p.Text)
				}
			}
			if p.Truncated {
				b.WriteString("    (truncated)\n")
			}
		}
		b.WriteString("\n")
	}
	return b.String()
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func ts(ms int64) string {
	if ms <= 0 {
		return "—"
	}
	return time.UnixMilli(ms).UTC().Format("2006-01-02 15:04:05 UTC")
}