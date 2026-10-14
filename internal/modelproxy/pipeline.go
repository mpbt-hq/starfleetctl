// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package modelproxy

import (
	"context"
)

// PipelineStage defines the interface for a stage in the processing pipeline.
// Each stage processes a Turn and may modify it or return an error.
type PipelineStage interface {
	// Name returns the stage name for identification and debugging.
	Name() string

	// Process processes the turn and returns an error if processing failed.
	// Stages can modify the turn in-place or return a new turn (conceptually).
	Process(ctx context.Context, turn *Turn) error

	// CanHandle determines if this stage can process the given turn.
	// Optional: if not implemented, stage handles all turns.
	CanHandle(turn *Turn) bool
}

// Pipeline orchestrates the execution of pipeline stages in order.
type Pipeline struct {
	stages []PipelineStage
}

// NewPipeline creates a new pipeline with the given stages.
func NewPipeline(stages ...PipelineStage) *Pipeline {
	return &Pipeline{stages: stages}
}

// AddStage adds a stage to the end of the pipeline.
func (p *Pipeline) AddStage(stage PipelineStage) {
	p.stages = append(p.stages, stage)
}

// Execute runs the pipeline stages in order on the given turn.
// If any stage returns an error, execution stops and that error is returned.
func (p *Pipeline) Execute(ctx context.Context, turn *Turn) error {
	for _, stage := range p.stages {
		if !stage.CanHandle(turn) {
			continue
		}
		if err := stage.Process(ctx, turn); err != nil {
			return turn.HandleError(err)
		}
	}
	return nil
}
