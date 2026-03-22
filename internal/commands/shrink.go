package commands

import (
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/alecthomas/kong"
	"github.com/chapmanjacobd/shrink/internal/db"
	"github.com/chapmanjacobd/shrink/internal/ffmpeg"
	"github.com/chapmanjacobd/shrink/internal/models"
	"github.com/chapmanjacobd/shrink/internal/utils"
)

// ShrinkCmd is the main command for shrinking media files
type ShrinkCmd struct {
	// CoreFlags
	Verbose   bool `short:"v" help:"Enable verbose logging"`
	Simulate  bool `help:"Dry run; don't actually do anything"`
	NoConfirm bool `short:"y" help:"Don't ask for confirmation"`

	// PathFilterFlags
	Include []string `short:"s" help:"Include paths matching pattern" group:"PathFilter"`
	Exclude []string `short:"E" help:"Exclude paths matching pattern" group:"PathFilter"`

	// FilterFlags
	Search []string `help:"Search terms" group:"Filter"`

	// MediaFilterFlags
	VideoOnly bool `help:"Only video files" group:"MediaFilter"`
	AudioOnly bool `help:"Only audio files" group:"MediaFilter"`
	ImageOnly bool `help:"Only image files" group:"MediaFilter"`
	TextOnly  bool `help:"Only text/ebook files" group:"MediaFilter"`

	// TimeFilterFlags
	CreatedAfter   string `help:"Created after date" group:"Time"`
	ModifiedAfter  string `help:"Modified after date" group:"Time"`
	ModifiedBefore string `help:"Modified before date" group:"Time"`

	// DeletedFlags
	HideDeleted bool `default:"true" help:"Exclude deleted files" group:"Deleted"`
	OnlyDeleted bool `help:"Include only deleted files" group:"Deleted"`

	Databases []string `arg:"" required:"" help:"SQLite database files or directories to scan"`

	Valid   bool `default:"true" help:"Attempt to process files with valid metadata"`
	Invalid bool `help:"Attempt to process files with invalid metadata"`

	MinSavingsVideo      string  `default:"5%" help:"Minimum savings for video (percentage or bytes)"`
	MinSavingsAudio      string  `default:"10%" help:"Minimum savings for audio (percentage or bytes)"`
	MinSavingsImage      string  `default:"15%" help:"Minimum savings for images (percentage or bytes)"`
	SourceAudioBitrate   string  `default:"256kbps" help:"Used to estimate duration when files are inside of archives or invalid"`
	SourceVideoBitrate   string  `default:"1500kbps" help:"Used to estimate duration when files are inside of archives or invalid"`
	TargetAudioBitrate   string  `default:"128kbps" help:"Target audio bitrate"`
	TargetVideoBitrate   string  `default:"800kbps" help:"Target video bitrate"`
	TargetImageSize      string  `default:"30KiB" help:"Target image size"`
	TranscodingVideoRate float64 `default:"1.8" help:"Ratio of duration eg. 4x realtime speed"`
	TranscodingAudioRate float64 `default:"150" help:"Ratio of duration eg. 100x realtime speed"`
	TranscodingImageTime float64 `default:"1.5" help:"Seconds to process an image"`

	MaxVideoHeight int    `default:"960" help:"Maximum video height"`
	MaxVideoWidth  int    `default:"1440" help:"Maximum video width"`
	MaxImageHeight int    `default:"2400" help:"Maximum image height"`
	MaxImageWidth  int    `default:"2400" help:"Maximum image width"`
	Preset         string `default:"7" help:"SVT-AV1 preset (0-13, lower is slower/better)"`
	CRF            string `default:"40" help:"CRF value for SVT-AV1 (0-63, lower is better)"`

	ContinueFrom     string  `help:"Skip media until specific file path is seen"`
	Move             string  `help:"Directory to move successful files"`
	MoveBroken       string  `help:"Directory to move unsuccessful files"`
	DeleteUnplayable bool    `help:"Delete unplayable files"`
	DeleteLarger     bool    `default:"true" help:"Delete larger of transcode or original files"`
	AlwaysSplit      bool    `help:"Always split audio on silence"`
	SplitLongerThan  float64 `help:"Split audio longer than N seconds"`
	MinSplitSegment  float64 `default:"60" help:"Minimum split segment duration in seconds"`
	MaxWidthBuffer   float64 `default:"0.05" help:"Buffer percentage for width upscaling"`
	MaxHeightBuffer  float64 `default:"0.05" help:"Buffer percentage for height upscaling"`
	Keyframes        bool    `help:"Extract keyframes only"`
	NoPreserveVideo  bool    `help:"Don't preserve video when audio-only"`
	IncludeTimecode  bool    `help:"Include timecode streams in output"`
	VerboseFFmpeg    bool    `help:"Enable verbose FFmpeg logging"`
	SkipOCR          bool    `help:"Skip OCR for PDFs that already contain text"`
	ForceOCR         bool    `help:"Force OCR even on PDFs with text"`
	RedoOCR          bool    `help:"Re-do OCR on PDFs that already have OCR"`
	NoOCR            bool    `help:"Skip OCR entirely"`

	// Parallelism
	VideoThreads int `default:"2" help:"Maximum concurrent video transcodes"`
	AudioThreads int `default:"4" help:"Maximum concurrent audio transcodes"`
	ImageThreads int `default:"8" help:"Maximum concurrent image conversions"`
	TextThreads  int `default:"2" help:"Maximum concurrent text conversions"`

	// Timeouts
	VideoTimeoutMult float64 `default:"3.0" help:"Video timeout multiplier (timeout = duration * multiplier)"`
	AudioTimeoutMult float64 `default:"0.5" help:"Audio timeout multiplier (timeout = duration * multiplier)"`
	VideoTimeout     string  `default:"90m" help:"Video timeout when duration is unknown"`
	AudioTimeout     string  `default:"10m" help:"Audio timeout when duration is unknown"`
	ImageTimeout     string  `default:"10m" help:"Image timeout"`
	TextTimeout      string  `default:"20m" help:"Text timeout"`

	ForceShrink bool `help:"Force reprocessing of already shrinked files"`

	sqlDBs []*sql.DB
}

