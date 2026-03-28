// Package commands implements the CLI commands and orchestration logic for the shrink application.
package commands

import (
	"github.com/chapmanjacobd/shrink/internal/models"
	"github.com/chapmanjacobd/shrink/internal/utils"
)

// Config contains all command-line flags and configuration for the shrink application
type Config struct {
	CoreFlags        `embed:""`
	PathFilterFlags  `embed:"" group:"PathFilter"`
	MediaFilterFlags `embed:"" group:"MediaFilter"`
	TimeFilterFlags  `embed:"" group:"Time"`
	DeletedFlags     `embed:"" group:"Deleted"`
	SavingsFlags     `embed:"" group:"Savings"`
	BitrateFlags     `embed:"" group:"Bitrate"`
	TranscodingFlags `embed:"" group:"Transcoding"`
	VideoFlags       `embed:"" group:"Video"`
	ImageFlags       `embed:"" group:"Image"`
	TextFlags        `embed:"" group:"Text"`
	ParallelFlags    `embed:"" group:"Parallel"`
	MemoryFlags      `embed:"" group:"Memory"`
	TimeoutFlags     `embed:"" group:"Timeout"`

	ContinueFrom string `help:"Skip media until specific file path is seen" env:"SHRINK_CONTINUE_FROM"`
	Move         string `help:"Directory to move successful files" env:"SHRINK_MOVE"`
	MoveBroken   string `help:"Directory to move unsuccessful files" env:"SHRINK_MOVE_BROKEN"`
	ForceShrink  bool   `help:"Force reprocessing of already shrinked files" env:"SHRINK_FORCE_SHRINK"`
}

type CoreFlags struct {
	Profile   string `default:"balance" help:"Profile preset: quality, speed, balance (default to balance preset 7 crf 40)" enum:"quality,speed,balance" env:"SHRINK_PROFILE"`
	Verbose   int    `short:"v" type:"counter" help:"Enable verbose logging (-v for info, -vv for debug)" env:"SHRINK_VERBOSE"`
	Simulate  bool   `help:"Dry run; don't actually do anything" env:"SHRINK_SIMULATE"`
	NoConfirm bool   `short:"y" help:"Don't ask for confirmation" env:"SHRINK_NO_CONFIRM"`
}

type PathFilterFlags struct {
	Include []string `short:"s" help:"Include paths matching pattern" env:"SHRINK_INCLUDE"`
	Exclude []string `short:"E" help:"Exclude paths matching pattern" env:"SHRINK_EXCLUDE"`
	Search  []string `help:"Search terms" env:"SHRINK_SEARCH"`
}

type MediaFilterFlags struct {
	VideoOnly bool `help:"Only video files" env:"SHRINK_VIDEO_ONLY"`
	AudioOnly bool `help:"Only audio files" env:"SHRINK_AUDIO_ONLY"`
	ImageOnly bool `help:"Only image files" env:"SHRINK_IMAGE_ONLY"`
	TextOnly  bool `help:"Only text/ebook files" env:"SHRINK_TEXT_ONLY"`
}

type TimeFilterFlags struct {
	CreatedAfter   string `help:"Created after date" env:"SHRINK_CREATED_AFTER"`
	ModifiedAfter  string `help:"Modified after date" env:"SHRINK_MODIFIED_AFTER"`
	ModifiedBefore string `help:"Modified before date" env:"SHRINK_MODIFIED_BEFORE"`
}

type DeletedFlags struct {
	// HideDeleted defaults to true: most users don't want to see deleted files in listings
	HideDeleted bool `default:"true" help:"Exclude deleted files" env:"SHRINK_HIDE_DELETED"`
	OnlyDeleted bool `help:"Include only deleted files" env:"SHRINK_ONLY_DELETED"`
	// Valid defaults to true: prefer processing files with valid metadata
	Valid   bool `default:"true" help:"Attempt to process files with valid metadata" env:"SHRINK_VALID"`
	Invalid bool `help:"Attempt to process files with invalid metadata" env:"SHRINK_INVALID"`
}

