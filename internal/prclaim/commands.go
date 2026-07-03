// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package prclaim

import (
	"fmt"
	"os"
)

// DoList implements `pr-claim --list`.
func (c *Claims) DoList() error {
	claims := c.allClaims()
	if len(claims) == 0 {
		fmt.Println("(no active claims)")
		return nil
	}
	fmt.Printf("%-6s  %-24s  %-7s  %s\n", "PR", "AGENT", "AGE", "WHAT")
	for _, r := range claims {
		mark := ""
		if c.stale(r.Epoch) {
			mark = " [STALE]"
		}
		fmt.Printf("%-6s  %-24s  %-7s  %s%s\n", r.PR, r.Agent, age(r.Epoch), r.Note, mark)
	}
	return nil
}

// DoClaim implements `pr-claim <pr#> [note]` and `--steal <pr#> [note]`
// (force=true). Returns exitError(3) when the PR is actively held by
// another agent.
func (c *Claims) DoClaim(pr, note string, force bool) error {
	c.warnID()
	lh, err := c.lockRegistry()
	if err != nil {
		return err
	}
	defer lh.Close()

	if r, ok := c.readClaim(pr); ok {
		switch {
		case r.Agent == c.AgentID:
			if err := c.writeClaim(pr, note); err != nil {
				return err
			}
			c.logEvent("refresh", fmt.Sprintf("pr#%s %s", pr, note))
			fmt.Printf("pr-claim: refreshed your claim on PR #%s\n", pr)
			return nil
		case c.stale(r.Epoch):
			fmt.Fprintf(os.Stderr, "pr-claim: reclaiming STALE claim on PR #%s (was '%s', age %s)\n", pr, r.Agent, age(r.Epoch))
			if err := c.writeClaim(pr, note); err != nil {
				return err
			}
			c.logEvent("reclaim-stale", fmt.Sprintf("pr#%s from %s", pr, r.Agent))
			return nil
		case force:
			fmt.Fprintf(os.Stderr, "pr-claim: STEALING PR #%s from '%s' (age %s)\n", pr, r.Agent, age(r.Epoch))
			if err := c.writeClaim(pr, note); err != nil {
				return err
			}
			c.logEvent("steal", fmt.Sprintf("pr#%s from %s", pr, r.Agent))
			return nil
		default:
			fmt.Fprintf(os.Stderr, "pr-claim: PR #%s is held by '%s' (age %s): %s\n", pr, r.Agent, age(r.Epoch), r.Note)
			fmt.Fprintln(os.Stderr, "pr-claim: pick another PR, coordinate, or --steal if that agent is gone.")
			return exitError(3)
		}
	}

	if err := c.writeClaim(pr, note); err != nil {
		return err
	}
	c.logEvent("claim", fmt.Sprintf("pr#%s %s", pr, note))
	fmt.Printf("pr-claim: claimed PR #%s for '%s'\n", pr, c.AgentID)
	return nil
}

// DoRelease implements `pr-claim --release <pr#>`. Set force=true to mirror
// bash's FORCE=1 env override (release someone else's claim).
func (c *Claims) DoRelease(pr string, force bool) error {
	lh, err := c.lockRegistry()
	if err != nil {
		return err
	}
	defer lh.Close()

	r, ok := c.readClaim(pr)
	if !ok {
		fmt.Printf("pr-claim: no active claim on PR #%s\n", pr)
		return nil
	}
	if r.Agent != c.AgentID && !force {
		fmt.Fprintf(os.Stderr, "pr-claim: PR #%s is held by '%s', not you ('%s'). Use FORCE=1 to override.\n", pr, r.Agent, c.AgentID)
		return exitError(3)
	}
	if err := c.removeClaim(pr); err != nil {
		return err
	}
	c.logEvent("release", fmt.Sprintf("pr#%s", pr))
	fmt.Printf("pr-claim: released PR #%s\n", pr)
	return nil
}

// DoReleaseAll implements `pr-claim --release-all`.
func (c *Claims) DoReleaseAll() error {
	lh, err := c.lockRegistry()
	if err != nil {
		return err
	}
	defer lh.Close()

	cnt := 0
	for _, r := range c.allClaims() {
		if r.Agent == c.AgentID {
			if err := c.removeClaim(r.PR); err == nil {
				cnt++
			}
		}
	}
	c.logEvent("release-all", fmt.Sprintf("%d claims", cnt))
	fmt.Printf("pr-claim: released %d claim(s) held by '%s'\n", cnt, c.AgentID)
	return nil
}

// DoWho implements `pr-claim --who <pr#>`: exit 0 if free/ours/stale, else
// print the holder and return exitError(3) — used by pr-checkout's soft
// warning.
func (c *Claims) DoWho(pr string) error {
	r, ok := c.readClaim(pr)
	if !ok {
		return nil
	}
	if r.Agent == c.AgentID || c.stale(r.Epoch) {
		return nil
	}
	fmt.Printf("%s (age %s): %s\n", r.Agent, age(r.Epoch), r.Note)
	return exitError(3)
}

// exitError carries a desired process exit code with no extra message
// (the message, if any, was already printed to stderr by the caller —
// matching the bash functions' `echo ... >&2; return 3` shape).
type exitError int

func (e exitError) Error() string { return "" }
func (e exitError) Code() int     { return int(e) }
