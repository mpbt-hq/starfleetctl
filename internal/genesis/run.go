// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package genesis

import (
	"fmt"
	"os"
	"path/filepath"
)

const usage = `starfleetctl genesis-init [dir]

Bootstraps a workspace that has NOTHING fleet-related yet — no AGENTS.md, no
cf/starfleetctl/, no scripts/starfleetctl — from just this one already-built
starfleetctl binary. Writes the small set of project-independent files that
wire starfleetctl in as an mpbt solution (cf/starfleetctl/*,
run-fetch.starfleetctl, run-build.starfleetctl, scripts/starfleetctl,
scripts/ship-names.txt), skipping any that already exist, then runs the same
checks as 'bootstrap --fix' (AGENTS.md + agents.d/index.md, DASHBOARD.md,
_WORK_ directory tree, .claude/settings.json allowlist entries, the
starfleetctl self-fragment).

dir defaults to the current directory. Deliberately does NOT use the normal
AGENTS.md+scripts/ workspace-root discovery — that's exactly what doesn't
exist yet here.
`

// Run dispatches a `genesis-init` invocation. Returns the process exit code.
func Run(args []string) int {
	dir := "."
	for _, a := range args {
		switch a {
		case "-h", "--help":
			fmt.Print(usage)
			return 0
		default:
			if len(a) > 0 && a[0] == '-' {
				fmt.Fprintf(os.Stderr, "genesis-init: unknown option: %s\n", a)
				return 2
			}
			dir = a
		}
	}

	root, err := filepath.Abs(dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "genesis-init:", err)
		return 1
	}

	created, err := Init(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "genesis-init:", err)
		return 1
	}
	if len(created) == 0 {
		fmt.Println("genesis-init: nothing to do — every template file already present")
	} else {
		fmt.Println("genesis-init: wrote:")
		for _, c := range created {
			fmt.Printf("  %s\n", c)
		}
	}
	fmt.Println("genesis-init: done — fleet-management setup (AGENTS.md/agents.d, DASHBOARD.md, _WORK_ dirs, allowlist, self-fragment) is now in place")
	return 0
}