type SavingsFlags struct {
	MinSavingsVideo string `default:"5%" help:"Minimum savings for video (percentage or bytes)" env:"SHRINK_MIN_SAVINGS_VIDEO"`
	MinSavingsAudio string `default:"10%" help:"Minimum savings for audio (percentage or bytes)" env:"SHRINK_MIN_SAVINGS_AUDIO"`
	MinSavingsImage string `default:"15%" help:"Minimum savings for images (percentage or bytes)" env:"SHRINK_MIN_SAVINGS_IMAGE"`
}

type BitrateFlags struct {
	SourceAudioBitrate string `default:"256kbps" help:"Used to estimate duration when files are inside of archives or invalid" env:"SHRINK_SOURCE_AUDIO_BITRATE"`
	SourceVideoBitrate string `default:"1500kbps" help:"Used to estimate duration when files are inside of archives or invalid" env:"SHRINK_SOURCE_VIDEO_BITRATE"`
	TargetAudioBitrate string `default:"128kbps" help:"Target audio bitrate" env:"SHRINK_TARGET_AUDIO_BITRATE"`
	TargetVideoBitrate string `default:"800kbps" help:"Target video bitrate" env:"SHRINK_TARGET_VIDEO_BITRATE"`
}

type TranscodingFlags struct {
	TranscodingVideoRate float64 `default:"1.8" help:"Ratio of duration eg. 4x realtime speed" env:"SHRINK_TRANSCODING_VIDEO_RATE"`
	TranscodingAudioRate float64 `default:"150" help:"Ratio of duration eg. 100x realtime speed" env:"SHRINK_TRANSCODING_AUDIO_RATE"`
	TranscodingImageTime float64 `default:"1.5" help:"Seconds to process an image" env:"SHRINK_TRANSCODING_IMAGE_TIME"`
	VerboseFFmpeg        bool    `help:"Enable verbose FFmpeg logging" env:"SHRINK_VERBOSE_FFMPEG"`
}

type VideoFlags struct {
	Preset          string `help:"SVT-AV1 preset (0-13, lower is slower/better) (default depends on profile)" env:"SHRINK_PRESET"`
	CRF             string `help:"CRF value for SVT-AV1 (0-63, lower is better) (default depends on profile)" env:"SHRINK_CRF"`
	MaxVideoHeight  int    `default:"960" help:"Maximum video height" env:"SHRINK_MAX_VIDEO_HEIGHT"`
	MaxVideoWidth   int    `default:"1440" help:"Maximum video width" env:"SHRINK_MAX_VIDEO_WIDTH"`
	Keyframes       bool   `help:"Extract keyframes only" env:"SHRINK_KEYFRAMES"`
	NoPreserveVideo bool   `help:"Don't preserve video when audio-only" env:"SHRINK_NO_PRESERVE_VIDEO"`
	IncludeTimecode bool   `help:"Include timecode streams in output" env:"SHRINK_INCLUDE_TIMECODE"`
	// DeleteLarger defaults to true: keep the smaller file (usually the transcoded output)
	DeleteLarger bool `default:"true" help:"Delete larger of transcode or original files" env:"SHRINK_DELETE_LARGER"`
}

type ImageFlags struct {
	TargetImageSize string  `default:"30KiB" help:"Target image size" env:"SHRINK_TARGET_IMAGE_SIZE"`
	MaxImageHeight  int     `default:"2400" help:"Maximum image height" env:"SHRINK_MAX_IMAGE_HEIGHT"`
	MaxImageWidth   int     `default:"2400" help:"Maximum image width" env:"SHRINK_MAX_IMAGE_WIDTH"`
	MaxWidthBuffer  float64 `default:"0.05" help:"Buffer percentage for width upscaling" env:"SHRINK_MAX_WIDTH_BUFFER"`
	MaxHeightBuffer float64 `default:"0.05" help:"Buffer percentage for height upscaling" env:"SHRINK_MAX_HEIGHT_BUFFER"`
}

