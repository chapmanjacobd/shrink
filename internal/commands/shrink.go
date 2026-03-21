package commands

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/alecthomas/kong"
	"github.com/chapmanjacobd/shrink/internal/db"
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
}

// ShrinkMedia represents a media file to be processed
type ShrinkMedia struct {
	Path           string
	Size           int64
	Duration       float64
	VideoCount     int
	AudioCount     int
	SubtitleCount  int
	Width          int
	Height         int
	VideoCodecs    string
	AudioCodecs    string
	SubtitleCodecs string
	MediaType      string
	Ext            string
	Category       string
	FutureSize     int64
	Savings        int64
	ProcessingTime int
	CompressedSize int64
	ArchivePath    string
	NewPath        string
	NewSize        int64
	TimeDeleted    int64
	Invalid        bool
	IsBroken       bool     // For archives: lsar failed to read contents
	PartFiles      []string // For multi-part archives: list of all part files
}

func (c *ShrinkCmd) Run(ctx *kong.Context) error {
	models.SetupLogging(c.Verbose)

	// Build processor configuration
	cfg := c.buildProcessorConfig()

	// Check installed tools
	tools := c.checkInstalledTools()

	// Initialize components
	ffmpeg := NewFFmpegProcessor(cfg)
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

func (c *ShrinkCmd) buildProcessorConfig() *ProcessorConfig {
	return &ProcessorConfig{
		SourceAudioBitrate:   utils.ParseBitrate(c.SourceAudioBitrate),
		SourceVideoBitrate:   utils.ParseBitrate(c.SourceVideoBitrate),
		TargetAudioBitrate:   utils.ParseBitrate(c.TargetAudioBitrate),
		TargetVideoBitrate:   utils.ParseBitrate(c.TargetVideoBitrate),
		TargetImageSize:      utils.ParseSize(c.TargetImageSize),
		MinSavingsVideo:      utils.ParsePercentOrBytes(c.MinSavingsVideo),
		MinSavingsAudio:      utils.ParsePercentOrBytes(c.MinSavingsAudio),
		MinSavingsImage:      utils.ParsePercentOrBytes(c.MinSavingsImage),
		TranscodingVideoRate: c.TranscodingVideoRate,
		TranscodingAudioRate: c.TranscodingAudioRate,
		TranscodingImageTime: c.TranscodingImageTime,
		Preset:               c.Preset,
		CRF:                  c.CRF,
		MaxVideoWidth:        c.MaxVideoWidth,
		MaxVideoHeight:       c.MaxVideoHeight,
		Keyframes:            c.Keyframes,
		AudioOnly:            c.AudioOnly,
		VideoOnly:            c.VideoOnly,
		AlwaysSplit:          c.AlwaysSplit,
		SplitLongerThan:      c.SplitLongerThan,
		MinSplitSegment:      c.MinSplitSegment,
		MaxWidthBuffer:       c.MaxWidthBuffer,
		MaxHeightBuffer:      c.MaxHeightBuffer,
		NoPreserveVideo:      c.NoPreserveVideo,
		IncludeTimecode:      c.IncludeTimecode,
		VerboseFFmpeg:        c.VerboseFFmpeg,
		SkipOCR:              c.SkipOCR,
		ForceOCR:             c.ForceOCR,
		RedoOCR:              c.RedoOCR,
		NoOCR:                c.NoOCR,
		DeleteUnplayable:     c.DeleteUnplayable,
		DeleteLarger:         c.DeleteLarger,
		MoveBroken:           c.MoveBroken,
		Valid:                c.Valid,
		Invalid:              c.Invalid,
		ForceShrink:          c.ForceShrink,
	}
}

func (c *ShrinkCmd) loadAllMedia() ([]ShrinkMedia, error) {
	var allMedia []ShrinkMedia

	for _, dbPath := range c.Databases {
		// Check if this is a database file or a directory
		if isDatabaseFile(dbPath) {
			sqlDB, _, err := db.ConnectWithInit(dbPath)
			if err != nil {
				return nil, err
			}

			media, err := c.loadMediaFromDB(sqlDB)
			sqlDB.Close()
			if err != nil {
				return nil, err
			}
			allMedia = append(allMedia, media...)
		} else {
			// Treat as directory and scan for media files
			media, err := c.scanDirectory(dbPath)
			if err != nil {
				return nil, err
			}
			allMedia = append(allMedia, media...)
		}
	}

	return allMedia, nil
}

// isDatabaseFile checks if a path is a SQLite database file
func isDatabaseFile(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	if slices.Contains(utils.SQLiteExtensions, ext) {
		return true
	}
	// Also check if file exists and has .db extension pattern
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	if !info.IsDir() {
		// File exists and is not a directory - treat as database if it has unknown extension
		// This allows explicit database files without standard extensions
		for _, dbExt := range utils.SQLiteExtensions {
			if strings.HasSuffix(strings.ToLower(path), dbExt) {
				return true
			}
		}
	}
	return false
}

// scanDirectory scans a directory recursively for media files
func (c *ShrinkCmd) scanDirectory(dirPath string) ([]ShrinkMedia, error) {
	var media []ShrinkMedia

	err := filepath.Walk(dirPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			slog.Warn("Error accessing path", "path", path, "error", err)
			return nil
		}

		// Skip directories
		if info.IsDir() {
			return nil
		}

		// Skip hidden files
		if strings.HasPrefix(info.Name(), ".") {
			return nil
		}

		ext := strings.ToLower(filepath.Ext(path))

		// Check if this is a media file
		isMedia := utils.MediaExtensionMap[ext] || utils.ArchiveExtensionMap[ext]
		if !isMedia {
			return nil
		}

		// Apply media type filters if specified
		if c.VideoOnly && !utils.VideoExtensionMap[ext] {
			return nil
		}
		if c.AudioOnly && !utils.AudioExtensionMap[ext] {
			return nil
		}
		if c.ImageOnly && !utils.ImageExtensionMap[ext] {
			return nil
		}
		if c.TextOnly && !utils.TextExtensionMap[ext] {
			return nil
		}

		// Create media entry with basic info
		m := ShrinkMedia{
			Path:      path,
			Size:      info.Size(),
			Ext:       ext,
			MediaType: detectMediaTypeFromExt(ext),
		}

		// Set video/audio counts for extension-based detection
		if utils.VideoExtensionMap[ext] {
			m.VideoCount = 1
		}
		if utils.AudioExtensionMap[ext] {
			m.AudioCount = 1
		}

		// Try to get accurate metadata using ffprobe for video/audio files
		if utils.VideoExtensionMap[ext] || utils.AudioExtensionMap[ext] {
			if probed, err := c.probeMedia(path); err == nil {
				m.Duration = probed.Duration
				if probed.VideoCount > 0 {
					m.VideoCount = probed.VideoCount
				}
				if probed.AudioCount > 0 {
					m.AudioCount = probed.AudioCount
				}
				m.SubtitleCount = probed.SubtitleCount
				if probed.VideoCodecs != "" {
					m.VideoCodecs = probed.VideoCodecs
				}
				if probed.AudioCodecs != "" {
					m.AudioCodecs = probed.AudioCodecs
				}
				if probed.SubtitleCodecs != "" {
					m.SubtitleCodecs = probed.SubtitleCodecs
				}
			}
		}

		media = append(media, m)
		return nil
	})
	if err != nil {
		return nil, err
	}

	return media, nil
}

