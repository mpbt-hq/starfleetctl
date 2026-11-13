// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
//
// Wormhole message envelope handling.
//
// Per W0-130/W0-134: Wormhole is a Comms transport backend, not a parallel
// messaging system. Received messages are imported only through Comms Core's
// external-ingestion API (Bus.PostExternal); this package never writes local
// Comms records itself.
//
// A message is an immutable UTF-8 file with a small, strictly-validated YAML
// front matter block followed by a free-form body. It is published verbatim
// into mail/<target-ship>/new/<message-id> and must never be modified after
// publication: retries reuse the same identity and the exact same bytes.
package wormhole

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const timeRFC3339 = "2006-01-02T15:04:05Z07:00"

// Message is a single Wormhole message.
type Message struct {
	ID   string `yaml:"id"`
	From string `yaml:"from"`
	// Sender is the logical reply/routing address of the sender when it differs
	// from the canonical envelope sender (e.g. "ChatGPT@pegasus" for an
	// external counterparty whose envelope sender is "ChatGPT"). It is preserved
	// through transport so replies can be routed to the originating domain.
	Sender    string    `yaml:"sender,omitempty"`
	To        string    `yaml:"to"`
	Subject   string    `yaml:"subject,omitempty"`
	Content   string    `yaml:"content,omitempty"`
	Created   time.Time `yaml:"created,omitempty"`
	Timestamp time.Time `yaml:"-"`
	State     string    `yaml:"-"`
	// Comms metadata preserved through transport.
	Type    string `yaml:"type,omitempty"`
	ReplyTo string `yaml:"reply_to,omitempty"`
}

// tokenRe matches safe envelope identifiers / path components. It deliberately
// forbids leading dots, slashes and whitespace so a value can never escape the
// Maildir tree or form a dot-segment.
var tokenRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// validToken reports whether s is a safe message/ship identifier.
func validToken(s string) bool {
	if s == "" || s == "." || s == ".." {
		return false
	}
	return tokenRe.MatchString(s)
}

// domainRe matches a safe logical routing domain label/host component.
var domainRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// validLogicalAddress reports whether s is a safe logical sender address of the
// form user@domain. It is used to preserve the external reply/routing address
// without allowing it as the canonical envelope sender token.
func validLogicalAddress(s string) bool {
	user, domain, ok := strings.Cut(s, "@")
	if !ok || user == "" || domain == "" {
		return false
	}
	// Exactly one domain separator: user@domain only.
	if strings.Contains(domain, "@") {
		return false
	}
	return validToken(user) && domainRe.MatchString(domain)
}

// canonicalSender normalizes an external sender address: "user@domain" is split
// into the canonical envelope sender "user" and the logical reply/routing
// address "user@domain". A plain token is returned unchanged as the canonical
// sender with no logical address.
func canonicalSender(from string) (canonical, logical string, err error) {
	if validToken(from) {
		return from, "", nil
	}
	if validLogicalAddress(from) {
		user, _, _ := strings.Cut(from, "@")
		return user, from, nil
	}
	return "", "", fmt.Errorf("invalid or missing sender %q", from)
}

// newMessageID returns a globally unique, path-safe message identifier.
// Uniqueness comes from 64 bits of CSPRNG entropy, never from a clock or a
// process-local counter (W0-024: second-based IDs collide).
func newMessageID() string {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		// crypto/rand failing is catastrophic for the platform; fall back to
		// nanosecond resolution plus a fixed marker rather than panicking.
		return fmt.Sprintf("w0-%s-fallback", time.Now().UTC().Format("20060102T150405.000000000Z"))
	}
	return fmt.Sprintf("w0-%s-%s", time.Now().UTC().Format("20060102T150405Z"), hex.EncodeToString(buf[:]))
}

// NewMessage creates a new wormhole message with a stable random identity.
// The typ argument is retained for API compatibility and is not persisted.
// replyTo is optional and preserved through transport.
func NewMessage(from, to, typ, content, replyTo string) *Message {
	now := time.Now().UTC()
	return &Message{
		ID:        newMessageID(),
		From:      from,
		To:        to,
		Content:   content,
		Created:   now,
		Timestamp: now,
		State:     "pending",
		Type:      typ,
		ReplyTo:   replyTo,
	}
}