func (c *ShrinkCmd) Run(ctx *kong.Context) error {
	models.SetupLogging(c.Verbose)
	defer c.closeDatabases()

	// Build processor configuration
	cfg := c.buildProcessorConfig()

	// Check installed tools
	tools := c.checkInstalledTools()

	// Initialize databases
	if err := c.initDatabases(); err != nil {
		return err
	}

	// Initialize components
	ffmpeg := ffmpeg.NewFFmpegProcessor(cfg)
	registry := NewProcessorRegistry(ffmpeg)
	metrics := NewShrinkMetrics()

	// Wrap the default logger to coordinate with the progress bar
	defaultHandler := &models.PlainHandler{
		Level: models.LogLevel,
		Out:   os.Stderr,
	}
	slog.SetDefault(slog.New(NewProgressLogHandler(defaultHandler, metrics)))

	// Load all media from databases
	allMedia, err := c.loadAllMedia()
	if err != nil {
		return err
	}

	slog.Info("Loaded media", "count", len(allMedia))
	if len(allMedia) == 0 {
		slog.Info("No media found")
		return nil
	}

	// Filter by available tools
	filteredMedia := c.filterByTools(allMedia, tools)
	slog.Info("Filtered media by tools",
		"count", len(filteredMedia),
		"ffmpeg", tools.FFmpeg,
		"magick", tools.ImageMagick,
		"calibre", tools.Calibre)

	if len(filteredMedia) == 0 {
		slog.Info("No processable media found")
		return nil
	}

	// Analyze and decide what to shrink
	toShrink := c.analyzeMedia(filteredMedia, cfg, registry, metrics)
	if len(toShrink) == 0 {
		fmt.Println("No files to shrink")
		metrics.LogSummary()
		return nil
	}

	// Deduplicate by path to prevent processing the same file multiple times
	// This can happen with archives or multiple database inputs
	seenPaths := make(map[string]bool)
	deduped := make([]models.ShrinkMedia, 0, len(toShrink))
	for _, m := range toShrink {
		if !seenPaths[m.Path] {
			seenPaths[m.Path] = true
			deduped = append(deduped, m)
		}
	}
	toShrink = deduped

	// Apply continue-from filter
	if c.ContinueFrom != "" {
		toShrink = c.applyContinueFrom(toShrink)
	}

	// Sort by efficiency (most space freed per second)
	c.sortByEfficiency(toShrink)

	// Print summary
	c.printSummary(toShrink)

	if c.Simulate {
		fmt.Println("Simulation mode - no files will be processed")
		return nil
	}

	// Confirm
	if !c.NoConfirm && !c.confirm() {
		return nil
	}

	// Process with parallelism
	c.processMedia(toShrink, registry, cfg, metrics)

	// Final summary
	metrics.LogSummary()
	return nil
}

func (c *ShrinkCmd) buildProcessorConfig() *models.ProcessorConfig {
	return &models.ProcessorConfig{
		Common: models.CommonConfig{
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
		},
		Video: models.VideoConfig{
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
		},
		Audio: models.AudioConfig{
			TargetAudioBitrate:   utils.ParseBitrate(c.TargetAudioBitrate),
			MinSavingsAudio:      utils.ParsePercentOrBytes(c.MinSavingsAudio),
			TranscodingAudioRate: c.TranscodingAudioRate,
			AudioOnly:            c.AudioOnly,
			AlwaysSplit:          c.AlwaysSplit,
			SplitLongerThan:      c.SplitLongerThan,
			MinSplitSegment:      c.MinSplitSegment,
		},
		Image: models.ImageConfig{
			TargetImageSize:      utils.ParseSize(c.TargetImageSize),
			MinSavingsImage:      utils.ParsePercentOrBytes(c.MinSavingsImage),
			TranscodingImageTime: c.TranscodingImageTime,
			MaxImageWidth:        c.MaxImageWidth,
			MaxImageHeight:       c.MaxImageHeight,
		},
		Text: models.TextConfig{
			SkipOCR:  c.SkipOCR,
			ForceOCR: c.ForceOCR,
			RedoOCR:  c.RedoOCR,
			NoOCR:    c.NoOCR,
		},
	}
}

