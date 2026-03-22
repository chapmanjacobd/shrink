package models

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
