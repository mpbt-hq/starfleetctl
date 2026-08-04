// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// newTestServer returns a web Server rooted at a fresh temp git repo.
func newTestServer(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("git", "init", "-q")
	run("git", "config", "user.email", "test@test")
	run("git", "config", "user.name", "test")

	s, err := New(dir, "127.0.0.1:0")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

// seedTopic writes a topic file directly (simulating task capture output).
func seedTopic(t *testing.T, s *Server, slug, title, body string) {
	t.Helper()
	path := filepath.Join(s.dash.TopicsDir(), slug+".md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	src := "---\ntitle: " + title + "\n" +
		"category: active\nkind: task\nstatus: open\n" +
		"assigned-to: \"—\"\ncreated-by: \"Enterprise\"\n" +
		"created: \"2026-07-15T00:00:00Z\"\ndoc_ref: \"—\"\n" +
		"---\n\n" + body
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
}

// apiGet/apiPost drive the mux directly, returning status + decoded body.
func apiGet(t *testing.T, h http.Handler, url string) (int, map[string]any) {
	t.Helper()
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, url, nil))
	var m map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &m)
	return rr.Code, m
}

func apiPost(t *testing.T, h http.Handler, url string, v any) (int, string) {
	t.Helper()
	buf, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, url, bytes.NewReader(buf)))
	return rr.Code, rr.Body.String()
}

// TestTopicAPIRoundTrip exercises the full-text view/edit flow:
// GET returns the seeded body, POST rewrites body + title through the
// sanctioned dashboard path (committing locally with push=false), and the
// change is visible on a subsequent GET.
func TestTopicAPIRoundTrip(t *testing.T) {
	s := newTestServer(t)
	slug := "task-round-trip"
	seedTopic(t, s, slug, "Round Trip Task", "Some body text.\n")
	h := s.Handler()

	code, got := apiGet(t, h, "/api/topic/"+slug)
	if code != http.StatusOK {
		t.Fatalf("GET status %d", code)
	}
	if got["body"] != "Some body text.\n" || got["title"] != "Round Trip Task" {
		t.Fatalf("GET content wrong: %+v", got)
	}

	code, resp := apiPost(t, h, "/api/topic/"+slug,
		map[string]any{"title": "Edited Title", "body": "Edited body text.\n", "push": false})
	if code != http.StatusOK {
		t.Fatalf("POST status %d: %s", code, resp)
	}

	code, got = apiGet(t, h, "/api/topic/"+slug)
	if code != http.StatusOK {
		t.Fatalf("GET after POST status %d", code)
	}
	if got["body"] != "Edited body text.\n" || got["title"] != "Edited Title" {
		t.Fatalf("updated content wrong: %+v", got)
	}

	// The sanctioned commit ran (single-file, local only).
	out, err := exec.Command("git", "-C", s.Root, "log", "--oneline").Output()
	if err != nil {
		t.Fatalf("git log: %v", err)
	}
	if !strings.Contains(string(out), "web: topic update "+slug) {
		t.Fatalf("expected commit message not found in log:\n%s", out)
	}
}

// TestTopicAPISubdirSlug verifies the path-based route keeps a category slug's
// slashes intact (starfleet/task-x -> topics/starfleet/task-x.md).
func TestTopicAPISubdirSlug(t *testing.T) {
	s := newTestServer(t)
	slug := "starfleet/task-x"
	seedTopic(t, s, slug, "Subdir Task", "subdir body\n")
	h := s.Handler()

	code, got := apiGet(t, h, "/api/topic/"+slug)
	if code != http.StatusOK {
		t.Fatalf("GET status %d (body %v)", code, got)
	}
	if got["body"] != "subdir body\n" || got["slug"] != slug {
		t.Fatalf("subdir content wrong: %+v", got)
	}
}

