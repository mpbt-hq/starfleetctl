package web

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/metux/starfleetctl/internal/timer"
)

// TestTimerCreateWithTZ verifies the web API accepts a timezone, parses the
// --at time in that zone, and persists the timezone on the record.
func TestTimerCreateWithTZ(t *testing.T) {
	s := newTestServer(t)
	body, _ := json.Marshal(map[string]any{
		"schedule_type": "once",
		"target_type":   "ship",
		"type":          "ship",
		"text":          "test tz",
		"at":            "15:20",
		"tz":            "Europe/Berlin",
	})
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest("POST", "/api/timer", bytes.NewReader(body)))
	if rr.Code != 200 {
		t.Fatalf("status = %d, body: %s", rr.Code, rr.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	id, _ := resp["id"].(string)
	if id == "" {
		t.Fatalf("no id in response: %s", rr.Body.String())
	}

	rec, err := timer.PickStore(s.Root, false)
	if err != nil {
		t.Fatal(err)
	}
	got, err := rec.Get(id)
	if err != nil {
		t.Fatalf("Get(%q): %v", id, err)
	}
	if got.Timezone != "Europe/Berlin" {
		t.Errorf("Timezone = %q, want Europe/Berlin", got.Timezone)
	}
}
