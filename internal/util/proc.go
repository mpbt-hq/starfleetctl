// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package util

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// IsStarfleetctlPID reports whether the given PID currently belongs to a
// running starfleetctl process, by checking that its /proc/<pid>/cmdline
// contains "starfleetctl". Used before killing a PID read from a PID file: if
// the file is stale and the PID has since been reused by an unrelated process,
// this returns false so we do not kill the wrong thing.
func IsStarfleetctlPID(pid int) bool {
	if pid <= 0 {
		return false
	}
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline"))
	if err != nil {
		return false
	}
	return strings.Contains(string(data), "starfleetctl")
}
