package commands

// Config contains all command-line flags and configuration for the shrink application
type Config struct {
	// CoreFlags
	Verbose   bool `short:"v" help:"Enable verbose logging" env:"SHRINK_VERBOSE"`
	Simulate  bool `help:"Dry run; don't actually do anything" env:"SHRINK_SIMULATE"`
	NoConfirm bool `short:"y" help:"Don't ask for confirmation" env:"SHRINK_NO_CONFIRM"`

	// Profile preset
	Profile string `default:"balance" help:"Profile preset: quality, speed, balance (default to balance preset 7 crf 40)" enum:"quality,speed,balance" env:"SHRINK_PROFILE"`

	// PathFilterFlags
	Include []string `short:"s" help:"Include paths matching pattern" group:"PathFilter" env:"SHRINK_INCLUDE"`
	Exclude []string `short:"E" help:"Exclude paths matching pattern" group:"PathFilter" env:"SHRINK_EXCLUDE"`

	// FilterFlags
	Search []string `help:"Search terms" group:"Filter" env:"SHRINK_SEARCH"`

	// MediaFilterFlags
	VideoOnly bool `help:"Only video files" group:"MediaFilter" env:"SHRINK_VIDEO_ONLY"`
	AudioOnly bool `help:"Only audio files" group:"MediaFilter" env:"SHRINK_AUDIO_ONLY"`
	ImageOnly bool `help:"Only image files" group:"MediaFilter" env:"SHRINK_IMAGE_ONLY"`
	TextOnly  bool `help:"Only text/ebook files" group:"MediaFilter" env:"SHRINK_TEXT_ONLY"`

	// TimeFilterFlags
	CreatedAfter   string `help:"Created after date" group:"Time" env:"SHRINK_CREATED_AFTER"`
	ModifiedAfter  string `help:"Modified after date" group:"Time" env:"SHRINK_MODIFIED_AFTER"`
	ModifiedBefore string `help:"Modified before date" group:"Time" env:"SHRINK_MODIFIED_BEFORE"`

	// DeletedFlags
	HideDeleted bool `default:"true" help:"Exclude deleted files" group:"Deleted" env:"SHRINK_HIDE_DELETED"`
	OnlyDeleted bool `help:"Include only deleted files" group:"Deleted" env:"SHRINK_ONLY_DELETED"`

	Valid   bool `default:"true" help:"Attempt to process files with valid metadata" env:"SHRINK_VALID"`
	Invalid bool `help:"Attempt to process files with invalid metadata" env:"SHRINK_INVALID"`

	MinSavingsVideo      string  `default:"5%" help:"Minimum savings for video (percentage or bytes)" env:"SHRINK_MIN_SAVINGS_VIDEO"`
	MinSavingsAudio      string  `default:"10%" help:"Minimum savings for audio (percentage or bytes)" env:"SHRINK_MIN_SAVINGS_AUDIO"`
	MinSavingsImage      string  `default:"15%" help:"Minimum savings for images (percentage or bytes)" env:"SHRINK_MIN_SAVINGS_IMAGE"`
	SourceAudioBitrate   string  `default:"256kbps" help:"Used to estimate duration when files are inside of archives or invalid" env:"SHRINK_SOURCE_AUDIO_BITRATE"`
	SourceVideoBitrate   string  `default:"1500kbps" help:"Used to estimate duration when files are inside of archives or invalid" env:"SHRINK_SOURCE_VIDEO_BITRATE"`
	TargetAudioBitrate   string  `default:"128kbps" help:"Target audio bitrate" env:"SHRINK_TARGET_AUDIO_BITRATE"`
	TargetVideoBitrate   string  `default:"800kbps" help:"Target video bitrate" env:"SHRINK_TARGET_VIDEO_BITRATE"`
	TargetImageSize      string  `default:"30KiB" help:"Target image size" env:"SHRINK_TARGET_IMAGE_SIZE"`
	TranscodingVideoRate float64 `default:"1.8" help:"Ratio of duration eg. 4x realtime speed" env:"SHRINK_TRANSCODING_VIDEO_RATE"`
	TranscodingAudioRate float64 `default:"150" help:"Ratio of duration eg. 100x realtime speed" env:"SHRINK_TRANSCODING_AUDIO_RATE"`
	TranscodingImageTime float64 `default:"1.5" help:"Seconds to process an image" env:"SHRINK_TRANSCODING_IMAGE_TIME"`

	MaxVideoHeight int    `default:"960" help:"Maximum video height" env:"SHRINK_MAX_VIDEO_HEIGHT"`
	MaxVideoWidth  int    `default:"1440" help:"Maximum video width" env:"SHRINK_MAX_VIDEO_WIDTH"`
	MaxImageHeight int    `default:"2400" help:"Maximum image height" env:"SHRINK_MAX_IMAGE_HEIGHT"`
	MaxImageWidth  int    `default:"2400" help:"Maximum image width" env:"SHRINK_MAX_IMAGE_WIDTH"`
	Preset         string `help:"SVT-AV1 preset (0-13, lower is slower/better) (default depends on profile)" env:"SHRINK_PRESET"`
	CRF            string `help:"CRF value for SVT-AV1 (0-63, lower is better) (default depends on profile)" env:"SHRINK_CRF"`

	ContinueFrom     string  `help:"Skip media until specific file path is seen" env:"SHRINK_CONTINUE_FROM"`
	Move             string  `help:"Directory to move successful files" env:"SHRINK_MOVE"`
	MoveBroken       string  `help:"Directory to move unsuccessful files" env:"SHRINK_MOVE_BROKEN"`
	DeleteUnplayable bool    `help:"Delete unplayable files" env:"SHRINK_DELETE_UNPLAYABLE"`
	DeleteLarger     bool    `default:"true" help:"Delete larger of transcode or original files" env:"SHRINK_DELETE_LARGER"`
	AlwaysSplit      bool    `help:"Always split audio on silence" env:"SHRINK_ALWAYS_SPLIT"`
	SplitLongerThan  float64 `help:"Split audio longer than N seconds" env:"SHRINK_SPLIT_LONGER_THAN"`
	MinSplitSegment  float64 `default:"60" help:"Minimum split segment duration in seconds" env:"SHRINK_MIN_SPLIT_SEGMENT"`
	MaxWidthBuffer   float64 `default:"0.05" help:"Buffer percentage for width upscaling" env:"SHRINK_MAX_WIDTH_BUFFER"`
	MaxHeightBuffer  float64 `default:"0.05" help:"Buffer percentage for height upscaling" env:"SHRINK_MAX_HEIGHT_BUFFER"`
	Keyframes        bool    `help:"Extract keyframes only" env:"SHRINK_KEYFRAMES"`
	NoPreserveVideo  bool    `help:"Don't preserve video when audio-only" env:"SHRINK_NO_PRESERVE_VIDEO"`
	IncludeTimecode  bool    `help:"Include timecode streams in output" env:"SHRINK_INCLUDE_TIMECODE"`
	VerboseFFmpeg    bool    `help:"Enable verbose FFmpeg logging" env:"SHRINK_VERBOSE_FFMPEG"`
	SkipOCR          bool    `help:"Skip OCR for PDFs that already contain text" env:"SHRINK_SKIP_OCR"`
	ForceOCR         bool    `help:"Force OCR even on PDFs with text" env:"SHRINK_FORCE_OCR"`
	RedoOCR          bool    `help:"Re-do OCR on PDFs that already have OCR" env:"SHRINK_REDO_OCR"`
	NoOCR            bool    `help:"Skip OCR entirely" env:"SHRINK_NO_OCR"`

	// Parallelism
	VideoThreads int `default:"2" help:"Maximum concurrent video transcodes" env:"SHRINK_VIDEO_THREADS"`
	AudioThreads int `default:"4" help:"Maximum concurrent audio transcodes" env:"SHRINK_AUDIO_THREADS"`
	ImageThreads int `default:"8" help:"Maximum concurrent image conversions" env:"SHRINK_IMAGE_THREADS"`
	TextThreads  int `default:"2" help:"Maximum concurrent text conversions" env:"SHRINK_TEXT_THREADS"`

	// Timeouts
	VideoTimeoutMult float64 `default:"3.0" help:"Video timeout multiplier (timeout = duration * multiplier)" env:"SHRINK_VIDEO_TIMEOUT_MULT"`
	AudioTimeoutMult float64 `default:"0.5" help:"Audio timeout multiplier (timeout = duration * multiplier)" env:"SHRINK_AUDIO_TIMEOUT_MULT"`
	VideoTimeout     string  `default:"90m" help:"Video timeout when duration is unknown" env:"SHRINK_VIDEO_TIMEOUT"`
	AudioTimeout     string  `default:"10m" help:"Audio timeout when duration is unknown" env:"SHRINK_AUDIO_TIMEOUT"`
	ImageTimeout     string  `default:"10m" help:"Image timeout" env:"SHRINK_IMAGE_TIMEOUT"`
	TextTimeout      string  `default:"20m" help:"Text timeout" env:"SHRINK_TEXT_TIMEOUT"`

	ForceShrink bool `help:"Force reprocessing of already shrinked files" env:"SHRINK_FORCE_SHRINK"`
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
