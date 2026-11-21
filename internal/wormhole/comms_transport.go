// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
//
// Wormhole as a Comms transport backend. Outbound messages selected by the
// Comms routing table are published through the normal durable Maildir spool;
// inbound messages enter Comms only through Bus.PostExternal.
package wormhole

import (
	"fmt"
	"strings"
	"time"

	"github.com/metux/starfleetctl/internal/comms"
	"github.com/metux/starfleetctl/internal/config"
)

// InstallCommsTransport registers the Wormhole transport with Comms. Call it
// once at process startup before any Comms send path runs.
func InstallCommsTransport() {
	comms.RegisterRemoteTransportWithValidator("wormhole", PublishRemoteMessage, ValidateRoute)
}

// ValidateRoute checks a Comms routing-table entry against the wormhole
// configuration: the named repository must exist, be enabled and be complete.
// Without this check a typo in the repository ID is only discovered per
// message, at publish time, and the message stays pending.
func ValidateRoute(root string, route comms.TransportRoute) error {
	if route.Repository == "" {
		return fmt.Errorf("route %q has no repository", route.Domain)
	}
	cfg, err := LoadConfig(root)
	if err != nil {
		return err
	}
	if !cfg.Enabled {
		return fmt.Errorf("wormhole transport is disabled")
	}
	for i := range cfg.Repositories {
		repo := cfg.Repositories[i]
		if repo.ID != route.Repository {
			continue
		}
		if !repoEnabled(repo) {
			return fmt.Errorf("repository %q is disabled", repo.ID)
		}
		if repo.Remote == "" || repo.Branch == "" {
			return fmt.Errorf("repository %q is incomplete", repo.ID)
		}
		return nil
	}
	return fmt.Errorf("unknown repository %q", route.Repository)
}

// PublishRemoteMessage publishes one Comms outbound message into the wormhole
// repository selected by the routing table. It returns the transport message
// ID (not a local Comms ID).
func PublishRemoteMessage(root string, route comms.TransportRoute, msg comms.OutboundMessage) (string, error) {
	if err := ValidateRoute(root, route); err != nil {
		return "", err
	}
	cfg, err := LoadConfig(root)
	if err != nil {
		return "", err
	}
	var repo *config.RepositoryConfig
	for i := range cfg.Repositories {
		if cfg.Repositories[i].ID == route.Repository {
			repo = &cfg.Repositories[i]
			break
		}
	}
	if repo == nil {
		return "", fmt.Errorf("wormhole: unknown repository %q", route.Repository)
	}
	if msg.From == "" || msg.Target == "" {
		return "", fmt.Errorf("wormhole: outbound message needs sender and recipient")
	}
	// Wormhole does not support attachments. The comms core uses an inline
	// pointer format [[attach:<name>:<size>:<sha256>]] for large payloads.
	// Reject such messages explicitly rather than silently losing the attachment.
	if strings.Contains(msg.Text, "[[attach:") {
		return "", fmt.Errorf("wormhole transport does not support attachments; use a transport that supports attachments or split the message")
	}

	cloneTimeout, _ := time.ParseDuration(cfg.Sync.CloneTimeout)
	commandTimeout, _ := time.ParseDuration(cfg.Sync.CommandTimeout)
	sp := newSpool(root, *repo, cfg.Sync.PushRetries, msg.From, cloneTimeout, commandTimeout)
	canonicalFrom, logicalSender, serr := canonicalSender(msg.From)
	if serr != nil {
		return "", serr
	}
	wm := NewMessage(canonicalFrom, msg.Target, msg.Type, msg.Text, msg.ReplyTo)
	if logicalSender != "" {
		wm.Sender = logicalSender
	}
	// If the caller provided a deterministic outbox ID, use it as the message ID.
	// This enables exactly-once semantics across retries.
	if msg.ID != "" {
		wm.ID = msg.ID
	}
	if err := sp.Publish(wm); err != nil {
		return "", err
	}
	return wm.ID, nil
}
