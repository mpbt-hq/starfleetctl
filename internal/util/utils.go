// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package util

import "strings"

// TrimTrailingNewline removes trailing newlines and carriage returns.
func TrimTrailingNewline(s string) string {
	return strings.TrimRight(s, "\n\r")
}
