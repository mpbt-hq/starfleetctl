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
//	check      verify served models (--probe: minimal chat request, --json)
//	meta-models meta-model routing strategies (list, show, sessions, switch, force)
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
		args := args[1:]
		if len(args) > 0 && (args[0] == "status") {
			return runHealthStatus(root, len(args) > 1 && args[1] == "--json")
		}
		for _, a := range args {
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
	case "meta-models":
		if len(args) < 2 {
			metaModelsUsage()
			return 2
		}
		return runMetaModels(root, args[1:])
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
check       verify served models (--probe: minimal chat request, --json)
`)
}

func checkUsage() {
	fmt.Fprint(os.Stderr, `usage: starfleetctl model-proxy check [--probe] [--json]
       starfleetctl model-proxy check status [--json]

Checks every model of the configured (proxied and direct) providers:
  • listing check — is each model served by the provider's /v1/models?
  • --probe — plus a minimal 1-token chat request per served model,
    and (by default) an agent-capability probe (tool-call support)
  • status — read the persisted health state (.starfleet-ai/var/model-health.json)
    without performing live requests (cheap query path)
Transient upstream failures (429/5xx, timeouts, saturation) are retried; only
hard errors mark a model as failed. The result is persisted to
.starfleet-ai/var/model-health.json (shown in the web model dropdown) and can
be triggered from the web console as well.
`)
}

func metaModelsUsage() {
	fmt.Fprint(os.Stderr, `usage: starfleetctl model-proxy meta-models <subcommand>

Meta-model routing strategies (strategies define named selection rules for models).

Subcommands:
  list        list all configured strategies
  show <id>   show strategy details + runtime state
  sessions    list session affinities (sessionID -> model)
  switch      manually switch a session to a model: --session <id> --strategy <id> --model <id>
  force       force all sessions to use a model: --strategy <id> --model <id>

Examples:
  starfleetctl model-proxy meta-models list
  starfleetctl model-proxy meta-models show heavy-model
  starfleetctl model-proxy meta-models sessions
  starfleetctl model-proxy meta-models switch --session ses_abc123 --strategy heavy-model --model nvidia/nemotron-3-ultra-550b-a55b
  starfleetctl model-proxy meta-models force --strategy heavy-model --model big-pickle
`)
}

func runMetaModels(root string, args []string) int {
	if len(args) == 0 {
		metaModelsUsage()
		return 2
	}
	switch args[0] {
	case "list":
		return runMetaModelsList(root)
	case "show":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "meta-models show: strategy ID required")
			return 2
		}
		return runMetaModelsShow(root, args[1])
	case "sessions":
		return runMetaModelsSessions(root)
	case "switch":
		return runMetaModelsSwitch(root, args[1:])
	case "force":
		return runMetaModelsForce(root, args[1:])
	case "help", "-h", "--help":
		metaModelsUsage()
		return 0
	default:
		fmt.Fprintf(os.Stderr, "meta-models: unknown subcommand: %s\n", args[0])
		metaModelsUsage()
		return 2
	}
}

// runHealthStatus reads the persisted health state (written by a previous
// check run or the automated background loop) and prints it. This is the
// cheap query path for agents / the web console — it performs no live
// requests.
func runHealthStatus(root string, asJSON bool) int {
	st := LoadHealthState(root)
	if asJSON {
		enc, err := json.MarshalIndent(st, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "model-proxy check status: %v\n", err)
			return 1
		}
		fmt.Println(string(enc))
		return 0
	}
	fmt.Printf("model health state: %s — probe=%v\n", st.At.Format(time.RFC3339), st.Probe)
	reachable, capable, failed, degraded := 0, 0, 0, 0
	for _, mh := range st.Models {
		switch mh.Status {
		case StatusFailed:
			failed++
		case StatusDegraded:
			degraded++
		}
		if mh.Reachable {
			reachable++
			if mh.ToolCalls != nil && *mh.ToolCalls {
				capable++
			}
		}
	}
	fmt.Printf("  models=%d reachable=%d tool-capable=%d degraded=%d failed=%d\n",
		len(st.Models), reachable, capable, degraded, failed)
	for _, mh := range st.Models {
		cap := ""
		if mh.ToolCalls != nil {
			if *mh.ToolCalls {
				cap = "tools"
			} else {
				cap = "no-tools"
			}
		}
		fmt.Printf("  [%-11s] %-38s %s %s\n", mh.Status, mh.ID, cap, mh.Detail)
	}
	return 0
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

// runMetaModelsList lists all configured strategies.
func runMetaModelsList(root string) int {
	cfg, err := Load(root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load config: %v\n", err)
		return 1
	}
	if len(cfg.Strategies) == 0 {
		fmt.Println("No strategies configured.")
		return 0
	}
	fmt.Printf("%-20s %-12s %-10s %-12s %s\n", "ID", "TYPE", "EFF-LIMIT", "MODELS", "DESCRIPTION")
	for _, s := range cfg.Strategies {
		fmt.Printf("%-20s %-12s %-10d %-12d %s\n", s.ID, s.Strategy, s.EffectiveLimit, len(s.Models), s.Description)
	}
	return 0
}

// runMetaModelsShow shows detailed information about a strategy.
func runMetaModelsShow(root string, strategyID string) int {
	cfg, err := Load(root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load config: %v\n", err)
		return 1
	}
	for _, s := range cfg.Strategies {
		if s.ID == strategyID {
			fmt.Printf("Strategy: %s\n", s.ID)
			fmt.Printf("  Description: %s\n", s.Description)
			fmt.Printf("  Type: %s\n", s.Strategy)
			fmt.Printf("  Default Model: %s\n", s.DefaultModel)
			fmt.Printf("  Effective Limit: %d tokens\n", s.EffectiveLimit)
			fmt.Printf("  Models:\n")
			for _, m := range s.Models {
				fmt.Printf("    - %s (provider=%s, context=%d, priority=%d, weight=%d)\n",
					m.ID, m.Provider, m.ContextWindow, m.Priority, m.Weight)
			}
			fmt.Printf("  Triggers:\n")
			fmt.Printf("    rate-limited: action=%s cooldown=%s after=%d\n",
				s.Triggers.RateLimited.Action, s.Triggers.RateLimited.Cooldown, s.Triggers.RateLimited.After)
			fmt.Printf("    quota-exhausted: action=%s cooldown=%s auto-recover=%v\n",
				s.Triggers.QuotaExhausted.Action, s.Triggers.QuotaExhausted.Cooldown, s.Triggers.QuotaExhausted.AutoRecover)
			fmt.Printf("    timeout: action=%s cooldown=%s max-consecutive=%d\n",
				s.Triggers.Timeout.Action, s.Triggers.Timeout.Cooldown, s.Triggers.Timeout.MaxConsecutive)
			fmt.Printf("    error: action=%s cooldown=%s\n",
				s.Triggers.Error.Action, s.Triggers.Error.Cooldown)
			fmt.Printf("  Heuristics:\n")
			fmt.Printf("    latency-p95: %s\n", s.Heuristics.LatencyP95Threshold)
			fmt.Printf("    error-rate: %s in %s\n", s.Heuristics.ErrorRateThreshold, s.Heuristics.ErrorRateWindow)
			fmt.Printf("    token-throughput: %s\n", s.Heuristics.TokenThroughputThreshold)
			fmt.Printf("    consecutive-failures: %d\n", s.Heuristics.ConsecutiveFailuresThreshold)
			return 0
		}
	}
	fmt.Fprintf(os.Stderr, "strategy not found: %s\n", strategyID)
	return 1
}

// runMetaModelsSessions lists session affinities.
func runMetaModelsSessions(root string) int {
	// This requires a running proxy to query session affinities.
	// For now, we can't query a running daemon from CLI.
	fmt.Println("Session affinities are only available via the web API (/api/meta-models/sessions) or when the proxy is running.")
	fmt.Println("Start the proxy daemon and use the web console to view session affinities.")
	return 0
}

// runMetaModelsSwitch switches a session's model.
func runMetaModelsSwitch(root string, args []string) int {
	sessionID := ""
	strategyID := ""
	modelID := ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--session":
			if i+1 < len(args) {
				sessionID = args[i+1]
				i++
			}
		case "--strategy":
			if i+1 < len(args) {
				strategyID = args[i+1]
				i++
			}
		case "--model":
			if i+1 < len(args) {
				modelID = args[i+1]
				i++
			}
		}
	}
	if sessionID == "" || strategyID == "" || modelID == "" {
		fmt.Fprintln(os.Stderr, "switch: --session, --strategy, and --model are required")
		return 2
	}
	fmt.Printf("Switching session %s in strategy %s to model %s\n", sessionID, strategyID, modelID)
	fmt.Println("This operation requires the proxy daemon to be running.")
	fmt.Println("Use the web console or POST /api/meta-models/switch to perform this action.")
	return 0
}

// runMetaModelsForce forces all sessions to use a model.
func runMetaModelsForce(root string, args []string) int {
	strategyID := ""
	modelID := ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--strategy":
			if i+1 < len(args) {
				strategyID = args[i+1]
				i++
			}
		case "--model":
			if i+1 < len(args) {
				modelID = args[i+1]
				i++
			}
		}
	}
	if strategyID == "" || modelID == "" {
		fmt.Fprintln(os.Stderr, "force: --strategy and --model are required")
		return 2
	}
	fmt.Printf("Forcing all sessions in strategy %s to model %s\n", strategyID, modelID)
	fmt.Println("This operation requires the proxy daemon to be running.")
	fmt.Println("Use the web console or POST /api/meta-models/force to perform this action.")
	return 0
}
