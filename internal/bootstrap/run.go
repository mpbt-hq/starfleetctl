// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package bootstrap

import (
	"fmt"
	"os"
)

const usage = `starfleetctl bootstrap [--fix]

Idempotent self-check (and, with --fix, self-repair) of the
fleet-management-specific one-time setup this tool depends on:
the agent-bus + agent-claims directory tree, and the
starfleetctl-related .claude/settings.json allowlist entries.

Without --fix: report-only, exits 1 if anything is missing.
With --fix: repairs whatever it safely can, exits 0 unless something
that isn't auto-fixable is still missing. (.starfleet-ai/etc/ship-names.txt
is auto-fixable too — refilled from the embedded template if missing/empty.)

This does NOT set up the broader mpbt build system (mpbt-builder,
project sources, etc.) — see ./bootstrap at the workspace root for that;
run this AFTER it, once starfleetctl itself is built.
`

// Run dispatches a `bootstrap` invocation. Returns the process exit code.
func Run(root string, args []string) int {
	fix := false
	for _, a := range args {
		switch a {
		case "-h", "--help":
			fmt.Print(usage)
			return 0
		case "--fix":
			fix = true
		default:
			fmt.Fprintf(os.Stderr, "bootstrap: unknown option: %s\n", a)
			return 2
		}
	}

	b := New(root)
	allOK := true
	for _, c := range Checks() {
		ok, detail := c.Verify(b)
		if ok {
			fmt.Printf("[ok]      %s — %s\n", c.Name, detail)
			continue
		}
		if fix && c.Fix != nil {
			if err := c.Fix(b); err != nil {
				fmt.Printf("[FAILED]  %s — fix attempted, error: %v\n", c.Name, err)
				allOK = false
				continue
			}
			// Re-verify after fixing rather than assuming success.
			ok2, detail2 := c.Verify(b)
			if ok2 {
				fmt.Printf("[fixed]   %s — %s\n", c.Name, detail2)
			} else {
				fmt.Printf("[FAILED]  %s — fix ran but still not satisfied: %s\n", c.Name, detail2)
				allOK = false
			}
			continue
		}
		suffix := " (rerun with --fix)"
		if c.Fix == nil {
			suffix = ""
		}
		fmt.Printf("[missing] %s — %s%s\n", c.Name, detail, suffix)
		allOK = false
	}

	if allOK {
		fmt.Println("bootstrap: all checks passed")
		return 0
	}
	return 1
}
