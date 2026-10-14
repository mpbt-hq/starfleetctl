// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package modelproxy

import (
	"context"
)

// ResponseAssemblyStage assembles the final response from the turn.
type ResponseAssemblyStage struct {
	// We'll hold a reference to the proxy to access its cfg, etc.
	proxy *Proxy
}

// NewResponseAssemblyStage creates a new response assembly stage.
func NewResponseAssemblyStage(proxy *Proxy) *ResponseAssemblyStage {
	return &ResponseAssemblyStage{proxy: proxy}
}

// Name returns the stage name.
func (s *ResponseAssemblyStage) Name() string {
	return "response_assembly"
}

// CanHandle determines if this stage can process the given turn.
// We'll handle all turns that have a prepared request (meaning they've gone through provider selection).
func (s *ResponseAssemblyStage) CanHandle(turn *Turn) bool {
	return turn.PreparedRequest != nil
}

// Process assembles the response.
func (s *ResponseAssemblyStage) Process(ctx context.Context, turn *Turn) error {
	// Determine if this is a streaming request
	isStreaming := turn.Stream

	// Create the appropriate assembler
	var assembler ResponseAssembler
	if isStreaming {
		assembler = NewStreamingResponseAssembler()
	} else {
		assembler = NewNonStreamingResponseAssembler()
	}

	// Assemble the response
	response, err := assembler.Assemble(turn)
	if err != nil {
		return err
	}

	// Store the assembled response in the turn
	turn.StatusCode = response.StatusCode
	turn.Headers = response.Headers
	if response.Body != nil {
		turn.BodyReader = response.Body
	}
	if response.Streaming {
		turn.StreamChunks = response.StreamChan
	}

	return nil
}
