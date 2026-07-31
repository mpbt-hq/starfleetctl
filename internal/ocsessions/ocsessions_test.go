// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package ocsessions

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidToken(t *testing.T) {
	good := []string{"ses_04c828f38ffefmfeMvGa4U3VcF", "msg_fb7aa3bf4001plKpLP9HehliiM", "aB_9-Z"}
	for _, s := range good {
		if !ValidToken(s) {
			t.Errorf("ValidToken(%q) = false, want true", s)
		}
	}
	bad := []string{"", "a'b", "a;DROP", "a b", "a/b", "a\"b", "ümlaut", "a\nb"}
	for _, s := range bad {
		if ValidToken(s) {
			t.Errorf("ValidToken(%q) = true, want false", s)
		}
	}
}

func TestCleanValue(t *testing.T) {
	if v := cleanValue("Enterprise"); v != "Enterprise" {
		t.Errorf("cleanValue(Enterprise) = %q", v)
	}
	// Quotes, semicolons and comment markers must be stripped so the value
	// can never escape the SQL string literal.
	for _, s := range []string{"'; DROP TABLE session;--", "' OR '1'='1", "a\"b", "a--b", "a;b", "a\nb"} {
		if v := cleanValue(s); strings.ContainsAny(v, `';"`+"\n") {
			t.Errorf("cleanValue(%q) = %q still contains metacharacters", s, v)
		}
	}
	if v := cleanValue(""); v != "" {
		t.Errorf("cleanValue(empty) = %q, want empty", v)
	}
}

func TestTailLog(t *testing.T) {
	dir := t.TempDir()
	dbPathCache = filepath.Join(dir, "opencode.db")
	logDir := filepath.Join(dir, "log")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "line1\nline2\nline3\nline4\nline5\n"
	if err := os.WriteFile(filepath.Join(logDir, "opencode.log"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	lines, err := TailLog(2)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 || lines[0] != "line4" || lines[1] != "line5" {
		t.Errorf("TailLog(2) = %v, want [line4 line5]", lines)
	}
}

// TestListAndTranscript exercises the real sqlite3 CLI against a temporary DB
// mirroring the opencode schema. Skipped when sqlite3 is unavailable.
func TestListAndTranscript(t *testing.T) {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Skip("sqlite3 CLI not available")
	}
	dir := t.TempDir()
	db := filepath.Join(dir, "opencode.db")

	bigText := strings.Repeat("x", partCap*2)
	schema := `
CREATE TABLE session (
  id TEXT PRIMARY KEY,
  title TEXT NOT NULL,
  agent TEXT NOT NULL,
  model TEXT,
  directory TEXT,
  time_created INTEGER,
  time_updated INTEGER,
  cost REAL,
  tokens_input INTEGER,
  tokens_output INTEGER,
  tokens_reasoning INTEGER
);
CREATE TABLE message (
  id TEXT PRIMARY KEY,
  session_id TEXT NOT NULL,
  time_created INTEGER,
  data TEXT
);
CREATE TABLE part (
  id TEXT PRIMARY KEY,
  message_id TEXT NOT NULL,
  session_id TEXT NOT NULL,
  time_created INTEGER,
  data TEXT
);
INSERT INTO session VALUES
  ('ses_test1','Enterprise','build','{"id":"big-pickle","providerID":"opencode"}','/ws',100,300,0.5,1000,200,50),
  ('ses_test2','Voyager','plan','{"id":"other/model","providerID":"nvidia"}','/ws',200,400,0.25,500,100,10);
INSERT INTO message VALUES
  ('msg_1','ses_test1',10,'{"role":"user","time":{"created":10}}'),
  ('msg_2','ses_test1',20,'{"role":"assistant","time":{"created":20},"finish":"tool-calls","modelID":"big-pickle","tokens":{"total":120}}'),
  ('msg_3','ses_test1',30,'{"role":"assistant","time":{"created":30}}');
INSERT INTO part VALUES
  ('p_1','msg_1','ses_test1',10,'{"type":"text","text":"hello"}'),
  ('p_2','msg_2','ses_test1',20,'{"type":"reasoning","text":"think"}'),
  ('p_3','msg_2','ses_test1',21,'{"type":"tool","tool":"bash","state":{"input":"ls","output":"file1"}}'),
  ('p_4','msg_2','ses_test1',22,'{"type":"text","text":"` + bigText + `"}');
`
	if err := exec.Command("sqlite3", db, schema).Run(); err != nil {
		t.Fatalf("create test db: %v", err)
	}
	dbPathCache = db
	defer func() { dbPathCache = "" }()

	sessions, err := List(ListOpts{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 2 {
		t.Fatalf("List() = %d sessions, want 2", len(sessions))
	}
	// Newest first.
	if sessions[0].Title != "Voyager" || sessions[0].Mode != "plan" {
		t.Errorf("sessions[0] = %+v, want Voyager/plan", sessions[0])
	}
	if sessions[1].Title != "Enterprise" || sessions[1].Model != "big-pickle" {
		t.Errorf("sessions[1] = %+v, want Enterprise/big-pickle", sessions[1])
	}
	if sessions[1].Updated != 300 || sessions[1].TokensInput != 1000 || sessions[1].Cost != 0.5 {
		t.Errorf("sessions[1] meta wrong: %+v", sessions[1])
	}

	filtered, err := List(ListOpts{Title: "Enterprise"})
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered) != 1 || filtered[0].ID != "ses_test1" {
		t.Errorf("List(Title=Enterprise) = %+v", filtered)
	}

	tr, err := SessionTranscript("ses_test1", 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	if tr.Session.Title != "Enterprise" || tr.Session.Running {
		t.Errorf("transcript session wrong: %+v", tr.Session)
	}
	if len(tr.Messages) != 3 {
		t.Fatalf("transcript has %d messages, want 3", len(tr.Messages))
	}
	// Chronological order (newest-first window reversed).
	if tr.Messages[0].ID != "msg_1" || tr.Messages[2].ID != "msg_3" {
		t.Errorf("message order wrong: %v", tr.Messages)
	}
	if tr.Messages[1].Role != "assistant" || tr.Messages[1].Finish != "tool-calls" || tr.Messages[1].Tokens != 120 {
		t.Errorf("msg_2 meta wrong: %+v", tr.Messages[1])
	}
	parts := tr.Messages[1].Parts
	if len(parts) != 3 {
		t.Fatalf("msg_2 has %d parts, want 3", len(parts))
	}
	var sawTool, sawTrunc bool
	for _, p := range parts {
		if p.Type == "tool" {
			sawTool = p.Tool == "bash" && p.Input == "ls" && p.Output == "file1"
		}
		if p.Type == "text" && p.Text != "hello" && len(p.Text) == partCap && p.Truncated {
			sawTrunc = true
		}
	}
	if !sawTool {
		t.Errorf("tool part missing/incorrect: %+v", parts)
	}
	if !sawTrunc {
		t.Errorf("truncation flag not set for oversized text part: %+v", parts)
	}

	// Pagination: window of 1 message starting at offset 1 returns msg_2 only.
	tr2, err := SessionTranscript("ses_test1", 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(tr2.Messages) != 1 || tr2.Messages[0].ID != "msg_2" {
		t.Errorf("Transcript(1,1) = %v, want [msg_2]", tr2.Messages)
	}

	if _, err := SessionTranscript("ses_test1'; DROP TABLE session;--", 10, 0); err == nil {
		t.Error("Transcript accepted a malicious session id")
	}
}
