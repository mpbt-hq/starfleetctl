// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
//
// DoInstallSelf is the mechanism behind the "starfleetctl carries its own
// instructions" design (praetor directive m0089, 2026-07-06): a consuming
// workspace's AGENTS.md should only need to know how to fetch/build
// starfleetctl and how to pull the actual usage instructions FROM it — not
// hand-duplicate and separately maintain a copy of them. See the root
// package doc comment (doc.go) for the embedding mechanism.
package agents

import (
	"os"

	starfleetctl "github.com/metux/starfleetctl"
)

const SelfSlug = "starfleetctl"

func selfFragmentMeta(order int) FragmentMeta {
	return FragmentMeta{
		Slug:  SelfSlug,
		Title: "starfleetctl — fleet-management CLI (auto-installed by `agents install-self`, do not hand-edit)",
		Order: order,
		Owner: "starfleetctl",
	}
}

// RenderSelfFragment returns exactly the bytes DoInstallSelf would write,
// without touching disk — lets a caller (bootstrap's verifySelfFragment)
// check whether an existing agents.d/starfleetctl.md is stale relative to
// the currently-running binary before deciding whether a fix is needed.
func RenderSelfFragment(order int) ([]byte, error) {
	return renderFragmentFile(selfFragmentMeta(order), starfleetctl.Readme), nil
}

// DoInstallSelf writes agents.d/starfleetctl.md from this binary's own
// embedded README.md, then reindexes. Unlike DoNew, this ALWAYS overwrites
// — the fragment is tool-owned (Owner: "starfleetctl"), meant to always
// mirror whatever starfleetctl commit is actually checked out; hand-editing
// it is not supported (it would just be clobbered on the next
// install-self, e.g. from `bootstrap --fix` after an update).
func (a *Agents) DoInstallSelf(order int) error {
	if err := os.MkdirAll(a.FragmentsDir(), 0o755); err != nil {
		return err
	}
	if err := writeFragmentFile(a.fragmentPath(SelfSlug), selfFragmentMeta(order), starfleetctl.Readme); err != nil {
		return err
	}
	return a.DoReindex()
}
