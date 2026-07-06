// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package bridged

import (
	"net"
	"time"
)

// Call dials sockPath, sends req, reads back one Response, and closes the
// connection — the client half of the one-request-per-connection protocol.
func Call(sockPath string, req Request, timeout time.Duration) (*Response, error) {
	if err := validateSockPath(sockPath); err != nil {
		return nil, err
	}
	conn, err := net.DialTimeout("unix", sockPath, timeout)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		return nil, err
	}
	if err := writeFrame(conn, req); err != nil {
		return nil, err
	}
	var resp Response
	if err := readFrame(conn, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}
