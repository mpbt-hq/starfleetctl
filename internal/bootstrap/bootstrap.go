// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
//
// Package bootstrap is starfleetctl's own self-setup/self-check command —
// Phase 3 of the starfleetctl roadmap (DASHBOARD.md, 2026-07-06). It is
// deliberately narrow: it only handles the fleet-management-specific
// one-time setup,
// NOT the broader "set up the whole mpbt build system" job — that's
// ./bootstrap's (bash, repo root) job, covering mpbt-builder, project
// clones, and the build environment. `starfleetctl bootstrap` is meant to run
// AFTER ./bootstrap has fetched+built the starfleetctl binary — see
// ./bootstrap's own final step, which now calls this.
//
// IMPORTANT LIMITATION, not fixed by this package (see DASHBOARD.md's
// starfleetctl row, Phase 3 entry, for the full writeup — flagged as a
// genuine design question for the praetor, not decided here): a plain
// `git clone` of this repo checks out `master`, the GitHub default branch,
// which is MISSING ./bootstrap, CLAUDE.md, DASHBOARD.md, scripts/agent-bus,
// .starfleet-ai/bin/starfleetctl, and every other piece of fleet tooling — all of it
// lives only on `mtx/agent-config` (the maintainer's personal staging
// branch, per CLAUDE.md's licensing-policy section: not auto-merged to
// master, promotion is the maintainer's own deliberate call). So "fully
// automatic bootstrap from a truly fresh clone" is not achievable without
// either generalizing that content onto master (a decision this package
// does not make) or a human/agent already knowing to `git checkout
// mtx/agent-config` first. Everything in this package assumes that has
// already happened — it starts from "you're on mtx/agent-config" and
// handles idempotent (re-)initialization from there.
package bootstrap

import (
	"path/filepath"
)

// Bootstrap holds one invocation's resolved locations.
type Bootstrap struct {
	Root         string // mpbt-workspace root
	SettingsFile string // .claude/settings.json
}

func New(root string) *Bootstrap {
	return &Bootstrap{
		Root:         root,
		SettingsFile: filepath.Join(root, ".claude", "settings.json"),
	}
}
