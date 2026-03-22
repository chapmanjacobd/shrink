package models

import "context"

// ProcessableInfo contains information about whether and how a media item can be processed
type ProcessableInfo struct {
	FutureSize     int64
	ProcessingTime int
	IsProcessable  bool
	ActualSize     int64    // Used if the actual size differs from the reported size (e.g. multi-part archives)
	IsBroken       bool     // Whether the media item is broken/unreadable
	PartFiles      []string // Associated files (e.g. multi-part archive volumes)
}

// MediaProcessor defines the interface for processing different media types
type MediaProcessor interface {
	// CanProcess returns true if this processor can handle the given media
	CanProcess(m *ShrinkMedia) bool

	// EstimateSize calculates the future file size and processing time
	EstimateSize(m *ShrinkMedia, cfg *ProcessorConfig) ProcessableInfo

	// Process executes the transcoding/conversion
	// Returns a single ProcessResult containing all outputs and cleanup tasks
	Process(ctx context.Context, m *ShrinkMedia, cfg *ProcessorConfig, registry ProcessorRegistry) ProcessResult

	// Category returns the type identifier for this processor
	Category() string

	// RequiredTool returns the name of the external tool required by this processor
	RequiredTool() string
}

// ProcessorRegistry interface defines how to retrieve a processor
type ProcessorRegistry interface {
	GetProcessor(m *ShrinkMedia) MediaProcessor
}
