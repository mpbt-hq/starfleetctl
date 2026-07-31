// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
//
// Package ocsessions reads the opencode session database (SQLite) and client
// log so the fleet web console can monitor opencode sessions that are NOT
// running under a termctl terminal (manually started, timer-spawned, etc.).
//
// The DB is always opened read-only (`sqlite3 -readonly`) — SQLite's WAL mode
// lets a reader coexist with the live opencode writer without blocking it.
// Queries run via the `sqlite3` CLI (no new Go dependencies); the DB path is
// resolved once via `opencode db path` and cached. All values embedded into
// SQL are sanitized (see cleanValue / ValidToken) since the CLI cannot bind
// parameters.
package ocsessions

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// partCap is the per-field truncation cap (bytes) applied to transcript text /
// tool input / tool output before sending them to the web frontend. It keeps
// responses bounded even though single parts can exceed 1 MB.
const partCap = 8192

// maxLimit is the hard upper bound for the session list / transcript window.
const maxLimit = 500

var dbPathCache string

// ListOpts filters the session list.
type ListOpts struct {
	Title string // match session title (ship name) exactly
	Mode  string // match mode (build / plan / explore / general) exactly
	Limit int    // max rows (1..maxLimit, default 50)
}

// Session is one opencode session row (a single opencode launch).
type Session struct {
	ID              string  `json:"id"`
	Title           string  `json:"title"`
	Mode            string  `json:"mode"`
	Model           string  `json:"model"`
	Directory       string  `json:"directory"`
	Created         int64   `json:"created"`
	Updated         int64   `json:"updated"`
	Cost            float64 `json:"cost"`
	TokensInput     int64   `json:"tokens_input"`
	TokensOutput    int64   `json:"tokens_output"`
	TokensReasoning int64   `json:"tokens_reasoning"`
	Running         bool    `json:"running"`
}

// Part is one content part of a message (text, reasoning, tool call, ...).
// Text/Input/Output are capped at partCap; Truncated signals an elided tail.
type Part struct {
	MessageID string `json:"message_id"`
	Type      string `json:"type"`
	Tool      string `json:"tool,omitempty"`
	Text      string `json:"text,omitempty"`
	Input     string `json:"input,omitempty"`
	Output    string `json:"output,omitempty"`
	Truncated bool   `json:"truncated"`
}

// Msg is one transcript message with its parts.
type Msg struct {
	ID     string `json:"id"`
	Role   string `json:"role"`
	Finish string `json:"finish,omitempty"`
	Model  string `json:"model_id,omitempty"`
	Time   int64  `json:"time"`
	Tokens int64  `json:"tokens_total"`
	Parts  []Part `json:"parts"`
}

// Transcript is a session's meta plus a chronological window of messages.
type Transcript struct {
	Session  Session `json:"session"`
	Messages []Msg   `json:"messages"`
}

// ResolveDBPath returns the opencode SQLite database path, resolving it via
// `opencode db path` (honouring XDG data dirs and any --data override) and
// caching the result. Falls back to the XDG default when the CLI is missing.
func ResolveDBPath() (string, error) {
	if dbPathCache != "" {
		return dbPathCache, nil
	}
	// Explicit override wins (e.g. a custom --data dir or test harness).
	if env := strings.TrimSpace(os.Getenv("OPENCODE_DATA_DIR")); env != "" {
		if p, err := checkDB(filepath.Join(env, "opencode.db")); err == nil {
			dbPathCache = p
			return p, nil
		}
	}
	if out, err := exec.Command("opencode", "db", "path").Output(); err == nil {
		if p, err := checkDB(strings.TrimSpace(string(out))); err == nil {
			dbPathCache = p
			return p, nil
		}
	}
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("ocsessions: cannot resolve home dir: %w", err)
		}
		base = filepath.Join(home, ".local", "share")
	}
	p, err := checkDB(filepath.Join(base, "opencode", "opencode.db"))
	if err != nil {
		return "", fmt.Errorf("ocsessions: opencode database not found: %w", err)
	}
	dbPathCache = p
	return p, nil
}

// checkDB verifies that p exists and is a regular file.
func checkDB(p string) (string, error) {
	st, err := os.Stat(p)
	if err != nil {
		return "", err
	}
	if st.IsDir() {
		return "", fmt.Errorf("not a file: %s", p)
	}
	return p, nil
}

// LogPath returns the opencode client log path (data dir / log / opencode.log).
func LogPath() (string, error) {
	db, err := dbPath()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(db), "log", "opencode.log"), nil
}

func dbPath() (string, error) {
	if dbPathCache == "" {
		if _, err := ResolveDBPath(); err != nil {
			return "", err
		}
	}
	return dbPathCache, nil
}

