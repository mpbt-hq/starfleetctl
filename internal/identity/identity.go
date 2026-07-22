// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
//
// Package identity resolves the calling agent's fleet identity.
// Env var is STARFLEET_SHIP_ID.
package identity

import "os"

// ShipID returns the resolved ship identifier from STARFLEET_SHIP_ID env var.
func ShipID() string {
	return os.Getenv("STARFLEET_SHIP_ID")
}