type TextFlags struct {
	SkipOCR  bool `help:"Skip OCR for PDFs that already contain text" env:"SHRINK_SKIP_OCR"`
	ForceOCR bool `help:"Force OCR even on PDFs with text" env:"SHRINK_FORCE_OCR"`
	RedoOCR  bool `help:"Re-do OCR on PDFs that already have OCR" env:"SHRINK_REDO_OCR"`
	NoOCR    bool `help:"Skip OCR entirely" env:"SHRINK_NO_OCR"`
}

type ParallelFlags struct {
	VideoThreads    int `default:"2" help:"Maximum concurrent video transcodes" env:"SHRINK_VIDEO_THREADS"`
	Video4KThreads  int `default:"1" help:"Maximum concurrent video transcodes for 4K+ resolution videos" env:"SHRINK_VIDEO_4K_THREADS"`
	AudioThreads    int `default:"4" help:"Maximum concurrent audio transcodes" env:"SHRINK_AUDIO_THREADS"`
	ImageThreads    int `default:"8" help:"Maximum concurrent image conversions" env:"SHRINK_IMAGE_THREADS"`
	TextThreads     int `default:"2" help:"Maximum concurrent text conversions" env:"SHRINK_TEXT_THREADS"`
	AnalysisThreads int `default:"0" help:"Maximum concurrent analysis workers (0: CPU count * 4)" env:"SHRINK_ANALYSIS_THREADS"`
}

type MemoryFlags struct {
	MemoryLimit    string `default:"" help:"Maximum memory usage per process (e.g., 4G, 512M). Default: 8GB. Set to 0 for no limit" env:"SHRINK_MEMORY_LIMIT"`
	MemorySwapMax  string `default:"" help:"Maximum swap usage per process (e.g., 2G, 0 to disable). Default: half of MemoryLimit" env:"SHRINK_MEMORY_SWAP_MAX"`
	UseJournald    bool   `help:"Use journald-compatible mode for systemd-run" env:"SHRINK_USE_JOURNALD"`
	DisableSystemd bool   `help:"Disable systemd-run wrapper even if available" env:"SHRINK_DISABLE_SYSTEMD"`
}

type TimeoutFlags struct {
	VideoTimeout     string  `default:"90m" help:"Video timeout when duration is unknown" env:"SHRINK_VIDEO_TIMEOUT"`
	AudioTimeout     string  `default:"10m" help:"Audio timeout when duration is unknown" env:"SHRINK_AUDIO_TIMEOUT"`
	ImageTimeout     string  `default:"10m" help:"Image timeout" env:"SHRINK_IMAGE_TIMEOUT"`
	TextTimeout      string  `default:"20m" help:"Text timeout" env:"SHRINK_TEXT_TIMEOUT"`
	VideoTimeoutMult float64 `default:"3.0" help:"Video timeout multiplier (timeout = duration * multiplier)" env:"SHRINK_VIDEO_TIMEOUT_MULT"`
	AudioTimeoutMult float64 `default:"0.5" help:"Audio timeout multiplier (timeout = duration * multiplier)" env:"SHRINK_AUDIO_TIMEOUT_MULT"`
	SplitLongerThan  float64 `help:"Split audio longer than N seconds" env:"SHRINK_SPLIT_LONGER_THAN"`
	MinSplitSegment  float64 `default:"60" help:"Minimum split segment duration in seconds" env:"SHRINK_MIN_SPLIT_SEGMENT"`
	DeleteUnplayable bool    `help:"Delete unplayable files" env:"SHRINK_DELETE_UNPLAYABLE"`
	AlwaysSplit      bool    `help:"Always split audio on silence" env:"SHRINK_ALWAYS_SPLIT"`
}

func (c *Config) ApplyProfile() {
	preset := "7"
	crf := "40"
	switch c.Profile {
	case "quality":
		preset = "4"
		crf = "32"
	case "speed":
		preset = "10"
		crf = "45"
	case "balance":
		preset = "7"
		crf = "40"
	}
	if c.Preset == "" {
		c.Preset = preset
	}
	if c.CRF == "" {
		c.CRF = crf
	}
}

