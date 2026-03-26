package commands

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"

	"github.com/alecthomas/kong"
	"github.com/chapmanjacobd/shrink/internal/db"
	"github.com/chapmanjacobd/shrink/internal/ffmpeg"
	"github.com/chapmanjacobd/shrink/internal/models"
	"github.com/chapmanjacobd/shrink/internal/utils"
)

// UnknownExt tracks the count and total size of files with an unknown extension
type UnknownExt struct {
	Count int
	Size  int64
}

// ShrinkCmd is the main command for shrinking media files
type ShrinkCmd struct {
	unknownExtensions map[string]UnknownExt
	skippedByTool     map[string]int64 // Tracks known extensions skipped due to missing tools (e.g., "ffmpeg: mkv")
	sqlDBs            []*sql.DB
	Databases         []string `arg:"" required:"" help:"SQLite database files or directories to scan"`
	Config            `embed:""`
}

func (c *ShrinkCmd) Run(ctx *kong.Context) error {
	c.ApplyProfile()
	models.SetupLogging(c.Verbose)
	defer c.closeDatabases()

	c.unknownExtensions = make(map[string]UnknownExt)
	c.skippedByTool = make(map[string]int64)

	// Build processor configuration
	cfg := c.BuildProcessorConfig()

	// Check installed tools
	tools := c.checkInstalledTools()

	// Initialize databases
	if err := c.initDatabases(); err != nil {
		return err
	}

	// Initialize components
	ffmpegProc := ffmpeg.NewFFmpegProcessor(cfg)
	registry := NewProcessorRegistry(ffmpegProc, cfg, c.VideoOnly, c.AudioOnly, c.ImageOnly, c.TextOnly)
	defer registry.Cleanup()
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
	filteredMedia := c.filterByTools(allMedia, registry, tools)
	allMedia = nil // Free memory
	slog.Info("Filtered media by tools",
		"count", len(filteredMedia),
		"ffmpeg", tools.FFmpeg,
		"magick", tools.ImageMagick,
		"calibre", tools.Calibre)

	// Print unknown extensions and skipped files table once
	c.PrintUnknownExtensions()

	if len(filteredMedia) == 0 {
		slog.Info("No processable media found")
		return nil
	}

	// Initialize Engine
	engCfg := EngineConfig{
		VideoThreads:    c.VideoThreads,
		AudioThreads:    c.AudioThreads,
		ImageThreads:    c.ImageThreads,
		TextThreads:     c.TextThreads,
		AnalysisThreads: c.AnalysisThreads,
		Timeout:         c.TimeoutFlags,
	}
	engine := NewEngine(c, cfg, engCfg, c.sqlDBs, registry, metrics)

	// Analyze and decide what to shrink
	slog.Info("Analyzing files", "count", len(filteredMedia))
	toShrink := engine.analyzeMedia(filteredMedia)
	filteredMedia = nil // Free memory
	slog.Info("Analysis complete", "selected", len(toShrink))

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
	seenPaths = nil // Free memory
	slog.Info("Deduplication complete", "unique_count", len(toShrink))

	// Apply continue-from filter
	if c.ContinueFrom != "" {
		slog.Debug("Applying continue-from filter", "from", c.ContinueFrom)
		toShrink = c.ApplyContinueFrom(toShrink)
		slog.Info("Continue-from filter applied", "remaining", len(toShrink))
	}

	// Sort by efficiency (most space freed per second)
	slog.Debug("Sorting files by efficiency")
	c.SortByEfficiency(toShrink)
	slog.Info("Sort complete")

	// Print summary
	c.PrintSummary(toShrink)

	if c.Simulate {
		fmt.Println("Simulation mode - no files will be processed")
		return nil
	}

	// Confirm
	if !c.NoConfirm {
		// Clear any progress display before showing the confirm prompt
		engine.metrics.ClearProgress()
		if !c.Confirm() {
			return nil
		}
	}

	// Setup context for graceful shutdown
	runCtx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// Process with parallelism
	engine.processMedia(runCtx, toShrink)
	toShrink = nil // Free memory

	// Final summary
	metrics.LogSummary()

	// Clear maps to free memory
	c.unknownExtensions = nil
	c.skippedByTool = nil

	return nil
}