// validateMessage checks that all envelope fields required for a durable
// import are present and path-safe. An empty body/content is permanently
// invalid and must be rejected at ingress so it cannot be imported or retried.
func validateMessage(m *Message) error {
	if m == nil {
		return fmt.Errorf("nil message")
	}
	if !validToken(m.ID) {
		return fmt.Errorf("invalid or missing message id %q", m.ID)
	}
	if !validToken(m.From) {
		return fmt.Errorf("invalid or missing sender %q", m.From)
	}
	if m.Sender != "" && !validLogicalAddress(m.Sender) {
		return fmt.Errorf("invalid or missing sender %q", m.Sender)
	}
	if !validToken(m.To) {
		return fmt.Errorf("invalid or missing recipient %q", m.To)
	}
	if strings.TrimSpace(m.Content) == "" {
		return fmt.Errorf("empty message content")
	}
	return nil
}

// buildMessageBytes serializes a message into its canonical immutable form.
// The exact returned bytes are what gets committed and what is later handed to
// PostExternal; callers must not re-serialize on retry.
func buildMessageBytes(m *Message) []byte {
	if m.Created.IsZero() {
		m.Created = time.Now().UTC()
	}
	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b, "id: %s\n", m.ID)
	fmt.Fprintf(&b, "from: %s\n", m.From)
	if m.Sender != "" {
		fmt.Fprintf(&b, "sender: %s\n", yamlScalar(m.Sender))
	}
	fmt.Fprintf(&b, "to: %s\n", m.To)
	if m.Subject != "" {
		fmt.Fprintf(&b, "subject: %s\n", yamlScalar(m.Subject))
	}
	if m.Type != "" {
		fmt.Fprintf(&b, "type: %s\n", yamlScalar(m.Type))
	}
	if m.ReplyTo != "" {
		fmt.Fprintf(&b, "reply_to: %s\n", yamlScalar(m.ReplyTo))
	}
	fmt.Fprintf(&b, "created: %s\n", m.Created.UTC().Format(time.RFC3339))
	b.WriteString("---\n")
	b.WriteString(m.Content)
	if !strings.HasSuffix(m.Content, "\n") {
		b.WriteString("\n")
	}
	return []byte(b.String())
}

// yamlScalar quotes a scalar when necessary so arbitrary subjects round-trip.
func yamlScalar(s string) string {
	if s == "" {
		return `""`
	}
	needs := false
	for _, r := range s {
		if r == ':' || r == '#' || r == '\n' || r == '\r' || r == '\t' || r == '"' || r == '\'' || r == '[' || r == ']' || r == '{' || r == '}' || r == ',' || r == '&' || r == '*' || r == '!' || r == '|' || r == '>' || r == '@' || r == '`' {
			needs = true
			break
		}
	}
	if strings.HasPrefix(s, " ") || strings.HasSuffix(s, " ") || strings.EqualFold(s, "null") || strings.EqualFold(s, "true") || strings.EqualFold(s, "false") {
		needs = true
	}
	if !needs {
		return s
	}
	return fmt.Sprintf("%q", s)
}

// splitEnvelope separates the YAML front matter block from the body.
//
// Canonical form: the front matter starts and ends with a line consisting
// solely of "---" ("---\n<yaml>\n---\n<body>"). Backward-compatible, bounded
// tolerance: accept a missing opening "---" delimiter, or a missing closing
// "---" delimiter, only when the header/body boundary is unambiguous (a blank
// line after a contiguous leading block of YAML-like "key: ..." header lines).
// No guessing: if the boundary cannot be determined unambiguously, the message
// is rejected. The exact bytes are never rewritten on parse; normalization is
// internal (parseMessage/buildMessageBytes).
func splitEnvelope(data []byte) (front []byte, body []byte, err error) {
	text := string(data)

	// Canonical (and tolerated-missing-closing): leading front matter delimiter.
	if strings.HasPrefix(text, "---\n") {
		rest := text[len("---\n"):]
		if idx := strings.Index(rest, "\n---\n"); idx >= 0 {
			return []byte(rest[:idx]), []byte(rest[idx+len("\n---\n"):]), nil
		}
		// Canonical message with an empty body ends with "\n---" on the last line.
		if strings.HasSuffix(rest, "\n---") {
			return []byte(rest[:len(rest)-len("\n---")]), nil, nil
		}
		// Missing closing delimiter: accept only with an unambiguous blank-line
		// boundary after a contiguous header block.
		if front, body, ok := splitUnbracketedHeader(rest); ok {
			return front, body, nil
		}
		return nil, nil, fmt.Errorf("message front matter is not terminated by '---'")
	}

	// Tolerated: missing opening delimiter, only with an unambiguous header/body
	// boundary (blank line after contiguous header fields at file start).
	if front, body, ok := splitUnbracketedHeader(text); ok {
		return front, body, nil
	}
	return nil, nil, fmt.Errorf("message must start with '---' front matter delimiter")
}

