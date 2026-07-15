// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package shipnames

import (
	"path/filepath"
	"time"

	"github.com/metux/starfleetctl/internal/flock"
)

// assignLock mirrors bash's `flock -w 10 9` — a fixed 10s wait, not
// configurable (unlike LOCK_WAIT elsewhere), matching scripts/ship-names.
// Only `assign` locks; release/list/gc/flagship are unlocked in the bash
// original too.
func (r *Registry) assignLock() (*flock.Handle, error) {
	path := filepath.Join(r.ShipsDir, ".assign.lock")
	return flock.Lock(path, flock.Options{Timeout: 10 * time.Second})
}