// probeMedia uses ffprobe to get accurate stream counts and metadata
func (c *ShrinkCmd) probeMedia(path string) (*ShrinkMedia, error) {
	if !utils.CommandExists("ffprobe") {
		return nil, fmt.Errorf("ffprobe not available")
	}

	cmd := exec.Command("ffprobe",
		"-v", "quiet",
		"-hide_banner",
		"-show_format",
		"-show_streams",
		"-of", "json",
		path)

	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	var data struct {
		Streams []struct {
			CodecType   string            `json:"codec_type"`
			CodecName   string            `json:"codec_name"`
			Width       int               `json:"width"`
			Height      int               `json:"height"`
			RFrameRate  string            `json:"r_frame_rate"`
			Channels    int               `json:"channels"`
			SampleRate  string            `json:"sample_rate"`
			Tags        map[string]string `json:"tags"`
			Disposition map[string]int    `json:"disposition"`
		} `json:"streams"`
		Format struct {
			Duration string            `json:"duration"`
			Tags     map[string]string `json:"tags"`
		} `json:"format"`
	}

	if err := json.Unmarshal(output, &data); err != nil {
		return nil, err
	}

	m := &ShrinkMedia{
		Path: path,
	}

	var vCodecs, aCodecs, sCodecs []string

	for _, s := range data.Streams {
		switch s.CodecType {
		case "video":
			// Skip attached pics (album art)
			if s.Disposition["attached_pic"] == 1 || s.CodecName == "mjpeg" || s.CodecName == "png" {
				continue
			}
			m.VideoCount++
			codecInfo := s.CodecName
			vCodecs = append(vCodecs, codecInfo)

			if m.Width == 0 {
				m.Width = s.Width
				m.Height = s.Height
			}
		case "audio":
			m.AudioCount++
			codecInfo := s.CodecName
			if s.Channels > 0 {
				codecInfo += fmt.Sprintf(" %dch", s.Channels)
			}
			aCodecs = append(aCodecs, codecInfo)
		case "subtitle":
			m.SubtitleCount++
			label := s.CodecName
			if lang := s.Tags["language"]; lang != "" {
				label = lang
			}
			sCodecs = append(sCodecs, label)
		}
	}

	// Format info
	if d, err := strconv.ParseFloat(data.Format.Duration, 64); err == nil {
		m.Duration = d
	}

	m.VideoCodecs = strings.Join(vCodecs, ", ")
	m.AudioCodecs = strings.Join(aCodecs, ", ")
	m.SubtitleCodecs = strings.Join(sCodecs, ", ")

	return m, nil
}

