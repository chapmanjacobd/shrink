package models

import "context"

// MediaProcessor defines the interface for processing different media types
type MediaProcessor interface {
	// CanProcess returns true if this processor can handle the given media
	CanProcess(m *ShrinkMedia) bool

	// EstimateSize calculates the future file size and processing time
	EstimateSize(m *ShrinkMedia, cfg *ProcessorConfig) (futureSize int64, processingTime int)

	// Process executes the transcoding/conversion
	// Returns a single ProcessResult containing all outputs and cleanup tasks
	Process(ctx context.Context, m *ShrinkMedia, cfg *ProcessorConfig, registry ProcessorRegistry) ProcessResult

	// Category returns the type identifier for this processor
	Category() string
}

// ProcessorRegistry interface defines how to retrieve a processor
type ProcessorRegistry interface {
	GetProcessor(m *ShrinkMedia) MediaProcessor
}
