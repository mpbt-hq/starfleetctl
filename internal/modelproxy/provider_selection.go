// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package modelproxy

import (
	"context"
	"fmt"
)

// ProviderSelectionStage selects a provider and prepares the upstream request.
type ProviderSelectionStage struct {
	// We'll hold a reference to the proxy to access its cfg, strategies, etc.
	// But to avoid circular dependencies, we'll pass the necessary dependencies via the constructor.
	proxy *Proxy
}

// NewProviderSelectionStage creates a new provider selection stage.
func NewProviderSelectionStage(proxy *Proxy) *ProviderSelectionStage {
	return &ProviderSelectionStage{proxy: proxy}
}

// Name returns the stage name.
func (s *ProviderSelectionStage) Name() string {
	return "provider_selection"
}

// CanHandle determines if this stage can process the given turn.
// We only handle turns that haven't had their request prepared yet.
func (s *ProviderSelectionStage) CanHandle(turn *Turn) bool {
	return turn.PreparedRequest == nil
}

// Process selects a provider and prepares the request.
func (s *ProviderSelectionStage) Process(ctx context.Context, turn *Turn) error {
	// Select a provider for the turn.
	provider, err := s.selectProvider(turn)
	if err != nil {
		return err
	}
	turn.Provider = provider.ID

	// Prepare the request using the provider's adapter.
	adapterFn := s.proxy.providerRegistry.Get(provider.ID)
	if adapterFn == nil {
		return fmt.Errorf("no adapter found for provider %s", provider.ID)
	}
	adapter := adapterFn(provider)
	req, err := adapter.PrepareRequest(turn)
	if err != nil {
		return err
	}
	turn.PreparedRequest = req

	return nil
}

// selectProvider selects a provider for the given turn.
// This mimics the logic in the existing proxy's handleChat function.
func (s *ProviderSelectionStage) selectProvider(turn *Turn) (*Provider, error) {
	// Check if the model is a strategy.
	strat := s.proxy.getStrategyForModel(turn.Model)
	if strat != nil {
		// Route through strategy.
		_, chosenProv, err := s.proxy.routeStrategyModel(strat, turn.SessionID(), turn.Model)
		if err != nil {
			return nil, err
		}
		// chosenProv is a *Provider from the config.
		return chosenProv, nil
	}

	// Direct provider routing.
	prov, _ := s.proxy.routeModel(turn.Model)
	if prov == nil {
		return nil, fmt.Errorf("model %q not served by any configured model-proxy provider", turn.Model)
	}
	// We don't need to return the upstreamModel because the provider adapter will use the provider's BaseURL.
	// But note: the existing proxy's routeModel returns the provider and the upstream model ID.
	// We'll need to set the turn's Model to the upstreamModel for the provider adapter.
	// However, the provider adapter's PrepareRequest uses the turn.Model to build the body.
	// We want to keep the original model in the turn for the body, but the provider adapter needs to know
	// the actual upstream model ID to use in the URL if using prefix routing.
	// We'll handle this in the provider adapter's PrepareRequest by checking for a prefix.
	// For now, we'll leave the turn.Model as the original model and let the provider adapter handle prefix routing.
	// We'll need to update the provider adapter's PrepareRequest to support prefix routing.

	// We'll return the provider.
	return prov, nil
}