func (c *ShrinkCmd) loadMediaFromDB(sqlDB *sql.DB) ([]ShrinkMedia, error) {
	query := `
		SELECT path,
            size,
            COALESCE(duration, 0),
            COALESCE(video_count, 0),
            COALESCE(audio_count, 0),
            COALESCE(video_codecs, ''),
            COALESCE(audio_codecs, ''),
            COALESCE(subtitle_codecs, ''),
            COALESCE(media_type, '')
		FROM media
		WHERE COALESCE(time_deleted, 0) = 0
            AND size > 0
	`

	if !c.ForceShrink {
		query += " AND COALESCE(is_shrinked, 0) = 0"
	}

	// Filter by media type (prefilter in database)
	var typeConditions []string
	if c.VideoOnly {
		typeConditions = append(typeConditions, "media_type = 'video'")
	}
	if c.AudioOnly {
		typeConditions = append(typeConditions, "media_type = 'audio'", "media_type = 'audiobook'")
	}
	if c.ImageOnly {
		typeConditions = append(typeConditions, "media_type = 'image'")
	}
	if c.TextOnly {
		typeConditions = append(typeConditions, "media_type = 'text'")
	}
	if len(typeConditions) > 0 {
		query += " AND (" + strings.Join(typeConditions, " OR ") + ")"
	}

	rows, err := sqlDB.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var media []ShrinkMedia
	for rows.Next() {
		var m ShrinkMedia
		err := rows.Scan(&m.Path, &m.Size, &m.Duration, &m.VideoCount, &m.AudioCount,
			&m.VideoCodecs, &m.AudioCodecs, &m.SubtitleCodecs, &m.MediaType)
		if err != nil {
			slog.Error("Scan error", "error", err)
			continue
		}
		m.Ext = strings.ToLower(filepath.Ext(m.Path))
		media = append(media, m)
	}

	return media, rows.Err()
}

