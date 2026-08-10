// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package comms

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// seedMsg writes a flat msgs/<id>.json record into dir.
func seedMsg(t *testing.T, dir, id string, epoch int64, from, target, text string) {
	t.Helper()
	rec := fmt.Sprintf(`{"id":%q,"epoch":%d,"from":%q,"target":%q,"text":%q}`,
		id, epoch, from, target, text)
	if err := os.WriteFile(filepath.Join(dir, id+".json"), []byte(rec), 0644); err != nil {
		t.Fatal(err)
	}
}

// TestConversation_FiltersOutUnrelatedShips is a regression test for the
// ship-chat bug: messages involving the web viewer (McKinley) but not the
// selected ship must NOT leak into the conversation.
func TestConversation_FiltersOutUnrelatedShips(t *testing.T) {
	dir := t.TempDir()
	b := &Bus{MsgDir: dir}

	seedMsg(t, dir, "m1", 100, "Stargazer", "Enterprise", "halllo enterprise")
	seedMsg(t, dir, "m2", 200, "Enterprise", "Stargazer", "reply")
	seedMsg(t, dir, "m3", 300, "Stargazer", "Voyager", "unrelated")
	seedMsg(t, dir, "m4", 400, "McKinley", "Stargazer", "unrelated viewer msg")
	// Fleet broadcast: in the fan-out model this is a per-ship copy carrying
	// the concrete recipient (Target=Enterprise), not the legacy "all" target.
	seedMsg(t, dir, "m5", 500, "Stargazer", "Enterprise", "fleet broadcast")

	msgs := b.Conversation("Enterprise")

	got := map[string]bool{}
	for _, m := range msgs {
		got[m.ID] = true
	}
	for _, want := range []string{"m1", "m2", "m5"} {
		if !got[want] {
			t.Errorf("Conversation(Enterprise) missing %s", want)
		}
	}
	for _, forbid := range []string{"m3", "m4"} {
		if got[forbid] {
			t.Errorf("Conversation(Enterprise) must not contain %s (unrelated to Enterprise)", forbid)
		}
	}
}

// TestConversation_NewestFirst verifies the newest-first ordering that the
// web UI relies on.
func TestConversation_NewestFirst(t *testing.T) {
	dir := t.TempDir()
	b := &Bus{MsgDir: dir}

	seedMsg(t, dir, "m1", 100, "Stargazer", "Enterprise", "old")
	seedMsg(t, dir, "m2", 200, "Enterprise", "Stargazer", "middle")
	seedMsg(t, dir, "m3", 300, "Enterprise", "Stargazer", "newest")

	msgs := b.Conversation("Enterprise")
	if len(msgs) != 3 {
		t.Fatalf("want 3 messages, got %d", len(msgs))
	}
	if msgs[0].ID != "m3" || msgs[1].ID != "m2" || msgs[2].ID != "m1" {
		t.Errorf("Conversation(Enterprise) order = %s/%s/%s, want newest first m3/m2/m1",
			msgs[0].ID, msgs[1].ID, msgs[2].ID)
	}
}

// TestTailLines verifies that tailLines reads only the last n lines, including
// the large-file (multi-chunk) case where the chunk boundary falls mid-line.
func TestTailLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.log")

	// Build a file larger than the tail chunk size (64KB) so multiple chunks
	// get read back. Each line is "line-NNNNN\n".
	big := make([]byte, 0, 80*1024)
	const lines = 5000
	for i := 0; i < lines; i++ {
		big = append(big, fmt.Sprintf("line-%05d\n", i)...)
	}
	if err := os.WriteFile(path, big, 0644); err != nil {
		t.Fatal(err)
	}

	got, err := tailLines(path, 3)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"line-04997", "line-04998", "line-04999"}
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("got %q, want %q", got, want)
	}

	// More lines than the file has: return everything.
	gotAll, err := tailLines(path, 999999)
	if err != nil {
		t.Fatal(err)
	}
	if len(gotAll) != lines {
		t.Fatalf("tailLines(999999) = %d lines, want %d", len(gotAll), lines)
	}

	// No trailing newline.
	noNL := filepath.Join(dir, "nonl.log")
	if err := os.WriteFile(noNL, []byte("a\nb\nc"), 0644); err != nil {
		t.Fatal(err)
	}
	got3, err := tailLines(noNL, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got3) != 2 || got3[0] != "b" || got3[1] != "c" {
		t.Fatalf("no-NL tail = %q, want [b c]", got3)
	}

	// Empty / missing file.
	if got, err := tailLines(path+"nope", 5); err == nil && len(got) != 0 {
		t.Fatalf("missing file tail = %q, want empty", got)
	}
	empty := filepath.Join(dir, "empty.log")
	if err := os.WriteFile(empty, nil, 0644); err != nil {
		t.Fatal(err)
	}
	if got, err := tailLines(empty, 5); err != nil || len(got) != 0 {
		t.Fatalf("empty file tail = %q (err=%v), want empty", got, err)
	}

	// n <= 0 returns nothing.
	if got, err := tailLines(path, 0); err != nil || len(got) != 0 {
		t.Fatalf("n=0 tail = %q (err=%v), want empty", got, err)
	}
}

// TestInboxCounts verifies that the one-pass InboxCounts() matches the
// per-agent inboxCount() semantics for the modern per-ship message layout
// (msgs/<agent>/unseen/ + msgs/<agent>/seen/).
func TestInboxCounts(t *testing.T) {
	dir := t.TempDir()
	b := &Bus{MsgDir: dir}

	write := func(rel, id string) {
		t.Helper()
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(p, 0755); err != nil {
			t.Fatal(err)
		}
		// Real unseen/seen copies carry the recipient ship in the target field.
		ship := filepath.Base(filepath.Dir(p))
		seedMsg(t, p, id, 100, "Sender", ship, "hi")
	}

	// Enterprise: 2 unseen + 1 seen (acked).
	write(filepath.Join("Enterprise", "unseen"), "m1")
	write(filepath.Join("Enterprise", "unseen"), "m2")
	write(filepath.Join("Enterprise", "seen"), "m3")
	// Voyager: 1 unseen.
	write(filepath.Join("Voyager", "unseen"), "m4")
	// Hidden dir + stray file at top level must be ignored.
	write(filepath.Join(".hidden", "unseen"), "m5")
	if err := os.WriteFile(filepath.Join(dir, "m6.json"), []byte(`{"id":"m6","epoch":1,"from":"x","target":"y","text":"z"}`), 0644); err != nil {
		t.Fatal(err)
	}

	got := b.InboxCounts()
	if got["Enterprise"] != 2 {
		t.Fatalf("InboxCounts Enterprise = %d, want 2", got["Enterprise"])
	}
	if got["Voyager"] != 1 {
		t.Fatalf("InboxCounts Voyager = %d, want 1", got["Voyager"])
	}
	if got[".hidden"] != 0 {
		t.Fatalf("InboxCounts hidden dir = %d, want 0", got[".hidden"])
	}
	if _, ok := got["m6"]; ok {
		t.Fatalf("InboxCounts counted a top-level file")
	}

	// Cross-check against the old per-agent scan on the same store.
	if got["Enterprise"] != b.inboxCount("Enterprise") {
		t.Fatalf("InboxCounts Enterprise %d != inboxCount %d", got["Enterprise"], b.inboxCount("Enterprise"))
	}
}
