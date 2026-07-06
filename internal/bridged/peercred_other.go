// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

//go:build !linux

package bridged

import "net"

// peerUIDMatchesSelf has no non-Linux implementation yet (macOS/BSD use
// LOCAL_PEERCRED/getpeereid, not SO_PEERCRED) — always allow, so the socket
// file's 0600 mode remains the only enforcement on those platforms. This
// codebase's fleet-control tooling runs on Linux in practice; documenting
// the gap explicitly rather than silently no-op'ing without a trace.
func peerUIDMatchesSelf(_ *net.UnixConn) (bool, error) {
	return true, nil
}
