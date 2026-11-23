// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package session

import "strings"

// shipProcessEnv returns a child environment with all inherited fleet identity
// keys removed and replaced by the identity explicitly assigned to that child.
// Unrelated STARFLEET_* settings (notably STARFLEET_BUS_DIR) are preserved.
func shipProcessEnv(base []string, shipID, role, target, launchType, class string) []string {
	env := make([]string, 0, len(base)+5)
	for _, kv := range base {
		switch {
		case strings.HasPrefix(kv, "STARFLEET_SHIP_ID="),
			strings.HasPrefix(kv, "STARFLEET_ROLE="),
			strings.HasPrefix(kv, "STARFLEET_TARGET="),
			strings.HasPrefix(kv, "STARFLEET_LAUNCH_TYPE="),
			strings.HasPrefix(kv, "STARFLEET_CLASS="):
			continue
		}
		env = append(env, kv)
	}
	env = append(env,
		"STARFLEET_SHIP_ID="+shipID,
		"STARFLEET_ROLE="+role,
		"STARFLEET_LAUNCH_TYPE="+launchType,
	)
	if target != "" {
		env = append(env, "STARFLEET_TARGET="+target)
	}
	if class != "" {
		env = append(env, "STARFLEET_CLASS="+class)
	}
	return env
}
