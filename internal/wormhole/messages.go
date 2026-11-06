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
	ID        string    `yaml:"id"`
	From      string    `yaml:"from"`
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

// splitEnvelope separates the YAML front matter block from the body. The
// front matter must start and end with a line consisting solely of "---".
func splitEnvelope(data []byte) (front []byte, body []byte, err error) {
	text := string(data)
	if !strings.HasPrefix(text, "---\n") {
		return nil, nil, fmt.Errorf("message must start with '---' front matter delimiter")
	}
	rest := text[len("---\n"):]
	idx := strings.Index(rest, "\n---\n")
	if idx < 0 {
		// A message with an empty body ends with "\n---" on the last line.
		if strings.HasSuffix(rest, "\n---") {
			return []byte(rest[:len(rest)-len("\n---")]), nil, nil
		}
		return nil, nil, fmt.Errorf("message front matter is not terminated by '---'")
	}
	front = []byte(rest[:idx])
	body = []byte(rest[idx+len("\n---\n"):])
	return front, body, nil
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