func (c *ShrinkCmd) initDatabases() error {
	for _, dbPath := range c.Databases {
		if db.IsDatabaseFile(dbPath) {
			sqlDB, _, err := db.ConnectWithInit(dbPath)
			if err != nil {
				return err
			}
			c.sqlDBs = append(c.sqlDBs, sqlDB)
		}
	}
	return nil
}

func (c *ShrinkCmd) closeDatabases() {
	for _, sqlDB := range c.sqlDBs {
		if sqlDB != nil {
			sqlDB.Close()
		}
	}
}

func (c *ShrinkCmd) loadAllMedia() ([]models.ShrinkMedia, error) {
	var allMedia []models.ShrinkMedia

	// Load from opened databases
	for _, sqlDB := range c.sqlDBs {
		records, err := db.LoadMediaFromDB(sqlDB, c.ForceShrink, c.VideoOnly, c.AudioOnly, c.ImageOnly, c.TextOnly)
		if err != nil {
			return nil, err
		}

		for _, r := range records {
			allMedia = append(allMedia, models.ShrinkMedia{
				Path:           r.Path,
				Size:           r.Size,
				Duration:       r.Duration,
				VideoCount:     r.VideoCount,
				AudioCount:     r.AudioCount,
				VideoCodecs:    r.VideoCodecs,
				AudioCodecs:    r.AudioCodecs,
				SubtitleCodecs: r.SubtitleCodecs,
				MediaType:      r.MediaType,
				Ext:            strings.ToLower(filepath.Ext(r.Path)),
			})
		}
	}

	// Scan directories
	for _, dbPath := range c.Databases {
		if !db.IsDatabaseFile(dbPath) {
			media, err := c.scanDirectory(dbPath)
			if err != nil {
				return nil, err
			}
			allMedia = append(allMedia, media...)
		}
	}

	return allMedia, nil
}

func (c *ShrinkCmd) filterByTools(media []models.ShrinkMedia, tools InstalledTools) []models.ShrinkMedia {
	filtered := make([]models.ShrinkMedia, 0, len(media))

	for _, m := range media {
		canProcess := false

		// Audio/Video
		if (m.MediaType == "audio" || (utils.AudioExtensionMap[m.Ext] && m.VideoCount == 0)) ||
			(m.MediaType == "video" || (utils.VideoExtensionMap[m.Ext] && m.VideoCount >= 1)) {
			canProcess = tools.FFmpeg
		}
		// Image
		if m.MediaType == "image" || (utils.ImageExtensionMap[m.Ext] && m.Duration == 0) {
			canProcess = tools.ImageMagick
		}
		// Text
		if m.MediaType == "text" || utils.TextExtensionMap[m.Ext] {
			canProcess = tools.Calibre
		}
		// Archives
		if m.MediaType == "archive" || utils.ArchiveExtensionMap[m.Ext] {
			canProcess = tools.Unar
		}

		if canProcess {
			filtered = append(filtered, m)
		}
	}

	return filtered
}

func (c *ShrinkCmd) moveToBroken(path string, partFiles []string) {
	if c.MoveBroken == "" || path == "" {
		return
	}

	// Get the parent folder name for tidy organization
	parentFolder := filepath.Base(filepath.Dir(path))
	destDir := filepath.Join(c.MoveBroken, parentFolder)

	// Move the main file
	if _, err := os.Stat(path); err == nil {
		os.MkdirAll(destDir, 0o755)
		dest := filepath.Join(destDir, filepath.Base(path))
		if err := os.Rename(path, dest); err != nil {
			slog.Warn("Failed to move broken file", "from", path, "to", dest, "error", err)
		} else {
			slog.Info("Moved broken file", "from", path, "to", dest)
		}
	}

	// Move multi-part archive files if present
	for _, partFile := range partFiles {
		if !filepath.IsAbs(partFile) {
			partFile = filepath.Join(filepath.Dir(path), partFile)
		}
		if _, err := os.Stat(partFile); err == nil {
			os.MkdirAll(destDir, 0o755)
			dest := filepath.Join(destDir, filepath.Base(partFile))
			if err := os.Rename(partFile, dest); err != nil {
				slog.Warn("Failed to move broken archive part", "from", partFile, "to", dest, "error", err)
			} else {
				slog.Info("Moved broken archive part", "from", partFile, "to", dest)
			}
		}
	}
}

func (c *ShrinkCmd) moveTo(path string) {
	if c.Move != "" && path != "" {
		dest := filepath.Join(c.Move, filepath.Base(path))
		if err := os.Rename(path, dest); err != nil {
			slog.Warn("Failed to move file", "from", path, "to", dest, "error", err)
		}
	}
}
