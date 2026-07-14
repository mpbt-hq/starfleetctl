// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
//
// Package templates holds embedded source-data files that more than one
// starfleetctl subsystem needs to materialize on disk (currently just the
// ship-name pool). Keeping them here — rather than duplicating the bytes in
// both internal/genesis and internal/bootstrap — gives every consumer a
// single source of truth.
package templates

import _ "embed"

// ShipNamesRel is the in-repo path (relative to a workspace root) where the
// ship-name pool is installed.
const ShipNamesRel = ".starfleet-ai/etc/ship-names.txt"

// ShipNames is the canonical ship-name pool, embedded so both `genesis-init`
// and `bootstrap --fix` can (re)create it without any network access.
//
//go:embed ship-names.txt
var ShipNames []byte