// List returns the most recently updated sessions, optionally filtered.
func List(o ListOpts) ([]Session, error) {
	db, err := dbPath()
	if err != nil {
		return nil, err
	}
	limit := o.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > maxLimit {
		limit = maxLimit
	}
	var conds []string
	if v := cleanValue(o.Title); v != "" {
		conds = append(conds, "title = '"+v+"'")
	}
	if v := cleanValue(o.Mode); v != "" {
		conds = append(conds, "agent = '"+v+"'")
	}
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}
	sql := `SELECT id, title, agent AS mode, json_extract(model, '$.id') AS model, directory, ` +
		`CAST(time_created AS TEXT) AS created, CAST(time_updated AS TEXT) AS updated, cost, ` +
		`CAST(tokens_input AS TEXT) AS tokens_input, CAST(tokens_output AS TEXT) AS tokens_output, ` +
		`CAST(tokens_reasoning AS TEXT) AS tokens_reasoning FROM session` + where +
		` ORDER BY time_updated DESC LIMIT ` + strconv.Itoa(limit)
	rows, err := query(db, sql)
	if err != nil {
		return nil, err
	}
	out := make([]Session, 0, len(rows))
	for _, r := range rows {
		out = append(out, parseSession(r))
	}
	return out, nil
}

// SessionTranscript returns a session's meta plus a chronological window of
// its messages (the most recent `limit` messages starting at `offset`).
func SessionTranscript(id string, limit, offset int) (*Transcript, error) {
	if !ValidToken(id) {
		return nil, fmt.Errorf("ocsessions: invalid session id")
	}
	db, err := dbPath()
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 50
	}
	if limit > maxLimit {
		limit = maxLimit
	}
	if offset < 0 {
		offset = 0
	}

	// Session meta.
	metaRows, err := query(db, `SELECT id, title, agent AS mode, json_extract(model, '$.id') AS model, directory, `+
		`CAST(time_created AS TEXT) AS created, CAST(time_updated AS TEXT) AS updated, cost, `+
		`CAST(tokens_input AS TEXT) AS tokens_input, CAST(tokens_output AS TEXT) AS tokens_output, `+
		`CAST(tokens_reasoning AS TEXT) AS tokens_reasoning FROM session WHERE id = '`+id+`'`)
	if err != nil {
		return nil, err
	}
	if len(metaRows) == 0 {
		return nil, fmt.Errorf("ocsessions: session not found: %s", id)
	}
	t := &Transcript{Session: parseSession(metaRows[0])}

	// Message window (newest first in SQL, reversed to chronological below).
	msgRows, err := query(db, `SELECT id, CAST(time_created AS TEXT) AS time, `+
		`json_extract(data, '$.role') AS role, json_extract(data, '$.finish') AS finish, `+
		`json_extract(data, '$.modelID') AS model_id, json_extract(data, '$.tokens.total') AS tokens_total `+
		`FROM message WHERE session_id = '`+id+`' ORDER BY time_created DESC, id `+
		`LIMIT `+strconv.Itoa(limit)+` OFFSET `+strconv.Itoa(offset))
	if err != nil {
		return nil, err
	}
	t.Messages = make([]Msg, 0, len(msgRows))
	ids := make([]string, 0, len(msgRows))
	for i := len(msgRows) - 1; i >= 0; i-- { // reverse: chronological order
		m := parseMsg(msgRows[i])
		t.Messages = append(t.Messages, m)
		ids = append(ids, m.ID)
	}

	// Parts for exactly the shown messages (grouped by message_id in Go).
	if len(ids) > 0 {
		capStr := strconv.Itoa(partCap)
		partRows, err := query(db, `SELECT message_id, CAST(time_created AS TEXT) AS time, `+
			`json_extract(data, '$.type') AS type, json_extract(data, '$.tool') AS tool, `+
			`substr(json_extract(data, '$.text'), 1, `+capStr+`) AS text, `+
			`substr(json_extract(data, '$.state.input'), 1, `+capStr+`) AS input, `+
			`substr(json_extract(data, '$.state.output'), 1, `+capStr+`) AS output, `+
			`(length(json_extract(data, '$.text')) > `+capStr+`) AS text_trunc, `+
			`(length(json_extract(data, '$.state.input')) > `+capStr+`) AS input_trunc, `+
			`(length(json_extract(data, '$.state.output')) > `+capStr+`) AS output_trunc `+
			`FROM part WHERE message_id IN ('`+strings.Join(ids, "','")+`') `+
			`ORDER BY message_id, time_created, id`)
		if err != nil {
			return nil, err
		}
		byMsg := make(map[string][]Part, len(t.Messages))
		for _, r := range partRows {
			p := parsePart(r)
			byMsg[p.MessageID] = append(byMsg[p.MessageID], p)
		}
		for i := range t.Messages {
			t.Messages[i].Parts = byMsg[t.Messages[i].ID]
		}
	}
	return t, nil
}

