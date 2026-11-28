// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package session

import (
	"bufio"
	"fmt"
	"net"
	"path/filepath"
	"testing"
)

func controlTestServer(t *testing.T, response string, wantCommand string) string {
	t.Helper()
	socketPath := filepath.Join(t.TempDir(), "ship.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		line, err := bufio.NewReader(conn).ReadString('\n')
		if err == nil && line != wantCommand {
			_, _ = fmt.Fprintf(conn, "ERROR unexpected command %q\n", line)
			return
		}
		if response != "" {
			_, _ = fmt.Fprintln(conn, response)
		}
	}()
	return socketPath
}

func TestSendShipControlCommand(t *testing.T) {
	t.Run("acknowledgement", func(t *testing.T) {
		socket := controlTestServer(t, "OK model=provider/model pid=4321", "RESTART model=provider/model\n")
		got, err := sendShipControlCommand(socket, "RESTART model=provider/model")
		if err != nil {
			t.Fatalf("sendShipControlCommand: %v", err)
		}
		if got != "OK model=provider/model pid=4321" {
			t.Fatalf("response = %q, want model-bearing acknowledgement", got)
		}
		if err := validateRestartAcknowledgement(got); err != nil { t.Fatalf("valid acknowledgement rejected: %v", err) }
	})

	t.Run("supervisor rejection is returned", func(t *testing.T) {
		socket := controlTestServer(t, "ERROR model unavailable", "RESTART\n")
		got, err := sendShipControlCommand(socket, "RESTART")
		if err != nil {
			t.Fatalf("sendShipControlCommand: %v", err)
		}
		if got != "ERROR model unavailable" {
			t.Fatalf("response = %q, want supervisor error", got)
		}
	})

	t.Run("empty response is an error", func(t *testing.T) {
		socket := controlTestServer(t, "", "RESTART\n")
		if _, err := sendShipControlCommand(socket, "RESTART"); err == nil {
			t.Fatal("sendShipControlCommand succeeded without an acknowledgement")
		}
	})
}

func TestValidateRestartAcknowledgementRequiresEffectiveModelAndPID(t *testing.T) {
 for _,response:=range []string{"OK","OK model= pid=42","OK model=provider/model pid=0","OK model=provider/model pid=nope","ERROR model unavailable"} {
  t.Run(response,func(t *testing.T){if err:=validateRestartAcknowledgement(response);err==nil{t.Fatalf("accepted invalid acknowledgement %q",response)}})
 }
}
