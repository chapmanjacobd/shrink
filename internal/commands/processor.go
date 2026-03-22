package commands

import (
	"github.com/chapmanjacobd/shrink/internal/ffmpeg"
	"github.com/chapmanjacobd/shrink/internal/models"
	"github.com/chapmanjacobd/shrink/internal/utils"
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

// NewProcessorRegistry creates a new registry with all available processors
func NewProcessorRegistry(ffmpeg *ffmpeg.FFmpegProcessor) *MediaRegistry {
	return &MediaRegistry{
		processors: []models.MediaProcessor{
			NewVideoProcessor(ffmpeg),
			NewAudioProcessor(ffmpeg),
			NewImageProcessor(),
			NewTextProcessor(),
			NewArchiveProcessor(ffmpeg),
		},
	}
}

// GetProcessor returns the appropriate processor for a media item
func (r *MediaRegistry) GetProcessor(m *models.ShrinkMedia) models.MediaProcessor {
	for _, p := range r.processors {
		if p.CanProcess(m) {
			return p
		}
	}
	return nil
}

// GetAllProcessors returns all registered processors
func (r *MediaRegistry) GetAllProcessors() []models.MediaProcessor {
	return r.processors
}

// ============================================================================
// Helper Utilities
// ============================================================================

// shouldConvertToAVIF returns true if the extension should be converted to AVIF
func shouldConvertToAVIF(ext string) bool {
	if !utils.ImageExtensionMap[ext] {
		return false
	}
	// Skip vector formats and already-optimized formats
	skipExts := map[string]bool{
		".avif": true, // Already AVIF
		".svg":  true, // Vector format
		".svgz": true, // Compressed SVG
	}
	return !skipExts[ext]
}