// ============================================================================
// Database Operations
// ============================================================================

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

	// Bulk mark files with already-optimized extensions as shrinked
	// This prevents loading them for processing
	if len(c.sqlDBs) > 0 {
		db.BulkMarkOptimizedExtensions(c.sqlDBs)
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
				Path:       r.Path,
				Size:       r.Size,
				Duration:   r.Duration,
				VideoCount: r.VideoCount,
				AudioCount: r.AudioCount,
				MediaType:  r.MediaType,
				Ext:        strings.ToLower(filepath.Ext(r.Path)),
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

// ============================================================================
// Media Filtering and Analysis
// ============================================================================

// filterByTools filters media based on available tools and requested categories
func (c *ShrinkCmd) filterByTools(media []models.ShrinkMedia, registry *MediaRegistry, tools InstalledTools) []models.ShrinkMedia {
	filtered := make([]models.ShrinkMedia, 0, len(media))

	for _, m := range media {
		// Ignore MediaType from DB, determine only by filterByTool
		m.MediaType = ""

		// Skip if already optimized
		if utils.IsOptimized(m.Ext) {
			// Mark as skipped in all DBs if it came from a DB (file still exists)
			db.MarkSkipped(c.sqlDBs, m.Path)
			continue
		}

		p := registry.GetProcessor(&m)
		if p == nil {
			// If it's a known media/archive extension, it was filtered out by flags
			if utils.MediaExtensionMap[m.Ext] || utils.ArchiveExtensionMap[m.Ext] || isMultiPartArchiveExt(m.Ext) {
				continue
			}

			// Truly unknown extension
			ext := m.Ext
			if ext == "" {
				ext = "no extension"
			}
			entry := c.unknownExtensions[ext]
			entry.Count++
			entry.Size += m.Size
			c.unknownExtensions[ext] = entry
			continue
		}

		// Set category and media type from processor
		m.Category = p.Category()
		m.MediaType = strings.ToLower(m.Category)

		// Check if tool is available
		tool := p.RequiredTool()
		if !tools.IsAvailable(tool) {
			key := fmt.Sprintf("%s: %s", tool, m.Ext)
			c.skippedByTool[key] += m.Size
			continue
		}

		filtered = append(filtered, m)
	}

	return filtered
}

func (c *ShrinkCmd) SortByEfficiency(media []models.ShrinkMedia) {
	slices.SortFunc(media, func(a, b models.ShrinkMedia) int {
		timeA := max(a.ProcessingTime, 1)
		timeB := max(b.ProcessingTime, 1)
		ratioA := float64(a.Savings) / float64(timeA)
		ratioB := float64(b.Savings) / float64(timeB)
		if ratioA > ratioB {
			return -1
		} else if ratioA < ratioB {
			return 1
		}
		return 0
	})
}

func (c *ShrinkCmd) PrintSummary(media []models.ShrinkMedia) {
	var totalSize, totalFuture, totalSavings int64
	type breakdown struct {
		count   int
		size    int64
		future  int64
		savings int64
	}
	typeBreakdown := make(map[string]breakdown)

	for _, m := range media {
		totalSize += m.Size
		totalFuture += m.FutureSize
		totalSavings += m.Savings

		key := m.DisplayCategory()
		b := typeBreakdown[key]
		b.count++
		b.size += m.Size
		b.future += m.FutureSize
		b.savings += m.Savings
		typeBreakdown[key] = b
	}

	// Print summary table
	fmt.Println()
	headers := []string{"Media Type", "Count", "Current", "Future", "Savings", "Future Compression"}
	var rows [][]string

	// Sort keys for consistent output
	keys := make([]string, 0, len(typeBreakdown))
	for k := range typeBreakdown {
		keys = append(keys, k)
	}
	slices.Sort(keys)

	for _, key := range keys {
		b := typeBreakdown[key]
		var speed string
		if b.future > 0 {
			ratio := float64(b.size) / float64(b.future)
			speed = fmt.Sprintf("%.1fx", ratio)
		} else {
			// No future size available (e.g., analysis failed, empty archive)
			speed = "-"
		}
		rows = append(rows, []string{
			key,
			strconv.Itoa(b.count),
			utils.FormatSize(b.size),
			utils.FormatSize(b.future),
			utils.FormatSize(b.savings),
			speed,
		})
	}

	// Add TOTAL row
	rows = append(rows, []string{
		"TOTAL",
		"",
		utils.FormatSize(totalSize),
		utils.FormatSize(totalFuture),
		utils.FormatSize(totalSavings),
		"",
	})

	utils.PrintTable(headers, rows)
	fmt.Println()
}

func (c *ShrinkCmd) PrintUnknownExtensions() {
	// Combine unknown extensions and skipped by tool
	hasUnknown := len(c.unknownExtensions) > 0
	hasSkipped := len(c.skippedByTool) > 0

	if !hasUnknown && !hasSkipped {
		return
	}

	fmt.Println("Unknown File Extensions Scanned")
	headers := []string{"Extension", "Count", "Total Size"}
	var rows [][]string

	// Sort by size descending
	type extEntry struct {
		ext   string
		count int
		size  int64
	}
	var sorted []extEntry

	for ext, entry := range c.unknownExtensions {
		sorted = append(sorted, extEntry{ext, entry.Count, entry.Size})
	}
	// Note: skippedByTool only has size, no count readily available per file here,
	// but for consistency we'll just show what we have.
	for key, size := range c.skippedByTool {
		sorted = append(sorted, extEntry{key, 0, size})
	}

	slices.SortFunc(sorted, func(a, b extEntry) int {
		if a.size > b.size {
			return -1
		} else if a.size < b.size {
			return 1
		}
		return 0
	})

	for _, es := range sorted {
		countStr := strconv.Itoa(es.count)
		if es.count == 0 {
			countStr = "-"
		}
		rows = append(rows, []string{es.ext, countStr, utils.FormatSize(es.size)})
	}
	utils.PrintTable(headers, rows)
	fmt.Println()
}

func (c *ShrinkCmd) Confirm() bool {
	fmt.Print("\nProceed with shrinking? [y/N] ")
	var response string
	fmt.Scanln(&response)
	return strings.ToLower(response) == "y"
}

func (c *ShrinkCmd) ApplyContinueFrom(media []models.ShrinkMedia) []models.ShrinkMedia {
	found := false
	var filtered []models.ShrinkMedia

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

// ============================================================================
// File Operations
// ============================================================================

func (c *ShrinkCmd) getDestPath(path, target string) (string, bool) {
	if !strings.HasPrefix(target, ":/") {
		return "", false
	}
	mountPoint, err := utils.GetMountPoint(path)
	if err != nil {
		return "", false
	}
	relPath, err := filepath.Rel(mountPoint, path)
	if err != nil {
		return "", false
	}
	// Result: mountPoint + targetDir + relPath
	return filepath.Join(mountPoint, target[2:], relPath), true
}

func (c *ShrinkCmd) MoveToBroken(path string, partFiles []string) {
	if c.MoveBroken == "" || path == "" {
		return
	}

	dest, ok := c.getDestPath(path, c.MoveBroken)
	if !ok {
		// Get the parent folder name for tidy organization (original behavior)
		parentFolder := filepath.Base(filepath.Dir(path))
		dest = filepath.Join(c.MoveBroken, parentFolder, filepath.Base(path))
	}

	// Move the main file
	if _, err := os.Stat(path); err == nil {
		if err := utils.MoveFile(path, dest); err != nil {
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
			partDest, ok := c.getDestPath(partFile, c.MoveBroken)
			if !ok {
				parentFolder := filepath.Base(filepath.Dir(path))
				partDest = filepath.Join(c.MoveBroken, parentFolder, filepath.Base(partFile))
			}
			if err := utils.MoveFile(partFile, partDest); err != nil {
				slog.Warn("Failed to move broken archive part", "from", partFile, "to", partDest, "error", err)
			} else {
				slog.Info("Moved broken archive part", "from", partFile, "to", partDest)
			}
		}
	}
}

func (c *ShrinkCmd) MoveTo(path string) {
	if c.Move != "" && path != "" {
		dest, ok := c.getDestPath(path, c.Move)
		if !ok {
			dest = filepath.Join(c.Move, filepath.Base(path))
		}
		if err := utils.MoveFile(path, dest); err != nil {
			slog.Warn("Failed to move file", "from", path, "to", dest, "error", err)
		}
	}
}
