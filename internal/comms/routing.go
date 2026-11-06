// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
//
// Comms routing table: the single source of truth for deciding whether a
// recipient is local to this bus or has to be handed to a remote transport.
// Addresses keep the form user@domain; domainless recipients and the local
// domain are local. Domains are logical communication domains, not hostnames.
package comms

import (
	"fmt"
	"strings"
	"sync"

	"github.com/metux/starfleetctl/internal/config"
)

// TransportRoute is one resolved remote routing-table entry.
type TransportRoute struct {
	Domain     string
	Transport  string
	Repository string
}

// OutboundMessage is the transport-neutral payload handed to a remote
// transport through the normal Comms send path.
type OutboundMessage struct {
	From    string
	Target  string
	Text    string
	Type    string
	ReplyTo string
}

// RemoteTransportFunc publishes an outbound message through a registered
// transport and returns the transport's stable external message ID.
type RemoteTransportFunc func(root string, route TransportRoute, msg OutboundMessage) (string, error)

var (
	transportMu      sync.RWMutex
	remoteTransports = make(map[string]RemoteTransportFunc)
)

// RegisterRemoteTransport makes a transport backend available to the normal
// Comms send path. Called by the transport package at startup.
func RegisterRemoteTransport(name string, fn RemoteTransportFunc) {
	if name == "" || fn == nil {
		return
	}
	transportMu.Lock()
	defer transportMu.Unlock()
	remoteTransports[name] = fn
}

func lookupRemoteTransport(name string) (RemoteTransportFunc, bool) {
	transportMu.RLock()
	defer transportMu.RUnlock()
	fn, ok := remoteTransports[name]
	return fn, ok
}

type routingTable struct {
	localDomain string
	byDomain    map[string]TransportRoute
	configured  bool
}

// loadRoutingTable reads the routing table from the Comms configuration.
func loadRoutingTable(root string) (*routingTable, error) {
	cfg, err := config.Load(root)
	if err != nil {
		return nil, fmt.Errorf("comms routing: %w", err)
	}
	rt := &routingTable{
		localDomain: strings.TrimSpace(cfg.Comms.Routing.LocalDomain),
		byDomain:    make(map[string]TransportRoute),
	}
	if rt.localDomain == "" {
		rt.localDomain = "local"
	}
	for _, entry := range cfg.Comms.Routing.Domains {
		domain := strings.TrimSpace(entry.Domain)
		if domain == "" {
			return nil, fmt.Errorf("comms routing: empty domain")
		}
		if strings.ContainsAny(domain, " @/\\\t\n") {
			return nil, fmt.Errorf("comms routing: invalid domain %q", domain)
		}
		if strings.EqualFold(domain, rt.localDomain) {
			return nil, fmt.Errorf("comms routing: domain %q is the local domain", domain)
		}
		key := strings.ToLower(domain)
		if _, exists := rt.byDomain[key]; exists {
			return nil, fmt.Errorf("comms routing: duplicate domain %q", domain)
		}
		transport := strings.TrimSpace(entry.Transport)
		if transport == "" {
			return nil, fmt.Errorf("comms routing: domain %q has no transport", domain)
		}
		repository := strings.TrimSpace(entry.Repository)
		if transport != "local" && repository == "" {
			return nil, fmt.Errorf("comms routing: domain %q has no repository", domain)
		}
		rt.byDomain[key] = TransportRoute{
			Domain:     domain,
			Transport:  transport,
			Repository: repository,
		}
		rt.configured = true
	}
	return rt, nil
}

// resolve maps a recipient address to either a local target or a remote route.
// When no routing table is configured, all recipients stay local (backwards
// compatible with the existing bus, including identities containing '@').
func (rt *routingTable) resolve(target string) (localTarget string, route TransportRoute, remote bool, err error) {
	addr := strings.TrimSpace(target)
	if addr == "" {
		return "", TransportRoute{}, false, fmt.Errorf("comms: empty recipient")
	}
	user, domain := addr, ""
	if i := strings.LastIndex(addr, "@"); i >= 0 {
		user, domain = addr[:i], addr[i+1:]
	}
	if domain == "" || strings.EqualFold(domain, rt.localDomain) {
		if user == "" {
			return "", TransportRoute{}, false, fmt.Errorf("comms: invalid recipient %q", target)
		}
		return user, TransportRoute{}, false, nil
	}
	if !rt.configured {
		return addr, TransportRoute{}, false, nil
	}
	route, ok := rt.byDomain[strings.ToLower(domain)]
	if !ok {
		return "", TransportRoute{}, false, fmt.Errorf("comms: no route for domain %q", domain)
	}
	if user == "" {
		return "", TransportRoute{}, false, fmt.Errorf("comms: invalid recipient %q", target)
	}
	return user, route, true, nil
}

// resolveTarget resolves a recipient using the current routing table.
func (b *Bus) resolveTarget(target string) (string, TransportRoute, bool, error) {
	rt, err := loadRoutingTable(b.Root)
	if err != nil {
		return "", TransportRoute{}, false, err
	}
	return rt.resolve(target)
}

// publishRemote hands an outbound message to the transport selected by the
// routing table.
func (b *Bus) publishRemote(route TransportRoute, msg OutboundMessage) (string, error) {
	fn, ok := lookupRemoteTransport(route.Transport)
	if !ok {
		return "", fmt.Errorf("comms: transport %q is not available", route.Transport)
	}
	id, err := fn(b.Root, route, msg)
	if err != nil {
		return "", fmt.Errorf("comms: transport %q: %w", route.Transport, err)
	}
	if id == "" {
		return "", fmt.Errorf("comms: transport %q returned an empty message id", route.Transport)
	}
	return id, nil
}
