// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package prclaim

import (
	"path/filepath"
	"time"

	"github.com/metux/starfleetctl/internal/flock"
)

func (c *Claims) lockRegistry() (*flock.Handle, error) {
	path := filepath.Join(c.ClaimDir, ".lock")
	return flock.Lock(path, flock.Options{Timeout: 600 * time.Second})
}
