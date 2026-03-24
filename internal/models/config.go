// Package models defines the core data structures used throughout the application.
package models

import "strings"

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
	Output     string              // Full command output (e.g. from ffmpeg)
	StopAll    bool                // Whether to stop all processing (e.g. environment error)
}

// VideoConfig contains configuration for video processing
type VideoConfig struct {
	Preset               string
	CRF                  string
	TargetVideoBitrate   int64
	MinSavingsVideo      float64
	TranscodingVideoRate float64
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
	SplitLongerThan      float64
	MinSplitSegment      float64
	AudioOnly            bool
	AlwaysSplit          bool
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
	MoveBroken         string
	SourceAudioBitrate int64
	SourceVideoBitrate int64
	MaxWidthBuffer     float64
	MaxHeightBuffer    float64
	DeleteUnplayable   bool
	DeleteLarger       bool
	Valid              bool
	Invalid            bool
	ForceShrink        bool
	VerboseFFmpeg      bool
	IncludeTimecode    bool
	// Memory monitoring
	MemoryLimit        int64 // Memory limit in bytes (0 = no limit)
	MemoryCheckInterval int   // Memory check interval in milliseconds
}

// ProcessorConfig contains comprehensive configuration for all media processing
type ProcessorConfig struct {
	Video  VideoConfig
	Audio  AudioConfig
	Image  ImageConfig
	Text   TextConfig
	Common CommonConfig
}

// GetMinSavings returns the minimum savings threshold for a media category
func (c *ProcessorConfig) GetMinSavings(category string) float64 {
	switch strings.ToLower(category) {
	case "video":
		return c.Video.MinSavingsVideo
	case "audio":
		return c.Audio.MinSavingsAudio
	case "image", "text":
		return c.Image.MinSavingsImage
	default:
		return 0.05 // Default 5%
	}
}
