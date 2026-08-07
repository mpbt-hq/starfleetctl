// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package timer

import (
	"strings"
	"testing"
	"time"
)

// TestParseAtTimeTimezone verifies that timezone suffixes and the tz argument
// are honored when parsing --at strings.
func TestParseAtTimeTimezone(t *testing.T) {
	now := time.Now().UTC()

	cases := []struct {
		name string
		at   string
		tz   string
		want time.Time // local wall-clock components (interpreted in the tz)
	}{
		{
			name: "explicit CEST suffix",
			at:   "15:20 CEST",
			tz:   "",
			want: time.Date(now.Year(), now.Month(), now.Day(), 15, 20, 0, 0, time.FixedZone("CEST", 2*3600)),
		},
		{
			name: "IANA suffix",
			at:   "09:00 Europe/Berlin",
			tz:   "",
			want: time.Date(now.Year(), now.Month(), now.Day(), 9, 0, 0, 0, loc(t, "Europe/Berlin")),
		},
		{
			name: "tz argument fallback",
			at:   "15:20",
			tz:   "CEST",
			want: time.Date(now.Year(), now.Month(), now.Day(), 15, 20, 0, 0, time.FixedZone("CEST", 2*3600)),
		},
		{
			name: "tomorrow with tz",
			at:   "tomorrow 17:30",
			tz:   "Europe/Berlin",
			want: time.Date(now.Year(), now.Month(), now.Day()+1, 17, 30, 0, 0, loc(t, "Europe/Berlin")),
		},
		{
			name: "no tz stays UTC",
			at:   "15:20",
			tz:   "",
			want: time.Date(now.Year(), now.Month(), now.Day(), 15, 20, 0, 0, time.UTC),
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := ParseAtTime(c.at, c.tz)
			if err != nil {
				t.Fatalf("ParseAtTime(%q, %q): %v", c.at, c.tz, err)
			}
			want := c.want.UTC()
			if !got.Equal(want) {
				t.Errorf("ParseAtTime(%q, %q) = %s, want %s", c.at, c.tz, got.Format(time.RFC3339), want.Format(time.RFC3339))
			}
		})
	}
}

// TestParseAtTimeRejectsTimezoneGarbage ensures a trailing unknown token is not
// silently accepted as a timezone.
func TestParseAtTimeRejectsTimezoneGarbage(t *testing.T) {
	_, err := ParseAtTime("15:20 NotAZone", "")
	if err == nil {
		t.Fatal("expected error for unknown timezone token, got nil")
	}
	if !strings.Contains(err.Error(), "invalid --at format") {
		t.Errorf("unexpected error: %v", err)
	}
}

func loc(t *testing.T, name string) *time.Location {
	t.Helper()
	l, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("LoadLocation(%q): %v", name, err)
	}
	return l
}
