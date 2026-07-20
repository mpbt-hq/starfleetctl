// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package agentbus

import (
	"encoding/json"
	"fmt"

	"github.com/metux/starfleetctl/internal/config"
)

// DoConfig implements `agent-bus config` — returns the plugin-relevant
// configuration as JSON so the opencode plugin can read tuning knobs
// (heartbeat interval, poll interval, etc.) instead of hardcoding them.
func (b *Bus) DoConfig() error {
	cfg, err := config.Load(b.Root)
	if err != nil {
		return err
	}
	out := map[string]int{
		"heartbeat_ms": cfg.AgentBus.HeartbeatMS,
		"poll_ms":      cfg.AgentBus.PollMS,
	}
	data, err := json.Marshal(out)
	if err != nil {
		return err
	}
	fmt.Println(string(data))
	return nil
}
