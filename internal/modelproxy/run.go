// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package modelproxy

import (
	"fmt"
	"os"
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
	case "help", "-h", "--help":
		usage()
		return 0
	default:
		usage()
		return 2
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: starfleetctl model-proxy <start|stop|restart|autostart|status|models>")
}
