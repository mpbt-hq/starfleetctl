// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
//
// System timer commands — workspace-level operations executed directly in the
// timer worker process, without going through the comms bus or any agent.

package timer

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/metux/starfleetctl/internal/dashboard"
	"github.com/metux/starfleetctl/internal/sop"
)

// runSystemCommand dispatches a system timer command by verb.
// cmd is the full command line (e.g. ["web", "restart"]).
func runSystemCommand(root string, cmd []string) error {
	if len(cmd) == 0 {
		return fmt.Errorf("system command: empty cmd")
	}
	verb := strings.ToLower(cmd[0])
	args := cmd[1:]

	switch verb {
	case "reindex":
		return runReindex(root)
	case "web":
		return runWeb(root, args)
	default:
		return fmt.Errorf("system command: unknown verb: %s", verb)
	}
}

// runReindex refreshes both the SOP instructions index and the dashboard index.
func runReindex(root string) error {
	a, err := sop.New(root)
	if err != nil {
		return fmt.Errorf("reindex sop: %w", err)
	}
	if err := a.DoReindex(); err != nil {
		return fmt.Errorf("reindex sop: %w", err)
	}
	d, err := dashboard.New(root)
	if err != nil {
		return fmt.Errorf("reindex dashboard: %w", err)
	}
	if err := d.DoReindex(); err != nil {
		return fmt.Errorf("reindex dashboard: %w", err)
	}
	return nil
}

// runWeb starts or restarts the web server by shelling out to starfleetctl.
// This avoids an import cycle (timer → web → timer) while keeping the
// operation in the same workspace.
// With no args or "start": autostart (idempotent — skips if already running).
// With "restart" or "force": stop + start (always restarts).
func runWeb(root string, args []string) error {
	force := false
	for _, a := range args {
		if strings.EqualFold(a, "restart") || strings.EqualFold(a, "force") {
			force = true
		}
	}
	cmd := "autostart"
	if force {
		cmd = "restart"
	}
	// Use starfleetctl binary from PATH (installed by bootstrap).
	c := exec.Command("starfleetctl", "web", cmd)
	c.Dir = root
	out, err := c.CombinedOutput()
	if err != nil {
		return fmt.Errorf("web %s: %w (output: %s)", cmd, err, strings.TrimSpace(string(out)))
	}
	return nil
}
