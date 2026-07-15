// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

// Package fsutil holds filesystem-name safety helpers shared across the
// packages that derive on-disk file names from user-supplied identifiers
// (agent-bus message ids, pr-claim PR numbers, ship names). The helpers
// guarantee that the returned name can never escape its intended directory
// via ".." or "/" — a class of path-traversal bug that previously let an
// argument like "../../foo" write/read/delete an arbitrary file.
package fsutil

import "strings"

// Safe turns an arbitrary identifier into a filesystem-safe base name and
// reports whether it is acceptable as a single path element. It rejects:
//   - empty strings,
//   - "." or ".." (directory references),
//   - anything containing a '/' (path separator) or a ".." element.
//
// Acceptable bytes are kept verbatim; every other byte is mapped to '_'
// (mirroring the bash original's `tr -c 'A-Za-z0-9._-' '_'`). The result
// is therefore guaranteed to be a single path component with no directory
// separators, so joining it under a known directory can never escape that
// directory.
//
// Callers must treat a false return as "refuse this identifier" — never
// fall back to using the raw input.
func Safe(s string) (string, bool) {
	if s == "" || s == "." || s == ".." {
		return "", false
	}
	if strings.Contains(s, "/") || strings.Contains(s, "..") {
		return "", false
	}
	b := []byte(s)
	for i, c := range b {
		if !((c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') ||
			(c >= '0' && c <= '9') || c == '.' || c == '_' || c == '-') {
			b[i] = '_'
		}
	}
	out := string(b)
	if out == "" || out == "." || out == ".." {
		return "", false
	}
	return out, true
}
