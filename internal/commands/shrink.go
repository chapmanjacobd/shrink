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
	models.CoreFlags        `embed:""`
	models.PathFilterFlags  `embed:""`
	models.FilterFlags      `embed:""`
	models.MediaFilterFlags `embed:""`
	models.TimeFilterFlags  `embed:""`
	models.DeletedFlags     `embed:""`

	Databases []string `arg:"" required:"" help:"SQLite database files" type:"existingfile"`

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

	ForceReshrink bool `help:"Force reprocessing of already shrinked files"`
}

// ShrinkMedia represents a media file to be processed
type ShrinkMedia struct {
	Path           string
	Size           int64
	Duration       float64
	VideoCount     int
	AudioCount     int
	VideoCodecs    string
	AudioCodecs    string
	SubtitleCodecs string
	Type           string
	Ext            string
	MediaType      string
	FutureSize     int64
	Savings        int64
	ProcessingTime int
	CompressedSize int64
	ArchivePath    string
	NewPath        string
	NewSize        int64
	TimeDeleted    int64
	Invalid        bool
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
		slog.Info("No files to shrink")
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
		slog.Info("Simulation mode - no files will be processed")
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
	}
}

func (c *ShrinkCmd) loadAllMedia() ([]ShrinkMedia, error) {
	var allMedia []ShrinkMedia

	for _, dbPath := range c.Databases {
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
	}

	return allMedia, nil
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
            COALESCE(type, '')
		FROM media
		WHERE COALESCE(time_deleted, 0) = 0
            AND size > 0
	`

	if !c.ForceReshrink {
		query += " AND COALESCE(is_shrinked, 0) = 0"
	}

	// Filter by media type (prefilter in database)
	var typeConditions []string
	if c.VideoOnly {
		typeConditions = append(typeConditions, "type = 'video'")
	}
	if c.AudioOnly {
		typeConditions = append(typeConditions, "type = 'audio'", "type = 'audiobook'")
	}
	if c.ImageOnly {
		typeConditions = append(typeConditions, "type = 'image'")
	}
	if c.TextOnly {
		typeConditions = append(typeConditions, "type = 'text'")
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
			&m.VideoCodecs, &m.AudioCodecs, &m.SubtitleCodecs, &m.Type)
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
		filetype := strings.ToLower(m.Type)

		// Audio/Video
		if ((strings.HasPrefix(filetype, "audio/") || strings.Contains(filetype, " audio")) ||
			(utils.AudioExtensionMap[m.Ext] && m.VideoCount == 0)) ||
			((strings.HasPrefix(filetype, "video/") || strings.Contains(filetype, " video")) ||
				(utils.VideoExtensionMap[m.Ext] && m.VideoCount >= 1)) {
			canProcess = tools.FFmpeg
		}
		// Image
		if (strings.HasPrefix(filetype, "image/") || strings.Contains(filetype, " image")) ||
			(utils.ImageExtensionMap[m.Ext] && m.Duration == 0) {
			canProcess = tools.ImageMagick
		}
		// Text
		if utils.TextExtensionMap[m.Ext] {
			canProcess = tools.Calibre
		}
		// Archives
		if strings.HasPrefix(filetype, "archive/") || utils.ArchiveExtensionMap[m.Ext] {
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

		// Get processor's media type
		m.MediaType = processor.MediaType()
		metrics.RecordStarted(m.MediaType, m.Path)

		// Estimate size and time
		futureSize, processingTime := processor.EstimateSize(m, cfg)

		// For archives, check if they contain processable content
		if m.MediaType == "Archived" {
			if archiveProc, ok := processor.(*ArchiveProcessor); ok {
				var hasProcessable bool
				futureSize, processingTime, hasProcessable = archiveProc.EstimateSizeForArchive(m, cfg)
				if !hasProcessable {
					metrics.RecordSkipped(m.MediaType)
					continue
				}
				// Use compressed size for savings calculation
				if m.CompressedSize > 0 {
					m.Size = m.CompressedSize
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
			metrics.RecordSkipped(m.MediaType)
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
	for _, m := range media {
		totalSize += m.Size
		totalFuture += m.FutureSize
		totalSavings += m.Savings
	}

	slog.Info("Summary",
		"count", len(media),
		"current", utils.FormatSize(totalSize),
		"future", utils.FormatSize(totalFuture),
		"savings", utils.FormatSize(totalSavings))
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

			sem := sems[original.MediaType]
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
}

func (c *ShrinkCmd) processSingle(m ShrinkMedia, registry *ProcessorRegistry,
	cfg *ProcessorConfig, metrics *ShrinkMetrics,
) ProcessResult {
	// Capture original timestamps before processing
	var originalAtime, originalMtime time.Time
	if stat, err := os.Stat(m.Path); err != nil {
		// File doesn't exist - mark as skipped (deleted)
		slog.Warn("File not found, marking as skipped", "path", m.Path)
		metrics.RecordSkipped(m.MediaType)
		c.markDeleted(m.Path)
		return ProcessResult{SourcePath: m.Path, Error: err}
	} else {
		originalAtime = stat.ModTime()
		originalMtime = stat.ModTime()
	}

	slog.Info("Processing",
		"path", m.Path,
		"type", m.MediaType,
		"size", utils.FormatSize(m.Size))

	processor := registry.GetProcessor(&m)
	if processor == nil {
		metrics.RecordFailure(m.MediaType)
		return ProcessResult{SourcePath: m.Path, Error: fmt.Errorf("no processor found")}
	}

	ctx, cancel := context.WithTimeout(context.Background(), c.getTimeout(m))
	defer cancel()

	result := processor.Process(ctx, &m, cfg)

	if result.Error != nil {
		slog.Error("Processing failed", "path", m.Path, "error", result.Error)
		metrics.RecordFailure(m.MediaType)
		if cfg.MoveBroken != "" {
			c.moveToBroken(m.Path)
		}
	}

	// 1. Handle Deletions
	for _, path := range result.ToDelete {
		if path == m.Path {
			c.markDeleted(path)
		}
		if _, err := os.Stat(path); err == nil {
			os.Remove(path)
		}
	}

	// 2. Handle New Files & Database Updates
	var totalNewSize int64
	for i, out := range result.Outputs {
		totalNewSize += out.Size

		// Determine if we should update existing record or add new one
		// We use updateDatabase when the original is replaced by a single output
		// to preserve metadata like play_count, etc.
		if len(result.Outputs) == 1 && out.Path != m.Path && result.Success {
			c.updateDatabase(m.Path, out.Path, out.Size, m.Duration)
		} else if out.Path != m.Path {
			c.addDatabaseEntry(out.Path, out.Size, m.Duration)
		} else {
			c.markShrinked(out.Path)
		}

		// Apply timestamps to the first output
		if i == 0 && !originalAtime.IsZero() {
			applyTimestamps(out.Path, originalAtime, originalMtime)

			// For audio/video, update duration from transcoded file
			if m.MediaType == "Audio" || m.MediaType == "Video" {
				if newDuration := c.getActualDuration(out.Path); newDuration > 0 {
					m.Duration = newDuration
					slog.Debug("Updated duration from transcoded file",
						"path", out.Path, "duration", newDuration)
				}
			}
		}
	}

	// 3. Handle Moves
	for _, path := range result.ToMove {
		c.moveTo(path)
	}

	// Special case: if original file was updated in place and no move was requested
	if len(result.ToMove) == 0 && result.Success {
		found := false
		for _, out := range result.Outputs {
			if out.Path == m.Path {
				found = true
				break
			}
		}
		if found {
			c.moveTo(m.Path)
		}
	}

	if result.Success {
		metrics.RecordSuccess(m.MediaType, m.Size, totalNewSize, m.ProcessingTime, int64(m.Duration))
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
	switch m.MediaType {
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
			_, _ = sqlDB.Exec("UPDATE media SET time_deleted = ? WHERE path = ?", time.Now().Unix(), path)
			sqlDB.Close()
		}
	}
}

func (c *ShrinkCmd) moveToBroken(path string) {
	if c.MoveBroken != "" && path != "" {
		if _, err := os.Stat(path); err == nil {
			dest := filepath.Join(c.MoveBroken, filepath.Base(path))
			os.MkdirAll(c.MoveBroken, 0o755)
			os.Rename(path, dest)
		}
	}
}

func (c *ShrinkCmd) updateDatabase(oldPath, newPath string, newSize int64, duration float64) {
	for _, dbPath := range c.Databases {
		sqlDB, _, err := db.ConnectWithInit(dbPath)
		if err == nil {
			_, _ = sqlDB.Exec("DELETE FROM media WHERE path = ?", newPath)
			if duration > 0 {
				_, _ = sqlDB.Exec(
					"UPDATE media SET path = ?, size = ?, duration = ? WHERE path = ?",
					newPath, newSize, duration, oldPath)
			} else {
				_, _ = sqlDB.Exec(
					"UPDATE media SET path = ?, size = ? WHERE path = ?",
					newPath, newSize, oldPath)
			}
			sqlDB.Close()
		}
	}
}

// addDatabaseEntry adds a new file entry to the database (for split files or archive contents)
func (c *ShrinkCmd) addDatabaseEntry(path string, size int64, duration float64) {
	for _, dbPath := range c.Databases {
		sqlDB, _, err := db.ConnectWithInit(dbPath)
		if err == nil {
			_, _ = sqlDB.Exec("DELETE FROM media WHERE path = ?", path)
			if duration > 0 {
				_, _ = sqlDB.Exec(
					"INSERT INTO media (path, size, duration, time_deleted, is_shrinked) VALUES (?, ?, ?, 0, 0)",
					path, size, duration)
			} else {
				_, _ = sqlDB.Exec(
					"INSERT INTO media (path, size, time_deleted, is_shrinked) VALUES (?, ?, 0, 0)",
					path, size)
			}
			sqlDB.Close()
		}
	}
}

func (c *ShrinkCmd) markShrinked(path string) {
	for _, dbPath := range c.Databases {
		sqlDB, _, err := db.ConnectWithInit(dbPath)
		if err == nil {
			_, _ = sqlDB.Exec("UPDATE media SET is_shrinked = 1 WHERE path = ?", path)
			sqlDB.Close()
		}
	}
}

func (c *ShrinkCmd) moveTo(path string) {
	if c.Move != "" && path != "" {
		dest := filepath.Join(c.Move, filepath.Base(path))
		os.Rename(path, dest)
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
