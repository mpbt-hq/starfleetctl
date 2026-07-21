// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package bridged

import (
	"net"
	"os"
	"syscall"
)

// peerUIDMatchesSelf verifies the connecting process's UID matches the
// daemon's own, via SO_PEERCRED (stdlib syscall package, no extra
// dependency — consistent with this codebase's stdlib-only convention).
// Defense in depth on top of the socket file's 0600 mode: the same trust
// boundary the file-based model already has today (anyone who can read/
// write ../agent-bus/* has full access regardless), just enforced at
// the connection layer too. SO_PEERCRED is Unix-socket/Linux-specific and
// won't carry over to a future TCP transport — that gap needs its own
// solution (e.g. a shared token) whenever cross-host support is actually
// scoped, not pretended away now.
func peerUIDMatchesSelf(uc *net.UnixConn) (bool, error) {
	raw, err := uc.SyscallConn()
	if err != nil {
		return false, err
	}
	var ucred *syscall.Ucred
	var sockErr error
	ctrlErr := raw.Control(func(fd uintptr) {
		ucred, sockErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	})
	if ctrlErr != nil {
		return false, ctrlErr
	}
	if sockErr != nil {
		return false, sockErr
	}
	return ucred.Uid == uint32(os.Getuid()), nil
}
