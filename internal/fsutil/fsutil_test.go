// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package fsutil

import "testing"

func TestSafe(t *testing.T) {
	tests := []struct {
		in     string
		want   string
		wantOK bool
	}{
		{"m0001", "m0001", true},
		{"123", "123", true},
		{"a.b-c_d", "a.b-c_d", true},
		{"foo bar", "foo_bar", true},
		{"../../etc/passwd", "", false},
		{"a/../b", "", false},
		{"/abs", "", false},
		{"..", "", false},
		{".", "", false},
		{"", "", false},
		{"with/slash", "", false},
		{"a..b", "", false}, // contains ".."
	}
	for _, tc := range tests {
		got, ok := Safe(tc.in)
		if ok != tc.wantOK {
			t.Errorf("Safe(%q) ok=%v, want %v", tc.in, ok, tc.wantOK)
			continue
		}
		if got != tc.want {
			t.Errorf("Safe(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