// TailLog returns the last n lines of the opencode client log.
func TailLog(n int) ([]string, error) {
	path, err := LogPath()
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("ocsessions: open log: %w", err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("ocsessions: stat log: %w", err)
	}
	if st.Size() == 0 {
		return []string{}, nil
	}
	const readChunk = 2 << 20 // 2 MiB tail window
	start := st.Size() - readChunk
	if start < 0 {
		start = 0
	}
	buf := make([]byte, st.Size()-start)
	if _, err := f.ReadAt(buf, start); err != nil {
		return nil, fmt.Errorf("ocsessions: read log: %w", err)
	}
	s := string(buf)
	if start > 0 { // cut the leading partial line
		if i := strings.IndexByte(s, '\n'); i >= 0 {
			s = s[i+1:]
		}
	}
	lines := strings.Split(strings.TrimSuffix(s, "\n"), "\n")
	if n <= 0 {
		n = 200
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines, nil
}

// query runs a read-only SQL query via the sqlite3 CLI and returns the rows.
func query(db, sql string) ([]map[string]any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sqlite3", "-readonly", "-json", db, sql)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("ocsessions: sqlite3: %s", msg)
	}
	dec := json.NewDecoder(bytes.NewReader(out))
	dec.UseNumber()
	var rows []map[string]any
	if err := dec.Decode(&rows); err != nil {
		return nil, fmt.Errorf("ocsessions: parse sqlite3 output: %w", err)
	}
	return rows, nil
}

func parseSession(r map[string]any) Session {
	return Session{
		ID:              sVal(r, "id"),
		Title:           sVal(r, "title"),
		Mode:            sVal(r, "mode"),
		Model:           sVal(r, "model"),
		Directory:       sVal(r, "directory"),
		Created:         iVal(r, "created"),
		Updated:         iVal(r, "updated"),
		Cost:            fVal(r, "cost"),
		TokensInput:     iVal(r, "tokens_input"),
		TokensOutput:    iVal(r, "tokens_output"),
		TokensReasoning: iVal(r, "tokens_reasoning"),
	}
}

func parseMsg(r map[string]any) Msg {
	return Msg{
		ID:     sVal(r, "id"),
		Role:   sVal(r, "role"),
		Finish: sVal(r, "finish"),
		Model:  sVal(r, "model_id"),
		Time:   iVal(r, "time"),
		Tokens: iVal(r, "tokens_total"),
	}
}

func parsePart(r map[string]any) Part {
	return Part{
		MessageID: sVal(r, "message_id"),
		Type:      sVal(r, "type"),
		Tool:      sVal(r, "tool"),
		Text:      sVal(r, "text"),
		Input:     sVal(r, "input"),
		Output:    sVal(r, "output"),
		Truncated: bVal(r, "text_trunc") || bVal(r, "input_trunc") || bVal(r, "output_trunc"),
	}
}

// ValidToken reports whether s is safe to embed into a SQL literal: a
// non-empty id/path token consisting of ASCII letters, digits, '_' and '-'.
func ValidToken(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' ||
			r == '_' || r == '-') {
			return false
		}
	}
	return true
}

// cleanValue strips everything but a conservative safe set from a filter value
// before embedding it into a SQL string literal. Empty results disable the
// filter (callers treat "" as "no filter").
func cleanValue(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' ||
			r == '_' || r == '-' || r == '.' || r == '/' || r == '@' || r == '%' || r == '+' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func sVal(m map[string]any, k string) string {
	if v, ok := m[k]; ok && v != nil {
		return fmt.Sprint(v)
	}
	return ""
}

func iVal(m map[string]any, k string) int64 {
	v, ok := m[k]
	if !ok || v == nil {
		return 0
	}
	switch n := v.(type) {
	case json.Number:
		i, _ := n.Int64()
		return i
	case float64:
		return int64(n)
	case string:
		i, _ := strconv.ParseInt(n, 10, 64)
		return i
	}
	return 0
}

func fVal(m map[string]any, k string) float64 {
	v, ok := m[k]
	if !ok || v == nil {
		return 0
	}
	switch n := v.(type) {
	case json.Number:
		f, _ := n.Float64()
		return f
	case float64:
		return n
	case string:
		f, _ := strconv.ParseFloat(n, 64)
		return f
	}
	return 0
}

func bVal(m map[string]any, k string) bool {
	v, ok := m[k]
	if !ok || v == nil {
		return false
	}
	switch n := v.(type) {
	case bool:
		return n
	case float64:
		return n != 0
	case json.Number:
		i, _ := n.Int64()
		return i != 0
	case string:
		return n != "" && n != "0"
	}
	return false
}