func (c *ShrinkCmd) filterByTools(media []ShrinkMedia, tools InstalledTools) []ShrinkMedia {
	filtered := make([]ShrinkMedia, 0, len(media))

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

func (c *ShrinkCmd) analyzeMedia(media []ShrinkMedia, cfg *ProcessorConfig,
	registry *ProcessorRegistry, metrics *ShrinkMetrics,
) []ShrinkMedia {
	var toShrink []ShrinkMedia

	for i := range media {
		m := &media[i]
		processor := registry.GetProcessor(m)
		if processor == nil {
			metrics.RecordSkipped("Unknown")
			continue
		}

		// Get processor's category
		m.Category = processor.Category()
		metrics.RecordStarted(m.Category, m.Path)

		// Estimate size and time
		futureSize, processingTime := processor.EstimateSize(m, cfg)

		// For archives, check if they contain processable content
		if m.Category == "Archived" {
			if archiveProc, ok := processor.(*ArchiveProcessor); ok {
				var hasProcessable bool
				var totalArchiveSize int64
				futureSize, processingTime, hasProcessable, totalArchiveSize = archiveProc.EstimateSizeForArchive(m, cfg)
				if !hasProcessable {
					// Check if archive is broken (lsar returned empty or parts missing)
					// totalArchiveSize == 0 means archive is broken (missing parts or unreadable)
					// totalArchiveSize == m.Size means lsar worked but found no processable content
					if totalArchiveSize == 0 {
						m.IsBroken = true
						// Get part files for multi-part archives
						m.PartFiles = archiveProc.getPartFiles(m.Path)
						// Add broken archives to toShrink so they can be moved to --move-broken
						toShrink = append(toShrink, *m)
					}
					metrics.RecordSkipped(m.Category)
					continue
				}
				// Use total archive size for multi-part archives
				if totalArchiveSize > 0 {
					m.Size = totalArchiveSize
				}
			}
		}

		// Check if we should shrink
		if ShouldShrink(m, futureSize, cfg) {
			m.FutureSize = futureSize
			m.ProcessingTime = processingTime
			m.Savings = m.Size - futureSize
			toShrink = append(toShrink, *m)
		} else {
			metrics.RecordSkipped(m.Category)
		}
	}

	return toShrink
}

func (c *ShrinkCmd) applyContinueFrom(media []ShrinkMedia) []ShrinkMedia {
	found := false
	var filtered []ShrinkMedia

	for _, m := range media {
		if m.Path == c.ContinueFrom {
			found = true
		}
		if found {
			filtered = append(filtered, m)
		}
	}

	return filtered
}

func (c *ShrinkCmd) sortByEfficiency(media []ShrinkMedia) {
	sort.Slice(media, func(i, j int) bool {
		timeI := max(media[i].ProcessingTime, 1)
		timeJ := max(media[j].ProcessingTime, 1)
		ratioI := float64(media[i].Savings) / float64(timeI)
		ratioJ := float64(media[j].Savings) / float64(timeJ)
		return ratioI > ratioJ
	})
}

func (c *ShrinkCmd) printSummary(media []ShrinkMedia) {
	var totalSize, totalFuture, totalSavings int64
	var totalTime int
	typeBreakdown := make(map[string]struct {
		count    int
		size     int64
		future   int64
		savings  int64
		procTime int
	})

	for _, m := range media {
		totalSize += m.Size
		totalFuture += m.FutureSize
		totalSavings += m.Savings
		totalTime += m.ProcessingTime

		key := m.Category
		if m.MediaType != "" {
			key = fmt.Sprintf("%s: %s", m.Category, strings.TrimPrefix(m.MediaType, "video/"))
		}
		b := typeBreakdown[key]
		b.count++
		b.size += m.Size
		b.future += m.FutureSize
		b.savings += m.Savings
		b.procTime += m.ProcessingTime
		typeBreakdown[key] = b
	}

	// Print summary table
	fmt.Println()
	fmt.Printf("%-24s %6s %12s %12s %12s %12s %12s\n",
		"Media Type", "Count", "Current", "Future", "Savings", "Proc Time", "Speed")
	fmt.Println(strings.Repeat("-", 95))

	// Sort keys for consistent output
	keys := make([]string, 0, len(typeBreakdown))
	for k := range typeBreakdown {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, key := range keys {
		b := typeBreakdown[key]
		speed := ""
		if b.procTime > 0 {
			ratio := float64(b.size) / float64(b.future)
			speed = fmt.Sprintf("%.1fx", ratio)
		}
		fmt.Printf("%-24s %6d %12s %12s %12s %12s %12s\n",
			key, b.count,
			utils.FormatSize(b.size),
			utils.FormatSize(b.future),
			utils.FormatSize(b.savings),
			utils.FormatDuration(b.procTime),
			speed)
	}
	fmt.Println(strings.Repeat("-", 95))
	fmt.Printf("%-24s %6s %12s %12s %12s %12s\n",
		"TOTAL", "",
		utils.FormatSize(totalSize),
		utils.FormatSize(totalFuture),
		utils.FormatSize(totalSavings),
		utils.FormatDuration(totalTime))
	fmt.Println()
}

func (c *ShrinkCmd) confirm() bool {
	fmt.Print("Proceed with shrinking? [y/N] ")
	var response string
	fmt.Scanln(&response)
	return strings.ToLower(response) == "y"
}

func (c *ShrinkCmd) processMedia(media []ShrinkMedia, registry *ProcessorRegistry,
	cfg *ProcessorConfig, metrics *ShrinkMetrics,
) {
	// Create worker pools
	sems := map[string]chan struct{}{
		"Video":    make(chan struct{}, c.VideoThreads),
		"Audio":    make(chan struct{}, c.AudioThreads),
		"Image":    make(chan struct{}, c.ImageThreads),
		"Text":     make(chan struct{}, c.TextThreads),
		"Archived": make(chan struct{}, 1),
	}

	var wg sync.WaitGroup
	done := make(chan struct{})

	// Progress printer goroutine
	go func() {
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				metrics.PrintProgress()
			case <-done:
				metrics.PrintProgress() // Final update
				return
			}
		}
	}()

	for _, m := range media {
		wg.Add(1)
		go func(original ShrinkMedia) {
			defer wg.Done()

			sem := sems[original.Category]
			if sem == nil {
				sem = sems["Archived"]
			}

			sem <- struct{}{}
			defer func() { <-sem }()

			// Set current file for progress display
			metrics.SetCurrentFile(original.Path)

			c.processSingle(original, registry, cfg, metrics)

			// Clear current file when done
			metrics.SetCurrentFile("")
		}(m)
	}

	wg.Wait()
	close(done)

	// Give progress printer a moment to finish final update
	time.Sleep(50 * time.Millisecond)

	// Clear progress display before returning
	metrics.ClearProgress()
}

