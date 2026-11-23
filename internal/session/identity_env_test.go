// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package session

import (
	"strings"
	"testing"
)

func TestShipProcessEnvDefiantStartsVoyager(t *testing.T) {
	base := []string{
		"PATH=/bin",
		"STARFLEET_SHIP_ID=Defiant",
		"STARFLEET_ROLE=ship",
		"STARFLEET_TARGET=Enterprise",
		"STARFLEET_LAUNCH_TYPE=terminal",
		"STARFLEET_CLASS=cruiser",
		"STARFLEET_BUS_DIR=/tmp/fleet-bus",
	}
	got := shipProcessEnv(base, "Voyager", "ship", "Enterprise", "background", "scout")
	values := map[string][]string{}
	for _, kv := range got {
		k, v, ok := strings.Cut(kv, "=")
		if ok && strings.HasPrefix(k, "STARFLEET_") {
			values[k] = append(values[k], v)
		}
	}
	want := map[string]string{
		"STARFLEET_SHIP_ID": "Voyager",
		"STARFLEET_ROLE": "ship",
		"STARFLEET_TARGET": "Enterprise",
		"STARFLEET_LAUNCH_TYPE": "background",
		"STARFLEET_CLASS": "scout",
		"STARFLEET_BUS_DIR": "/tmp/fleet-bus",
	}
	for key, value := range want {
		if len(values[key]) != 1 || values[key][0] != value {
			t.Errorf("%s = %v, want exactly [%q]", key, values[key], value)
		}
	}
}

func TestShipProcessEnvClearsStaleOptionalIdentity(t *testing.T) {
	base := []string{
		"STARFLEET_TARGET=Enterprise",
		"STARFLEET_CLASS=cruiser",
		"STARFLEET_SHIP_ID=Defiant",
		"STARFLEET_ROLE=ship",
		"STARFLEET_LAUNCH_TYPE=background",
	}
	got := shipProcessEnv(base, "Enterprise", "flagship", "", "terminal", "")
	for _, kv := range got {
		if strings.HasPrefix(kv, "STARFLEET_TARGET=") || strings.HasPrefix(kv, "STARFLEET_CLASS=") {
			t.Errorf("stale optional identity leaked: %s", kv)
		}
	}
}
