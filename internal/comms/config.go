// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package comms

import (
	"encoding/json"
	"fmt"

	"github.com/metux/starfleetctl/internal/config"
)

// DoConfig implements `comms config` — returns the plugin-relevant
// configuration as JSON so the opencode plugin can read tuning knobs
// (heartbeat interval, poll interval, etc.) instead of hardcoding them.
func (b *Bus) DoConfig() error {
	cfg, err := config.Load(b.Root)
	if err != nil {
		return err
	}
	out := struct {
		HeartbeatMS   int    `json:"heartbeat_ms"`
		PollMS        int    `json:"poll_ms"`
		FallbackModel string `json:"fallback_model"`
	}{
		HeartbeatMS:   cfg.Comms.HeartbeatMS,
		PollMS:        cfg.Comms.PollMS,
		FallbackModel: cfg.Comms.FallbackModel,
	}
	data, err := json.Marshal(out)
	if err != nil {
		return err
	}
	fmt.Println(string(data))
	return nil
}