func (c *ShrinkCmd) processSingle(m ShrinkMedia, registry *ProcessorRegistry,
	cfg *ProcessorConfig, metrics *ShrinkMetrics,
) ProcessResult {
	// Handle broken archives - move to --move-broken without processing
	if m.IsBroken {
		slog.Info("Broken archive detected, moving to broken directory", "path", m.Path)
		if cfg.MoveBroken != "" {
			c.moveToBroken(m.Path, m.PartFiles)
		}
		c.markDeleted(m.Path)
		metrics.RecordFailure(m.Category)
		return ProcessResult{SourcePath: m.Path, Success: false}
	}

	// Capture original timestamps before processing
	var originalAtime, originalMtime time.Time
	if stat, err := os.Stat(m.Path); err != nil {
		// File doesn't exist - mark as skipped (deleted)
		slog.Warn("File not found, marking as skipped", "path", m.Path)
		metrics.RecordSkipped(m.Category)
		c.markDeleted(m.Path)
		return ProcessResult{SourcePath: m.Path, Error: err}
	} else {
		originalAtime = stat.ModTime()
		originalMtime = stat.ModTime()
	}

	slog.Info("Processing",
		"path", m.Path,
		"category", m.Category,
		"size", utils.FormatSize(m.Size))

	processor := registry.GetProcessor(&m)
	if processor == nil {
		metrics.RecordFailure(m.Category)
		return ProcessResult{SourcePath: m.Path, Error: fmt.Errorf("no processor found")}
	}

	ctx, cancel := context.WithTimeout(context.Background(), c.getTimeout(m))
	defer cancel()

	result := processor.Process(ctx, &m, cfg)

	if result.Error != nil {
		slog.Error("Processing failed", "path", m.Path, "error", result.Error)
		metrics.RecordFailure(m.Category)
		if cfg.MoveBroken != "" {
			c.moveToBroken(m.Path, result.PartFiles)
		}
		return result
	}

	if !result.Success {
		// Processing succeeded but produced no valid output (e.g. invalid file)
		if cfg.DeleteUnplayable {
			c.markDeleted(m.Path)
			os.Remove(m.Path)
		}
		metrics.RecordFailure(m.Category)
		return result
	}

	// 1. Calculate new size and compare with original
	var totalNewSize int64
	for _, out := range result.Outputs {
		totalNewSize += out.Size
	}

	keepNewFiles := true
	if cfg.DeleteLarger && !cfg.ForceShrink && totalNewSize > m.Size {
		keepNewFiles = false
	}

	if keepNewFiles {
		// Keep new files, delete original
		if m.Path != "" {
			foundInOutputs := false
			for _, out := range result.Outputs {
				if out.Path == m.Path {
					foundInOutputs = true
					break
				}
			}
			if !foundInOutputs {
				c.markDeleted(m.Path)
				os.Remove(m.Path)
			}
		}

		// Update database and apply timestamps
		for i, out := range result.Outputs {
			// We use updateDatabase when the original is replaced by a single output
			// to preserve metadata like play_count, etc.
			// Except for archives, where we want to keep the archive record as deleted.
			if len(result.Outputs) == 1 && out.Path != m.Path && m.Category != "Archived" {
				c.updateDatabase(m.Path, out.Path, out.Size, m.Duration)
			} else if out.Path != m.Path {
				c.addDatabaseEntry(out.Path, out.Size, m.Duration)
			} else {
				c.markShrinked(out.Path)
			}

			if i == 0 && !originalAtime.IsZero() {
				applyTimestamps(out.Path, originalAtime, originalMtime)
				// Update duration if needed
				if m.Category == "Audio" || m.Category == "Video" {
					if newDuration := c.getActualDuration(out.Path); newDuration > 0 {
						m.Duration = newDuration
					}
				}
			}
			c.moveTo(out.Path)
		}
		metrics.RecordSuccess(m.Category, m.Size, totalNewSize, m.ProcessingTime, int64(m.Duration))
	} else {
		// Delete new files, keep original
		for _, out := range result.Outputs {
			if out.Path != m.Path {
				os.RemoveAll(out.Path) // RemoveAll because it might be a directory (TextProcessor/ArchiveProcessor)
			}
		}
		c.markShrinked(m.Path)
		metrics.RecordSuccess(m.Category, m.Size, m.Size, m.ProcessingTime, int64(m.Duration))
	}

	return result
}

