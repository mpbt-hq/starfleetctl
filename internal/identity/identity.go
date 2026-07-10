// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
//
// Package identity resolves the calling agent's fleet identity. The env-var
// name changed from AGENT_ID → STARFLEET_SHIP_ID (item 43,
// 2DO-starfleet-deployment.md). For a transition period both names are
// accepted: STARFLEET_SHIP_ID is preferred; AGENT_ID is the fallback.
package identity

import "os"

// ShipID returns the resolved ship identifier: STARFLEET_SHIP_ID env var if
// set, otherwise AGENT_ID, otherwise empty string.
func ShipID() string {
	if v := os.Getenv("STARFLEET_SHIP_ID"); v != "" {
		return v
	}
	return os.Getenv("AGENT_ID")
}