// TestTopicAPICategoryMove verifies relocating a topic to another category
// through the edit API: the slug gains the category prefix, the file moves
// into the category subdirectory, frontmatter status/assignment/category are
// updated, and moving back to toplevel (category "") works too.
func TestTopicAPICategoryMove(t *testing.T) {
	s := newTestServer(t)
	slug := "task-x"
	seedTopic(t, s, slug, "Task X", "body\n")
	h := s.Handler()

	// Move toplevel -> starfleet/.
	code, resp := apiPost(t, h, "/api/topic/"+slug,
		map[string]any{"title": "Task X", "body": "body\n", "category": "starfleet",
			"status": "assigned", "assigned_to": "Reliant", "push": false})
	if code != http.StatusOK {
		t.Fatalf("POST move status %d: %s", code, resp)
	}
	newSlug := "starfleet/task-x"
	if _, err := os.Stat(filepath.Join(s.dash.TopicsDir(), newSlug+".md")); err != nil {
		t.Fatalf("moved file missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(s.dash.TopicsDir(), slug+".md")); !os.IsNotExist(err) {
		t.Fatalf("old file still present: %v", err)
	}
	if code, got := apiGet(t, h, "/api/topic/"+newSlug); code != http.StatusOK {
		t.Fatalf("GET new slug status %d", code)
	} else {
		if got["category"] != "starfleet" || got["status"] != "assigned" || got["assigned_to"] != "Reliant" {
			t.Fatalf("moved topic fields wrong: %+v", got)
		}
	}
	if code, _ := apiGet(t, h, "/api/topic/"+slug); code != http.StatusNotFound {
		t.Fatalf("GET old slug: want 404, got %d", code)
	}

	// Move back starfleet/ -> toplevel.
	code, resp = apiPost(t, h, "/api/topic/"+newSlug,
		map[string]any{"title": "Task X", "body": "body\n", "category": "", "push": false})
	if code != http.StatusOK {
		t.Fatalf("POST move-back status %d: %s", code, resp)
	}
	if _, err := os.Stat(filepath.Join(s.dash.TopicsDir(), slug+".md")); err != nil {
		t.Fatalf("back-moved file missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(s.dash.TopicsDir(), newSlug+".md")); !os.IsNotExist(err) {
		t.Fatalf("starfleet/ file still present: %v", err)
	}
	if code, got := apiGet(t, h, "/api/topic/"+slug); code != http.StatusOK {
		t.Fatalf("GET original slug status %d", code)
	} else if got["category"] != "" {
		t.Fatalf("toplevel category wrong: %+v", got)
	}

	// The sanctioned move commits ran (git log contains both messages).
	out, err := exec.Command("git", "-C", s.Root, "log", "--oneline").Output()
	if err != nil {
		t.Fatalf("git log: %v", err)
	}
	for _, want := range []string{"web: topic move task-x -> starfleet/task-x",
		"web: topic move starfleet/task-x -> task-x"} {
		if !strings.Contains(string(out), want) {
			t.Fatalf("expected commit %q not found in log:\n%s", want, out)
		}
	}
}

// TestTopicAPIErrors checks the failure paths: unknown slug -> 404, bad JSON
// and unknown method -> 4xx.
func TestTopicAPIErrors(t *testing.T) {
	s := newTestServer(t)
	seedTopic(t, s, "task-x", "X", "body\n")
	h := s.Handler()

	if code, _ := apiGet(t, h, "/api/topic/does-not-exist"); code != http.StatusNotFound {
		t.Fatalf("GET unknown slug: want 404, got %d", code)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/topic/task-x", bytes.NewReader([]byte("{bad"))))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("POST bad json: want 400, got %d", rr.Code)
	}
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodDelete, "/api/topic/task-x", nil))
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("DELETE: want 405, got %d", rr.Code)
	}
	if code, _ := apiGet(t, h, "/api/topic/"); code != http.StatusBadRequest {
		t.Fatalf("GET empty slug: want 400, got %d", code)
	}
}
