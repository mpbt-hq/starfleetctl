// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package projectconfig

import (
	"testing"
)

func TestGetRemapCandidates(t *testing.T) {
	cfg := &ProjectConfig{
		PathRemapping: PathRemapping{
			Enabled: true,
			Prefix:  "Xext/",
			Renames: map[string]string{
				"Xext/xkeyboard/": "xkb/",
			},
		},
	}

	tests := []struct {
		name     string
		input    string
		contains []string // must contain these
	}{
		{
			name:     "Xext/xkeyboard/ file should map to xkb/",
			input:    "Xext/xkeyboard/xkb.c",
			contains: []string{"Xext/xkeyboard/xkb.c", "xkeyboard/xkb.c", "xkb/xkb.c"},
		},
		{
			name:     "xkb/ file should map to Xext/xkeyboard/",
			input:    "xkb/xkb.c",
			contains: []string{"xkb/xkb.c", "Xext/xkb/xkb.c", "Xext/xkeyboard/xkb.c"},
		},
		{
			name:     "Xext/ prefix strip",
			input:    "Xext/foo/bar.c",
			contains: []string{"Xext/foo/bar.c", "foo/bar.c"},
		},
		{
			name:     "no prefix, no rename",
			input:    "foo/bar.c",
			contains: []string{"foo/bar.c", "Xext/foo/bar.c"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := cfg.GetRemapCandidates(tt.input)
			for _, expected := range tt.contains {
				found := false
				for _, v := range result {
					if v == expected {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("GetRemapCandidates(%q) = %v, missing expected %q", tt.input, result, expected)
				}
			}
		})
	}
}
