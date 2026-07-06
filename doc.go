// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
//
// Package starfleetctl embeds this repo's own README.md so `starfleetctl`
// can hand its own usage instructions to whatever workspace it's installed
// into (see internal/agents' InstallSelf), instead of that content being
// hand-duplicated and separately maintained in every consumer's AGENTS.md.
// Consequence: an AGENTS.md fragment installed this way always matches
// whatever starfleetctl commit is actually checked out — a starfleetctl
// update that changes behavior and updates README.md accordingly carries
// its own instruction update along, with no separate doc-sync step needed
// in the consuming workspace.
//
// (Praetor design decision, 2026-07-06, relayed via directive m0089: "die
// Instruktionen müssen dann NICHT MEHR separat in mpbt-workspace mitversioniert
// werden, sie leben und versionieren sich mit starfleetctl selbst" — this
// mirrors the existing mpbt-hq-independence rule that starfleetctl knowledge
// belongs in the starfleetctl repo, not duplicated into every consumer.)
package starfleetctl

import _ "embed"

//go:embed README.md
var Readme string
