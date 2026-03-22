package commands

import (
	"strings"

	"github.com/chapmanjacobd/shrink/internal/ffmpeg"
	"github.com/chapmanjacobd/shrink/internal/models"
	"github.com/chapmanjacobd/shrink/internal/utils"
)

// BaseProcessor provides common functionality for all processors
type BaseProcessor struct {
	category string
}

// Category returns the media type for this processor
func (b *BaseProcessor) Category() string {
	return b.category
}

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

// ShouldShrink determines if a file should be shrinked based on savings threshold
func ShouldShrink(m *models.ShrinkMedia, futureSize int64, cfg *models.ProcessorConfig) bool {
	if cfg.Common.ForceShrink {
		return true
	}
	minSavings := getMinSavings(m, cfg)
	if minSavings < 1.0 {
		// Threshold is a percentage of future size
		shouldShrinkBuffer := int64(float64(futureSize) * minSavings)
		return m.Size > (futureSize + shouldShrinkBuffer)
	}
	// Threshold is absolute bytes
	return (m.Size - futureSize) >= int64(minSavings)
}

func getMinSavings(m *models.ShrinkMedia, cfg *models.ProcessorConfig) float64 {
	switch strings.ToLower(m.Category) {
	case "video":
		return cfg.Video.MinSavingsVideo
	case "audio":
		return cfg.Audio.MinSavingsAudio
	case "image", "text":
		return cfg.Image.MinSavingsImage
	default:
		return 0.05 // Default 5%
	}
}

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
