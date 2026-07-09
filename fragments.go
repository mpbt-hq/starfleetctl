// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package starfleetctl

import "embed"

//go:embed all:fragments
var Fragments embed.FS

// FragmentsRoot is the root directory inside the embedded FS.
const FragmentsRoot = "fragments"
