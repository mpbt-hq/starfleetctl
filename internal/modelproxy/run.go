// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package modelproxy

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// Run dispatches the `model-proxy` subcommand:
//
//	start      run the proxy in the foreground (daemon child / manual)
//	stop       stop the proxy daemon
//	restart    stop + autostart
//	autostart  ensure the proxy daemon is running (cron)
//	status     print running state + listen address
//	models     query the model catalog per configured provider (diagnostics)
func Run(root string, args []string) int {
	if len(args) == 0 {
		usage()
		return 2
	}
	switch args[0] {
	case "start":
		if err := Serve(root); err != nil {
			fmt.Fprintf(os.Stderr, "model-proxy start: %v\n", err)
			return 1
		}
		return 0
	case "stop":
		if err := Stop(root); err != nil {
			fmt.Fprintf(os.Stderr, "model-proxy stop: %v\n", err)
			return 1
		}
		return 0
	case "restart":
		if err := Restart(root); err != nil {
			fmt.Fprintf(os.Stderr, "model-proxy restart: %v\n", err)
			return 1
		}
		return 0
	case "autostart":
		ok, err := Autostart(root)
		if err != nil {
			fmt.Fprintf(os.Stderr, "model-proxy autostart: %v\n", err)
			return 1
		}
		if !ok {
			fmt.Fprintln(os.Stderr, "model-proxy autostart: could not verify daemon is listening")
			return 1
		}
		return 0
	case "status":
		if err := Status(root); err != nil {
			fmt.Fprintf(os.Stderr, "model-proxy status: %v\n", err)
			return 1
		}
		return 0
	case "models":
		if err := checkModels(root); err != nil {
			fmt.Fprintf(os.Stderr, "model-proxy models: %v\n", err)
			return 1
		}
		return 0
	case "check":
		probe := false
		asJSON := false
		for _, a := range args[1:] {
			switch a {
			case "--probe":
				probe = true
			case "--json":
				asJSON = true
			case "help", "-h", "--help":
				checkUsage()
				return 0
			default:
				fmt.Fprintf(os.Stderr, "model-proxy check: unknown option: %s\n", a)
				checkUsage()
				return 2
			}
		}
		if err := runHealthCheck(root, probe, asJSON); err != nil {
			fmt.Fprintf(os.Stderr, "model-proxy check: %v\n", err)
			return 1
		}
		return 0
	case "help", "-h", "--help":
		usage()
		return 0
	default:
		usage()
		return 2
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `usage: starfleetctl model-proxy <subcommand>

start       run the proxy in the foreground (daemon child / manual)
stop        stop the proxy daemon
restart     stop + autostart
autostart   ensure the proxy daemon is running (cron)
status      print running state + listen address
models      query the model catalog per configured provider (diagnostics)
check       verify models.yaml entries (--probe: minimal chat request, --json)
`)
}

func checkUsage() {
	fmt.Fprint(os.Stderr, `usage: starfleetctl model-proxy check [--probe] [--json]

Checks every active models.yaml entry of the proxied providers:
  • listing check — is each model served by the provider's /v1/models?
  • --probe — plus a minimal 1-token chat request per served model
Transient upstream failures (429/5xx, timeouts, saturation) are retried; only
hard errors mark a model as failed. The result is persisted to
.starfleet-ai/var/model-health.json (shown in the web model dropdown) and can
be triggered from the web console as well.
`)
}

// runHealthCheck runs the check, persists the state and prints the result
// (human-readable table or JSON).
func runHealthCheck(root string, probe, asJSON bool) error {
	report, err := RunCheck(root, probe)
	if err != nil {
		return err
	}
	state := &HealthState{}
	state.Merge(report)
	if err := WriteHealthState(root, state); err != nil {
		return fmt.Errorf("persist state: %w", err)
	}
	if asJSON {
		enc, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(enc))
		return nil
	}
	printCheckReport(report)
	return nil
}

// printCheckReport prints a per-provider table with status aggregates.
func printCheckReport(r *CheckReport) {
	fmt.Printf("model health check: %s — probe=%v\n", r.At.Format(time.RFC3339), r.Probe)
	fmt.Printf("  total=%d ok=%d not-served=%d degraded=%d failed=%d unknown=%d\n",
		r.Total, r.OK, r.NotServed, r.Degraded, r.Failed, r.Unknown)
	for _, mh := range r.Models {
		fmt.Printf("  [%-11s] %-38s %s\n", mh.Status, mh.ID, mh.Detail)
	}
}