// getActualDuration probes a file and returns its actual duration
func (c *ShrinkCmd) getActualDuration(path string) float64 {
	cmd := exec.Command("ffprobe",
		"-v", "quiet",
		"-print_format", "json",
		"-show_format",
		path)
	output, err := cmd.Output()
	if err != nil {
		return 0
	}

	var result struct {
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		return 0
	}

	duration, _ := strconv.ParseFloat(result.Format.Duration, 64)
	return duration
}

func (c *ShrinkCmd) getTimeout(m ShrinkMedia) time.Duration {
	switch m.Category {
	case "Video":
		duration := utils.GetDurationForTimeout(m.Duration, m.Size, m.Ext)
		if duration > 30 {
			return time.Duration(duration*c.VideoTimeoutMult) * time.Second
		}
		return utils.ParseDurationString(c.VideoTimeout)
	case "Audio":
		duration := m.Duration
		if duration > 30 {
			return time.Duration(duration*c.AudioTimeoutMult) * time.Second
		}
		return utils.ParseDurationString(c.AudioTimeout)
	case "Image":
		return utils.ParseDurationString(c.ImageTimeout)
	case "Text":
		return utils.ParseDurationString(c.TextTimeout)
	default:
		return 30 * time.Minute
	}
}

func (c *ShrinkCmd) markDeleted(path string) {
	for _, dbPath := range c.Databases {
		sqlDB, _, err := db.ConnectWithInit(dbPath)
		if err == nil {
			_, err := sqlDB.Exec("UPDATE media SET time_deleted = ? WHERE path = ?", time.Now().Unix(), path)
			if err != nil {
				slog.Warn("Failed to mark file deleted in database", "path", path, "error", err)
			}
			sqlDB.Close()
		} else {
			slog.Warn("Failed to connect to database for marking deleted", "path", path, "error", err)
		}
	}
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

func (c *ShrinkCmd) updateDatabase(oldPath, newPath string, newSize int64, duration float64) {
	for _, dbPath := range c.Databases {
		sqlDB, _, err := db.ConnectWithInit(dbPath)
		if err == nil {
			_, _ = sqlDB.Exec("DELETE FROM media WHERE path = ?", newPath)
			var execErr error
			if duration > 0 {
				_, execErr = sqlDB.Exec(
					"UPDATE media SET path = ?, size = ?, duration = ?, time_deleted = 0, is_shrinked = 1 WHERE path = ?",
					newPath, newSize, duration, oldPath)
			} else {
				_, execErr = sqlDB.Exec(
					"UPDATE media SET path = ?, size = ?, time_deleted = 0, is_shrinked = 1 WHERE path = ?",
					newPath, newSize, oldPath)
			}
			if execErr != nil {
				slog.Warn("Failed to update database entry", "oldPath", oldPath, "newPath", newPath, "error", execErr)
			}
			sqlDB.Close()
		} else {
			slog.Warn("Failed to connect to database for update", "oldPath", oldPath, "error", err)
		}
	}
}

// addDatabaseEntry adds a new file entry to the database (for split files or archive contents)
func (c *ShrinkCmd) addDatabaseEntry(path string, size int64, duration float64) {
	for _, dbPath := range c.Databases {
		sqlDB, _, err := db.ConnectWithInit(dbPath)
		if err == nil {
			_, err := sqlDB.Exec("DELETE FROM media WHERE path = ?", path)
			if err != nil {
				slog.Warn("Failed to delete existing media entry", "path", path, "error", err)
			}
			var execErr error
			if duration > 0 {
				_, execErr = sqlDB.Exec(
					"INSERT INTO media (path, size, duration, time_deleted, is_shrinked) VALUES (?, ?, ?, 0, 0)",
					path, size, duration)
			} else {
				_, execErr = sqlDB.Exec(
					"INSERT INTO media (path, size, time_deleted, is_shrinked) VALUES (?, ?, 0, 0)",
					path, size)
			}
			if execErr != nil {
				slog.Warn("Failed to add database entry", "path", path, "error", execErr)
			}
			sqlDB.Close()
		} else {
			slog.Warn("Failed to connect to database for adding entry", "path", path, "error", err)
		}
	}
}

func (c *ShrinkCmd) markShrinked(path string) {
	for _, dbPath := range c.Databases {
		sqlDB, _, err := db.ConnectWithInit(dbPath)
		if err == nil {
			_, err := sqlDB.Exec("UPDATE media SET is_shrinked = 1 WHERE path = ?", path)
			if err != nil {
				slog.Warn("Failed to mark file as shrinked in database", "path", path, "error", err)
			}
			sqlDB.Close()
		} else {
			slog.Warn("Failed to connect to database for marking shrinked", "path", path, "error", err)
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

// InstalledTools tracks which external tools are available
type InstalledTools struct {
	FFmpeg      bool
	ImageMagick bool
	Calibre     bool
	Unar        bool
}

func (c *ShrinkCmd) checkInstalledTools() InstalledTools {
	tools := InstalledTools{
		FFmpeg:      utils.CommandExists("ffmpeg"),
		ImageMagick: utils.CommandExists("magick"),
		Calibre:     utils.CommandExists("ebook-convert"),
		Unar:        utils.CommandExists("lsar"),
	}

	if !tools.FFmpeg {
		slog.Warn("ffmpeg not installed. Video and Audio files will be skipped")
	}
	if !tools.ImageMagick {
		slog.Warn("ImageMagick not installed. Image files will be skipped")
	}
	if !tools.Calibre {
		slog.Warn("Calibre not installed. Text files will be skipped")
	}
	if !tools.Unar {
		slog.Warn("unar not installed. Archives will not be extracted")
	}

	return tools
}

// applyTimestamps applies timestamps to a file or folder (recursively for folders)
func applyTimestamps(path string, atime, mtime time.Time) {
	// Apply to the path itself
	os.Chtimes(path, atime, mtime)

	// If it's a directory, walk and apply to all contents
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return
	}

	filepath.Walk(path, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		os.Chtimes(p, atime, mtime)
		return nil
	})
}
