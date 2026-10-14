// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package modelproxy

import (
	"sync"
)

// ProviderAdapterRegistry manages registered provider adapters by name.
type ProviderAdapterRegistry struct {
	mu       sync.RWMutex
	adapters map[string]func(*Provider) ProviderAdapter
}

// NewProviderAdapterRegistry creates a new registry.
func NewProviderAdapterRegistry() *ProviderAdapterRegistry {
	return &ProviderAdapterRegistry{
		adapters: make(map[string]func(*Provider) ProviderAdapter),
	}
}

// Register registers a provider adapter constructor with the given name.
func (r *ProviderAdapterRegistry) Register(name string, ctor func(*Provider) ProviderAdapter) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.adapters[name] = ctor
}

// Get returns the constructor for the given name, or nil if not found.
func (r *ProviderAdapterRegistry) Get(name string) func(*Provider) ProviderAdapter {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.adapters[name]
}

// Unregister removes the constructor for the given name.
func (r *ProviderAdapterRegistry) Unregister(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.adapters, name)
}

// GetAll returns all registered constructors.
func (r *ProviderAdapterRegistry) GetAll() map[string]func(*Provider) ProviderAdapter {
	r.mu.RLock()
	defer r.mu.RUnlock()
	copied := make(map[string]func(*Provider) ProviderAdapter, len(r.adapters))
	for name, ctor := range r.adapters {
		copied[name] = ctor
	}
	return copied
}

// Global registry instance.
var (
	globalProviderRegistry *ProviderAdapterRegistry
	providerRegistryOnce   sync.Once
)

// GetGlobalProviderRegistry returns the global provider adapter registry.
func GetGlobalProviderRegistry() *ProviderAdapterRegistry {
	providerRegistryOnce.Do(func() {
		globalProviderRegistry = NewProviderAdapterRegistry()
	})
	return globalProviderRegistry
}
