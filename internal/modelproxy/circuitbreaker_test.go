// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package modelproxy

import (
	"testing"
	"time"
)

// TestCircuitBreakerRecoversAfterCooldown is the regression test for the wedge
// where an Open breaker was skipped by every strategy, so checkCircuitBreaker
// never ran for it and the cooldown→HalfOpen transition could never fire: the
// model stayed out of rotation ("all circuit breakers open") until the proxy
// was restarted, even after the upstream had recovered.
func TestCircuitBreakerRecoversAfterCooldown(t *testing.T) {
	p, _, _ := testProxyWithStrategies(t)
	ss := p.getStrategy("heavy")
	if ss == nil {
		t.Fatal("strategy 'heavy' not found")
	}

	cb := ss.CircuitBreakers["m1"]
	metrics := ss.ModelMetrics["m1"]
	if cb == nil || metrics == nil {
		t.Fatal("m1 breaker/metrics not initialised")
	}

	// Simulate a lifetime error rate well above the 20% heuristic, then trip
	// the breaker and let its cooldown expire.
	metrics.RequestCount = 100
	metrics.ErrorCount = 60
	metrics.ConsecutiveFailures = 3
	cb.State = CircuitBreakerOpen
	cb.LastStateChange = time.Now().Add(-2 * circuitBreakerCooldown)

	// Routing must offer the expired breaker as a HalfOpen probe instead of
	// skipping it (old behaviour: falls through to m2).
	model, prov, err := p.routeStrategyModel(ss, "", "heavy")
	if err != nil {
		t.Fatalf("routeStrategyModel: %v", err)
	}
	if model != "m1" || prov == nil {
		t.Fatalf("routed to %q, want m1 (expired breaker must be probed)", model)
	}
	if cb.State != CircuitBreakerHalfOpen {
		t.Fatalf("breaker state = %v, want HalfOpen", cb.State)
	}
	if metrics.RequestCount != 0 || metrics.ErrorCount != 0 || metrics.ConsecutiveFailures != 0 {
		t.Fatalf("metrics window not reset on HalfOpen: %+v", metrics)
	}

	// A successful probe must close the breaker even though the historical
	// error rate was > 20%.
	p.recordStrategyMetrics(ss, "m1", time.Millisecond, 10, nil)
	p.checkCircuitBreaker(ss, "m1")
	if cb.State != CircuitBreakerClosed {
		t.Fatalf("breaker state = %v, want Closed after successful probe", cb.State)
	}
}

// TestCircuitBreakerOpenNotProbedBeforeCooldown ensures a still-cooling breaker
// is skipped and routing falls back to the next model.
func TestCircuitBreakerOpenNotProbedBeforeCooldown(t *testing.T) {
	p, _, _ := testProxyWithStrategies(t)
	ss := p.getStrategy("heavy")

	cb := ss.CircuitBreakers["m1"]
	cb.State = CircuitBreakerOpen
	cb.LastStateChange = time.Now() // cooldown just started

	model, _, err := p.routeStrategyModel(ss, "", "heavy")
	if err != nil {
		t.Fatalf("routeStrategyModel: %v", err)
	}
	if model != "m2" {
		t.Fatalf("routed to %q, want m2 while m1 is cooling down", model)
	}
}
