package commands

import (
	"github.com/chapmanjacobd/shrink/internal/ffmpeg"
	"github.com/chapmanjacobd/shrink/internal/models"
)

// ============================================================================
// Processor Types
// ============================================================================

// BaseProcessor provides common functionality for all processors
type BaseProcessor struct {
	category     string
	requiredTool string
}

// Category returns the media type for this processor
func (b *BaseProcessor) Category() string {
	return b.category
}

// RequiredTool returns the name of the external tool required by this processor
func (b *BaseProcessor) RequiredTool() string {
	return b.requiredTool
}

// ============================================================================
// Media Registry
// ============================================================================

// MediaRegistry manages all media processors
type MediaRegistry struct {
	processors []models.MediaProcessor
}

// NewProcessorRegistry creates a new registry with available processors based on flags
func NewProcessorRegistry(ffmpeg *ffmpeg.FFmpegProcessor, cfg *models.ProcessorConfig, videoOnly, audioOnly, imageOnly, textOnly, noArchives bool) *MediaRegistry {
	all := !videoOnly && !audioOnly && !imageOnly && !textOnly
	var processors []models.MediaProcessor

	if all || videoOnly {
		processors = append(processors, NewVideoProcessor(ffmpeg))
	}
	if all || audioOnly {
		processors = append(processors, NewAudioProcessor(ffmpeg))
	}
	if all || imageOnly {
		processors = append(processors, NewImageProcessor())
	}
	if all || textOnly {
		processors = append(processors, NewTextProcessor())
	}

	if !noArchives {
		// Add ArchiveProcessor as it might contain processable files of any requested type
		processors = append(processors, NewArchiveProcessor(ffmpeg, cfg))
	}

	return &MediaRegistry{
		processors: processors,
	}
}

// GetProcessor returns the appropriate processor for a media item
func (r *MediaRegistry) GetProcessor(m *models.ShrinkMedia) models.MediaProcessor {
	// If category is already set, try to find a processor matching it exactly
	if m.Category != "" {
		for _, p := range r.processors {
			if p.Category() == m.Category {
				return p
			}
		}
	}

	// Fallback to extension-based detection
	for _, p := range r.processors {
		if p.CanProcess(m) {
			return p
		}
	}
	return nil
}

// Cleanup releases resources held by processors
func (r *MediaRegistry) Cleanup() {
	for _, p := range r.processors {
		if tp, ok := p.(*TextProcessor); ok {
			tp.Cleanup()
		}
	}
}
