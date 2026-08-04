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
	seedMsg(t, dir, "m5", 500, "Stargazer", "all", "fleet broadcast")

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
