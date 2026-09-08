// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
//
// Package services orchestrates the companion daemons (web server, model
// proxy, timer worker) from a single config source (services.yaml). The
// flagship (starfleetctl run --flagship) pulls up the configured services at
// session start, replacing hand-rolled cron autostart entries that can lose
// the user's shell environment.
package services

import (
	"fmt"
	"io"
	"os"

	"github.com/metux/starfleetctl/internal/config"
	"github.com/metux/starfleetctl/internal/modelproxy"
	"github.com/metux/starfleetctl/internal/timer"
	"github.com/metux/starfleetctl/internal/web"
)

// AutostartConfigured brings up every service listed in services.yaml
// (services.autostart), each with its own idempotent start logic (skip if
// already running). Errors are reported on the given writer and collected;
// one failing service does not prevent the others from starting.
func AutostartConfigured(root string, out io.Writer) error {
	cfg, err := config.Load(root)
	if err != nil {
		return fmt.Errorf("services: load config: %w", err)
	}
	if len(cfg.Services.Autostart) == 0 {
		return nil
	}
	var errs []error
	for _, name := range cfg.Services.Autostart {
		if err := startOne(root, name, out); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("services: %d service(s) failed to start: %v", len(errs), errs)
	}
	return nil
}

func startOne(root, name string, out io.Writer) error {
	switch name {
	case "web":
		ok, err := web.Autostart(root)
		if err == nil && ok {
			fmt.Fprintln(out, "services: web server running")
		} else if err == nil {
			fmt.Fprintln(out, "services: web server start failed")
		}
		return err
	case "model-proxy":
		ok, err := modelproxy.Autostart(root)
		if err == nil && ok {
			fmt.Fprintln(out, "services: model-proxy running")
		} else if err == nil {
			fmt.Fprintln(out, "services: model-proxy start failed")
		}
		return err
	case "timer":
		if running, _ := timer.WorkerStatus(root); running {
			fmt.Fprintln(out, "services: timer worker already running")
			return nil
		}
		if err := timer.StartWorker(root); err != nil {
			return fmt.Errorf("timer worker: %w", err)
		}
		fmt.Fprintln(out, "services: timer worker started")
		return nil
	default:
		return fmt.Errorf("unknown service %q (supported: web, model-proxy, timer)", name)
	}
}

// HasAutostart reports whether services.yaml lists any autostart services.
func HasAutostart(root string) bool {
	cfg, err := config.Load(root)
	if err != nil || len(cfg.Services.Autostart) == 0 {
		return false
	}
	return true
}

// FlagshipAutostart runs AutostartConfigured to stderr (daemon context logs
// should not pollute stdout script output). Convenience for callers.
func FlagshipAutostart(root string) error {
	return AutostartConfigured(root, os.Stderr)
}
