// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
//
// Go port of scripts/pr-view: print PR metadata via `gh pr view --json
// <fields>` as one canonical, allowlistable command — same rationale as the
// bash original (avoid the Bash(gh)/Bash(gh *) prefix-matching gotcha from
// chaining an `echo`/`export` in front of the real gh call).
package ghpr

import (
	"fmt"
	"os"
)

const prViewUsage = `usage: starfleetctl pr-view <pr#> [json-fields]
  default fields: number,title,state
env:
  REPO   repo slug (default X11Libre/xserver)
`

// RunPRView implements `starfleetctl pr-view <pr#> [json-fields]`.
func RunPRView(args []string) int {
	if len(args) >= 1 && (args[0] == "-h" || args[0] == "--help") {
		fmt.Print(prViewUsage)
		return 0
	}
	if len(args) < 1 {
		fmt.Fprint(os.Stderr, prViewUsage)
		return 2
	}
	pr := args[0]
	fields := "number,title,state"
	if len(args) >= 2 {
		fields = args[1]
	}

	out, err := runGH("pr", "view", pr, "--repo", repo(), "--json", fields)
	if err != nil {
		fprintErr("pr-view", err)
		return 1
	}
	os.Stdout.Write(out)
	if len(out) == 0 || out[len(out)-1] != '\n' {
		fmt.Println()
	}
	return 0
}
