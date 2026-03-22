package commands

import (
	"context"
	"strings"

	"github.com/chapmanjacobd/shrink/internal/utils"
)

// ProcessOutputFile represents a new file created by a processor
type ProcessOutputFile struct {
	Path string
	Size int64
}

// ProcessResult contains the comprehensive result of processing a media file
type ProcessResult struct {
	SourcePath string              // Original file being processed
	Outputs    []ProcessOutputFile // New files created
	PartFiles  []string            // Multi-part archive part files (for cleanup)
	Success    bool                // Whether the overall operation succeeded
	Error      error               // Error if the operation failed
}

// MediaProcessor defines the interface for processing different media types
type MediaProcessor interface {
	// CanProcess returns true if this processor can handle the given media
	CanProcess(m *ShrinkMedia) bool

	// EstimateSize calculates the future file size and processing time
	EstimateSize(m *ShrinkMedia, cfg *ProcessorConfig) (futureSize int64, processingTime int)

	// Process executes the transcoding/conversion
	// Returns a single ProcessResult containing all outputs and cleanup tasks
	Process(ctx context.Context, m *ShrinkMedia, cfg *ProcessorConfig) ProcessResult

	// Category returns the type identifier for this processor
	Category() string
}

// VideoConfig contains configuration for video processing
type VideoConfig struct {
	TargetVideoBitrate   int64
	MinSavingsVideo      float64
	TranscodingVideoRate float64
	Preset               string
	CRF                  string
	MaxVideoWidth        int
	MaxVideoHeight       int
	VideoOnly            bool
	Keyframes            bool
	NoPreserveVideo      bool
}

// AudioConfig contains configuration for audio processing
type AudioConfig struct {
	TargetAudioBitrate   int64
	MinSavingsAudio      float64
	TranscodingAudioRate float64
	AudioOnly            bool
	AlwaysSplit          bool
	SplitLongerThan      float64
	MinSplitSegment      float64
}

// ImageConfig contains configuration for image processing
type ImageConfig struct {
	TargetImageSize      int64
	MinSavingsImage      float64
	TranscodingImageTime float64
	MaxImageWidth        int
	MaxImageHeight       int
}

// TextConfig contains configuration for text/ebook processing
type TextConfig struct {
	SkipOCR  bool
	ForceOCR bool
	RedoOCR  bool
	NoOCR    bool
}

// CommonConfig contains general configuration for all processors
type CommonConfig struct {
	SourceAudioBitrate int64
	SourceVideoBitrate int64
	DeleteUnplayable   bool
	DeleteLarger       bool
	MoveBroken         string
	Valid              bool
	Invalid            bool
	ForceShrink        bool
	VerboseFFmpeg      bool
	IncludeTimecode    bool
	MaxWidthBuffer     float64
	MaxHeightBuffer    float64
}

// ProcessorConfig contains comprehensive configuration for all media processing
type ProcessorConfig struct {
	Video  VideoConfig
	Audio  AudioConfig
	Image  ImageConfig
	Text   TextConfig
	Common CommonConfig
}

// BaseProcessor provides common functionality for all processors
type BaseProcessor struct {
	category string
}

// Category returns the media type for this processor
func (b *BaseProcessor) Category() string {
	return b.category
}

// ProcessorRegistry manages all media processors
type ProcessorRegistry struct {
	processors []MediaProcessor
}

// NewProcessorRegistry creates a new registry with all available processors
func NewProcessorRegistry(ffmpeg *FFmpegProcessor) *ProcessorRegistry {
	return &ProcessorRegistry{
		processors: []MediaProcessor{
			NewVideoProcessor(ffmpeg),
			NewAudioProcessor(ffmpeg),
			NewImageProcessor(),
			NewTextProcessor(),
			NewArchiveProcessor(ffmpeg),
		},
	}
}

// GetProcessor returns the appropriate processor for a media item
func (r *ProcessorRegistry) GetProcessor(m *ShrinkMedia) MediaProcessor {
	for _, p := range r.processors {
		if p.CanProcess(m) {
			return p
		}
	}
	return nil
}

// GetAllProcessors returns all registered processors
func (r *ProcessorRegistry) GetAllProcessors() []MediaProcessor {
	return r.processors
}

// ShouldShrink determines if a file should be shrinked based on savings threshold
func ShouldShrink(m *ShrinkMedia, futureSize int64, cfg *ProcessorConfig) bool {
	if cfg.Common.ForceShrink {
		return true
	}
	shouldShrinkBuffer := int64(float64(futureSize) * getMinSavings(m, cfg))
	return m.Size > (futureSize + shouldShrinkBuffer)
}

func getMinSavings(m *ShrinkMedia, cfg *ProcessorConfig) float64 {
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