func (c *Config) BuildProcessorConfig() *models.ProcessorConfig {
	return &models.ProcessorConfig{
		Common: c.buildCommonConfig(),
		Video:  c.buildVideoConfig(),
		Audio:  c.buildAudioConfig(),
		Image:  c.buildImageConfig(),
		Text:   c.buildTextConfig(),
	}
}

func (c *Config) buildCommonConfig() models.CommonConfig {
	// Parse memory limit - "0" means no limit, empty string uses default
	limit := utils.ParseSize(c.MemoryLimit)
	if c.MemoryLimit == "0" {
		limit = 0 // No memory limit
	} else if limit == 0 && c.MemoryLimit == "" {
		// Default to 8GB limit when not specified
		limit = 8 * 1024 * 1024 * 1024
	}

	// Parse swap limit - "0" means explicitly disable swap
	swapMax := utils.ParseSize(c.MemorySwapMax)
	if c.MemorySwapMax == "0" {
		swapMax = -1 // Explicitly disable swap
	}

	return models.CommonConfig{
		SourceAudioBitrate: utils.ParseBitrate(c.SourceAudioBitrate),
		SourceVideoBitrate: utils.ParseBitrate(c.SourceVideoBitrate),
		DeleteUnplayable:   c.DeleteUnplayable,
		DeleteLarger:       c.DeleteLarger,
		MoveBroken:         c.MoveBroken,
		Valid:              c.Valid,
		Invalid:            c.Invalid,
		ForceShrink:        c.ForceShrink,
		VerboseFFmpeg:      c.VerboseFFmpeg,
		IncludeTimecode:    c.IncludeTimecode,
		MaxWidthBuffer:     c.MaxWidthBuffer,
		MaxHeightBuffer:    c.MaxHeightBuffer,
		MemoryLimit:        limit,
		MemorySwapMax:      swapMax,
		UseJournald:        c.UseJournald,
		DisableSystemd:     c.DisableSystemd,
	}
}

func (c *Config) buildVideoConfig() models.VideoConfig {
	return models.VideoConfig{
		TargetVideoBitrate:   utils.ParseBitrate(c.TargetVideoBitrate),
		MinSavingsVideo:      utils.ParsePercentOrBytes(c.MinSavingsVideo),
		TranscodingVideoRate: c.TranscodingVideoRate,
		Preset:               c.Preset,
		CRF:                  c.CRF,
		MaxVideoWidth:        c.MaxVideoWidth,
		MaxVideoHeight:       c.MaxVideoHeight,
		VideoOnly:            c.VideoOnly,
		Keyframes:            c.Keyframes,
		NoPreserveVideo:      c.NoPreserveVideo,
	}
}

func (c *Config) buildAudioConfig() models.AudioConfig {
	return models.AudioConfig{
		TargetAudioBitrate:   utils.ParseBitrate(c.TargetAudioBitrate),
		MinSavingsAudio:      utils.ParsePercentOrBytes(c.MinSavingsAudio),
		TranscodingAudioRate: c.TranscodingAudioRate,
		AudioOnly:            c.AudioOnly,
		AlwaysSplit:          c.AlwaysSplit,
		SplitLongerThan:      c.SplitLongerThan,
		MinSplitSegment:      c.MinSplitSegment,
	}
}

func (c *Config) buildImageConfig() models.ImageConfig {
	return models.ImageConfig{
		TargetImageSize:      utils.ParseSize(c.TargetImageSize),
		MinSavingsImage:      utils.ParsePercentOrBytes(c.MinSavingsImage),
		TranscodingImageTime: c.TranscodingImageTime,
		MaxImageWidth:        c.MaxImageWidth,
		MaxImageHeight:       c.MaxImageHeight,
	}
}

func (c *Config) buildTextConfig() models.TextConfig {
	return models.TextConfig{
		SkipOCR:  c.SkipOCR,
		ForceOCR: c.ForceOCR,
		RedoOCR:  c.RedoOCR,
		NoOCR:    c.NoOCR,
	}
}
