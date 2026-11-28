// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package session

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"strings"
	"strconv"
	"time"
)

const (
	shipControlDialTimeout = 2 * time.Second
	shipControlIOTimeout   = 135 * time.Second
)

// sendShipControlCommand sends one line to a ship supervisor and waits for its
// newline-terminated response. It deliberately fails closed on write, read,
// and timeout errors: callers must not report a restart as successful merely
// because the command may have reached the socket.
func sendShipControlCommand(socketPath, command string) (string, error) {
	conn, err := net.DialTimeout("unix", socketPath, shipControlDialTimeout)
	if err != nil {
		return "", fmt.Errorf("connect to ship control socket %s: %w", socketPath, err)
	}
	defer conn.Close()

	if err := conn.SetDeadline(time.Now().Add(shipControlIOTimeout)); err != nil {
		return "", fmt.Errorf("set ship control socket deadline: %w", err)
	}
	if _, err := io.WriteString(conn, strings.TrimRight(command, "\r\n")+"\n"); err != nil {
		return "", fmt.Errorf("write ship control command: %w", err)
	}

	response, err := bufio.NewReaderSize(conn, 512).ReadString('\n')
	if err != nil {
		return "", fmt.Errorf("read ship control response: %w", err)
	}
	return strings.TrimSpace(response), nil
}


func validateRestartAcknowledgement(response string) error {
 fields:=strings.Fields(response)
 if len(fields)!=3 || fields[0]!="OK" || !strings.HasPrefix(fields[1],"model=") || strings.TrimPrefix(fields[1],"model=")=="" || !strings.HasPrefix(fields[2],"pid=") {
  return fmt.Errorf("invalid positive restart acknowledgement %q",response)
 }
 pid,err:=strconv.Atoi(strings.TrimPrefix(fields[2],"pid="))
 if err!=nil||pid<=0{return fmt.Errorf("invalid positive restart acknowledgement %q",response)}
 return nil
}

