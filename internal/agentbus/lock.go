// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package agentbus

import (
	"path/filepath"
	"time"

	"github.com/metux/starfleetctl/internal/flock"
)

func (b *Bus) lockBus() (*flock.Handle, error) {
	path := filepath.Join(b.BusDir, ".lock")
	return flock.Lock(path, flock.Options{Timeout: 600 * time.Second})
}
