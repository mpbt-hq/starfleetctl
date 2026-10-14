// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package modelproxy

import (
	"io"
	"net/http"
)

// ResponseAssembler assembles the final response from a completed turn.
type ResponseAssembler interface {
	// Assemble builds the final response from the turn.
	Assemble(turn *Turn) (Response, error)
}

// Response represents the final assembled response.
type Response struct {
	StatusCode int
	Headers    http.Header
	Body       io.Reader
	Streaming  bool
	StreamChan chan StreamChunk
}

// StreamChunkType is a synonym for ChunkType for clarity in response assembly.
type StreamChunkType = ChunkType

// StreamingResponseAssembler handles streaming response assembly.
type StreamingResponseAssembler struct {
	// Chunk processors can be chained to transform streaming data
	processors []StreamChunkProcessor
}

// StreamChunkProcessor can modify stream chunks as they flow through.
type StreamChunkProcessor interface {
	ProcessChunk(chunk StreamChunk, turn *Turn) (StreamChunk, error)
}

// NewStreamingResponseAssembler creates a new streaming response assembler.
func NewStreamingResponseAssembler() *StreamingResponseAssembler {
	return &StreamingResponseAssembler{
		processors: make([]StreamChunkProcessor, 0),
	}
}

// AddProcessor adds a chunk processor to the chain.
func (s *StreamingResponseAssembler) AddProcessor(processor StreamChunkProcessor) {
	s.processors = append(s.processors, processor)
}

// Assemble builds the streaming response from the turn.
func (s *StreamingResponseAssembler) Assemble(turn *Turn) (Response, error) {
	if turn.StreamChunks == nil {
		return Response{}, nil
	}

	// Create the output channel
	outChan := make(chan StreamChunk, 10)

	// Start a goroutine to process chunks through the processor chain
	go func() {
		defer close(outChan)
		for chunk := range turn.StreamChunks {
			processed := chunk
			var err error
			for _, processor := range s.processors {
				processed, err = processor.ProcessChunk(processed, turn)
				if err != nil {
					// Emit error chunk and stop
					outChan <- StreamChunk{
						Type: ChunkError,
						Data: []byte(err.Error()),
					}
					return
				}
			}
			outChan <- processed
		}
	}()

	return Response{
		StatusCode: turn.StatusCode,
		Headers:    turn.Headers,
		Streaming:  true,
		StreamChan: outChan,
	}, nil
}

// NonStreamingResponseAssembler handles non-streaming response assembly.
type NonStreamingResponseAssembler struct{}

// NewNonStreamingResponseAssembler creates a new non-streaming response assembler.
func NewNonStreamingResponseAssembler() *NonStreamingResponseAssembler {
	return &NonStreamingResponseAssembler{}
}

// Assemble builds the non-streaming response from the turn.
func (n *NonStreamingResponseAssembler) Assemble(turn *Turn) (Response, error) {
	return Response{
		StatusCode: turn.StatusCode,
		Headers:    turn.Headers,
		Body:       turn.BodyReader,
		Streaming:  false,
	}, nil
}