// splitUnbracketedHeader splits text into a front matter header and body when,
// and only when, the boundary is unambiguous: a contiguous block of YAML-like
// header lines at the very start of text, terminated by a blank line ("\n\n")
// that separates header from body. A "header line" is a key/value line of the
// form "key: ..." (or "key:") or a YAML continuation/sequence line belonging to
// that block (indented, or starting with '-'). This keeps the body from ever
// being interpreted as header: the first blank line after the initial header
// block is the boundary, and anything after it (including lines that look like
// "key: value") is body. Returns ok=false when no such unambiguous split exists.
func splitUnbracketedHeader(text string) (front []byte, body []byte, ok bool) {
	// Require a blank-line separator; without it the header/body boundary is
	// ambiguous (or the message has no body/content separation we may guess at).
	idx := strings.Index(text, "\n\n")
	if idx < 0 {
		return nil, nil, false
	}
	header := text[:idx]
	rest := text[idx+2:] // body follows the separating blank line
	if header == "" {
		return nil, nil, false
	}
	lines := strings.Split(header, "\n")
	// The header block must start with a key/value field at column 0.
	if !isHeaderKeyLine(lines[0]) {
		return nil, nil, false
	}
	for _, line := range lines[1:] {
		if line == "" {
			// No embedded blank lines inside the header block; boundary is the
			// first "\n\n" we split on, but be defensive.
			return nil, nil, false
		}
		if isHeaderKeyLine(line) {
			continue
		}
		// Continuation of a preceding mapping/sequence value (indented) or a
		// sequence item — still part of the front matter block.
		if line[0] == ' ' || line[0] == '\t' || line[0] == '-' {
			continue
		}
		// A non-indented, non-key line inside the would-be header means the
		// block is not a clean header; reject rather than guess.
		return nil, nil, false
	}
	return []byte(header), []byte(rest), true
}

// isHeaderKeyLine reports whether line is a YAML mapping key/value line at
// column 0 (e.g. "id: ...", "from:", "reply_to: \"\""). Keys are restricted to
// identifier-like characters so arbitrary body text is never mistaken for a
// header field.
func isHeaderKeyLine(line string) bool {
	if line == "" {
		return false
	}
	if line[0] == ' ' || line[0] == '\t' || line[0] == '-' || line[0] == '#' {
		return false
	}
	i := 0
	for i < len(line) {
		c := line[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '-' {
			i++
			continue
		}
		break
	}
	if i == 0 {
		return false
	}
	// Must be followed by ':' (key) optionally followed by space/value/end.
	return i < len(line) && line[i] == ':'
}

// parseMessage parses an immutable message file strictly. Unknown front-matter
// keys are rejected so a malformed file can never be silently misinterpreted.
func parseMessage(data []byte) (*Message, error) {
	front, body, err := splitEnvelope(data)
	if err != nil {
		return nil, err
	}
	if len(front) > 64*1024 {
		return nil, fmt.Errorf("message front matter exceeds 64 KiB")
	}

	m := &Message{}
	dec := yaml.NewDecoder(strings.NewReader(string(front)))
	dec.KnownFields(true)
	if err := dec.Decode(m); err != nil {
		return nil, fmt.Errorf("invalid message front matter: %w", err)
	}
	if m.Content == "" {
		m.Content = strings.TrimRight(string(body), "\n")
	}
	// Normalize an external logical sender address. "ChatGPT@pegasus" is a
	// valid logical reply address but not a valid canonical envelope sender
	// token, so split it into the canonical sender ("ChatGPT") while preserving
	// the full logical address for reply routing. A plain sender token is left
	// untouched.
	origFrom := m.From
	canonical, logical, serr := canonicalSender(origFrom)
	if serr != nil {
		return nil, serr
	}
	if logical != "" {
		// origFrom was a logical address (user@domain): if sender is also set it
		// must fully match that logical address.
		if m.Sender != "" && m.Sender != logical {
			return nil, fmt.Errorf("sender %q does not match from %q", m.Sender, origFrom)
		}
		m.From = canonical
		m.Sender = logical
	} else {
		// origFrom was a canonical sender token: if sender is set it must be a
		// logical address whose user part matches the canonical sender.
		if m.Sender != "" {
			if !validLogicalAddress(m.Sender) {
				return nil, fmt.Errorf("invalid or missing sender %q", m.Sender)
			}
			user, _, _ := strings.Cut(m.Sender, "@")
			if user != canonical {
				return nil, fmt.Errorf("sender %q does not match from %q", m.Sender, canonical)
			}
		}
		m.From = canonical
	}
	if err := validateMessage(m); err != nil {
		return nil, err
	}
	if m.Timestamp.IsZero() {
		if !m.Created.IsZero() {
			m.Timestamp = m.Created
		} else {
			m.Timestamp = time.Now().UTC()
		}
	}
	return m, nil
}
