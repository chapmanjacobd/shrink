package commands

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/chapmanjacobd/shrink/internal/db"
	"github.com/chapmanjacobd/shrink/internal/models"
	"github.com/chapmanjacobd/shrink/internal/utils"
)

// Processing timeout constants
const (
	defaultTimeout       = 30 * time.Minute // Default timeout for unknown media types
	minArchiveTimeoutSec = 600              // Minimum 10 minutes for archive processing
	archiveTimeoutPerGB  = 100              // 100 seconds per GB for archive processing
)

// ============================================================================
// Engine
// ============================================================================

// UI interface for user interaction and file operations.
// It abstracts CLI-specific actions from the core engine.
type UI interface {
	Confirm() bool
	MoveTo(path string)
	MoveToBroken(path string, partFiles []string)
	MoveToSkipped(path string, partFiles []string)
}

// EngineConfig contains concurrency and timeout settings for the engine.
type EngineConfig struct {
	VideoThreads    int
	Video4KThreads  int
	AudioThreads    int
	ImageThreads    int
	TextThreads     int
	AnalysisThreads int
	Timeout         TimeoutFlags
	Move            string
	ActiveTime      []*utils.TimeRange
	Simulate        bool
}

// Engine coordinates the media analysis and processing lifecycle.
type Engine struct {
	ui       UI
	sqlDBs   []*sql.DB
	cfg      *models.ProcessorConfig
	registry *MediaRegistry
	metrics  *ShrinkMetrics
	engCfg   EngineConfig
}

// NewEngine creates a new Engine instance.
func NewEngine(ui UI, cfg *models.ProcessorConfig, engCfg EngineConfig, sqlDBs []*sql.DB, registry *MediaRegistry, metrics *ShrinkMetrics) *Engine {
	return &Engine{
		cfg:      cfg,
		sqlDBs:   sqlDBs,
		registry: registry,
		metrics:  metrics,
		ui:       ui,
		engCfg:   engCfg,
	}
}

// ============================================================================
// Media Analysis
// ============================================================================

// analysisResult is the outcome of analyzing a single media item.
type analysisResult struct {
	index    int
	media    models.ShrinkMedia
	skip     bool
	broken   bool
	failed   bool
	skipMove bool
}

// analyzeMedia evaluates each media item to determine if it should be processed.
// It processes files grouped by media type: Images, Text, Video, Audio, Archives.
func (e *Engine) analyzeMedia(media []models.ShrinkMedia) ([]models.ShrinkMedia, []models.ShrinkMedia) {
	slog.Info("Starting media analysis", "total_files", len(media))

	// Group media by type for sequential processing
	// Order: Images, Text, Video, Video4K, Audio, Archives
	categories := []string{"Image", "Text", "Video", "Video4K", "Audio", "Archived"}
	mediaByCategory := make(map[string][]int)
	for i, m := range media {
		// Determine category from extension before processing
		ext := m.Ext
		if utils.ImageExtensionMap[ext] {
			mediaByCategory["Image"] = append(mediaByCategory["Image"], i)
		} else if utils.TextExtensionMap[ext] {
			mediaByCategory["Text"] = append(mediaByCategory["Text"], i)
		} else if utils.VideoExtensionMap[ext] {
			if m.Height >= 2160 || m.Width >= 2160 {
				mediaByCategory["Video4K"] = append(mediaByCategory["Video4K"], i)
			} else {
				mediaByCategory["Video"] = append(mediaByCategory["Video"], i)
			}
		} else if utils.AudioExtensionMap[ext] {
			mediaByCategory["Audio"] = append(mediaByCategory["Audio"], i)
		} else if utils.ArchiveExtensionMap[ext] || isMultiPartArchiveExt(ext) {
			mediaByCategory["Archived"] = append(mediaByCategory["Archived"], i)
		} else {
			// Unknown - put in a general bucket
			mediaByCategory["Archived"] = append(mediaByCategory["Archived"], i)
		}
	}

	// Results collection
	allResults := make(map[int]analysisResult)

	// Process each category sequentially
	for _, cat := range categories {
		indices := mediaByCategory[cat]
		if len(indices) == 0 {
			continue
		}

		slog.Info("Analyzing category", "category", cat, "count", len(indices))
		categoryResults := e.analyzeCategory(media, indices, cat)
		maps.Copy(allResults, categoryResults)
	}

	// Collect skipped files that should be moved. The caller performs the
	// actual moves after confirmation (and never during a simulation).
	var skippedToMove []models.ShrinkMedia
	for _, result := range allResults {
		if result.skipMove && result.skip {
			skippedToMove = append(skippedToMove, result.media)
		}
	}

	// Build final slice in original order
	// Failed analyses are skipped (not added to toShrink)
	var toShrink []models.ShrinkMedia
	for i := range media {
		result, exists := allResults[i]
		if !exists {
			continue
		}
		if result.failed {
			// Analysis failed (e.g., lsar timeout) - skip this file
			continue
		}
		if result.broken {
			toShrink = append(toShrink, result.media)
		} else if !result.skip {
			toShrink = append(toShrink, result.media)
		}
	}

	slog.Info("Media analysis complete", "to_shrink", len(toShrink), "skipped_to_move", len(skippedToMove))
	return toShrink, skippedToMove
}

// analyzeCategory analyzes a group of files of the same media type.
func (e *Engine) analyzeCategory(media []models.ShrinkMedia, indices []int, category string) map[int]analysisResult {
	// Use a fixed worker pool. Dynamic resizing made it possible for all
	// workers to exit while jobs were still queued, and resizing from another
	// goroutine concurrently with WaitGroup.Wait is not supported.
	targetConcurrency := e.engCfg.AnalysisThreads
	if targetConcurrency <= 0 {
		targetConcurrency = runtime.NumCPU() * 4
	}
	slog.Info("Analysis parallelism configured", "workers", targetConcurrency)

	jobs := make(chan int, min(len(indices), 1000))
	results := make(chan analysisResult, min(len(indices), 1000))

	var wg sync.WaitGroup
	var completedJobs atomic.Int64
	var activeWorkers atomic.Int32

	worker := func() {
		defer wg.Done()
		for idx := range jobs {
			activeWorkers.Add(1)
			results <- e.analyzeOne(media, idx)
			completedJobs.Add(1)
			activeWorkers.Add(-1)
		}
	}

	for range targetConcurrency {
		wg.Add(1)
		go worker()
	}

	progressDone := make(chan struct{})
	go func() {
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				completed := completedJobs.Load()
				workers := activeWorkers.Load()
				if completed > 0 || workers > 0 {
					status := fmt.Sprintf("\rAnalyzing %d/%d %s files", completed, len(indices), strings.ToLower(category))
					if workers > 0 {
						status += fmt.Sprintf(" (%d workers)", workers)
					}
					fmt.Printf("%s\033[K", status)
				}
			case <-progressDone:
				fmt.Printf("\rAnalyzed %d/%d %s files\033[K\n", completedJobs.Load(), len(indices), strings.ToLower(category))
				return
			}
		}
	}()

	go func() {
		for _, idx := range indices {
			jobs <- idx
		}
		close(jobs)
	}()

	go func() {
		wg.Wait()
		close(progressDone)
		close(results)
	}()

	resultMap := make(map[int]analysisResult, len(indices))
	for res := range results {
		resultMap[res.index] = res
	}
	return resultMap
}

// analyzeOne evaluates a single media item. It runs in a worker goroutine and
// does not mutate shared state other than the item at its own index.
func (e *Engine) analyzeOne(media []models.ShrinkMedia, idx int) analysisResult {
	m := &media[idx]
	result := analysisResult{index: idx, media: *m}

	processor := e.registry.GetProcessor(m)
	if processor == nil {
		slog.Warn("No processor found for file", "path", m.Path, "ext", m.Ext)
		result.skip = true
		return result
	}

	m.Category = processor.Category()
	// Check for 4K+ resolution videos and assign to a separate category.
	// 4K UHD is 3840x2160, so we check for any dimension >= 2160.
	if m.Category == "Video" && (m.Height >= 2160 || m.Width >= 2160) {
		m.Category = "Video4K"
	}
	e.metrics.RecordStarted(m.DisplayCategory(), m.Path)

	if m.Category == "Archived" && isSecondaryPart(m.Path) {
		// Secondary parts belong to the primary archive. Moving them
		// independently would break extraction.
		e.metrics.RecordSkipped(m.DisplayCategory())
		result.skip = true
		result.media = *m
		return result
	}

	// Estimate size and time. For archives, check the cache first.
	slog.Debug("Estimating size", "path", m.Path)
	var info models.ProcessableInfo
	if m.Category == "Archived" && len(e.sqlDBs) > 0 {
		info = e.getArchiveEstimateWithCache(m, processor)
	} else {
		info = processor.EstimateSize(m, e.cfg)
	}
	slog.Debug("Estimate complete", "path", m.Path, "processable", info.IsProcessable, "timeout", info.IsTimeout)

	// A timeout is not a skip: the file was never successfully analyzed and
	// must not be moved as if it had no shrinkable content.
	if info.IsTimeout {
		slog.Debug("Archive analysis timed out, skipping without moving", "path", m.Path)
		result.failed = true
		result.media = *m
		return result
	}

	if info.IsBroken {
		m.IsBroken = true
		m.PartFiles = info.PartFiles
		result.broken = true
	} else if !info.IsProcessable {
		// Case 1: Not processable (e.g., archive with no shrinkable content)
		e.metrics.RecordSkipped(m.DisplayCategory())
		result.skip = true
		result.skipMove = e.engCfg.Move != ""
	} else {
		// Use actual size if provided (e.g. multi-part archives)
		if info.ActualSize > 0 {
			m.Size = info.ActualSize
		}

		if !m.ShouldShrink(info.FutureSize, e.cfg) {
			// Case 2: No savings expected
			e.metrics.RecordSkipped(m.DisplayCategory())
			result.skip = true
			result.skipMove = e.engCfg.Move != ""
		} else {
			m.FutureSize = info.FutureSize
			m.ProcessingTime = info.ProcessingTime
			m.Savings = m.Size - info.FutureSize
			// Record queued time for ETA calculation
			e.metrics.RecordQueuedTime(m.DisplayCategory(), info.ProcessingTime)
		}
	}

	result.media = *m
	return result
}

// ============================================================================
// Processing Orchestration
// ============================================================================

// processWorker handles a single media item with proper metrics tracking.
// It ensures RecordStopped is always called, even on context cancellation.
func (e *Engine) processWorker(ctx context.Context, m models.ShrinkMedia, stopAll *atomic.Bool, cancel context.CancelFunc) {
	displayCat := m.DisplayCategory()
	path := m.Path
	e.metrics.RecordRunning(displayCat, path, m.ProcessingTime)

	// Ensure Running count is decremented even on cancel
	stopped := false
	defer func() {
		if !stopped {
			e.metrics.RecordStopped(displayCat, path)
		}
	}()

	// Check schedule: wait until estimated finish time falls within active period
	if len(e.engCfg.ActiveTime) > 0 {
		processingDuration := time.Duration(m.ProcessingTime) * time.Second
		now := time.Now()

		// Calculate wait time to ensure we finish during active hours
		waitDuration := utils.CalculateWaitDurationForFinish(now, processingDuration, e.engCfg.ActiveTime)

		if waitDuration > 0 {
			slog.Info("Schedule: waiting until active period",
				"path", m.Path,
				"wait", waitDuration.Round(time.Second),
				"estimated_finish", now.Add(waitDuration).Add(processingDuration).Format("15:04"))

			// Wait with context cancellation support
			select {
			case <-time.After(waitDuration):
				slog.Info("Schedule: active period started, processing", "path", m.Path)
			case <-ctx.Done():
				slog.Info("Schedule: waiting cancelled", "path", m.Path)
				return
			}
		}
	}

	result := e.processSingle(ctx, m)
	stopped = true
	e.metrics.RecordStopped(displayCat, path)

	// Check for stop-all signal (environment error)
	if result.StopAll {
		stopAll.Store(true)
		cancel()
	}
}

// processMedia manages the concurrent processing of media files.
func (e *Engine) processMedia(ctx context.Context, media []models.ShrinkMedia) {
	var wg sync.WaitGroup
	done := make(chan struct{})

	// StopAll flag and context cancellation for environment errors
	var stopAll atomic.Bool
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Progress printer goroutine
	go func() {
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				e.metrics.PrintProgress()
			case <-ctx.Done():
				e.metrics.SetCancelled(true)
				e.metrics.PrintProgress()
			case <-done:
				e.metrics.PrintProgress() // Final update
				return
			}
		}
	}()

	// Define queues and categories
	categories := []string{"Video", "Video4K", "Audio", "Image", "Text", "Archived"}
	queues := make(map[string]chan models.ShrinkMedia)
	for _, cat := range categories {
		queues[cat] = make(chan models.ShrinkMedia)
	}

	// Spawn workers for each category
	for _, cat := range categories {
		threads := 1
		switch cat {
		case "Video":
			threads = e.engCfg.VideoThreads
		case "Video4K":
			threads = e.engCfg.Video4KThreads
		case "Audio":
			threads = e.engCfg.AudioThreads
		case "Image":
			threads = e.engCfg.ImageThreads
		case "Text":
			threads = e.engCfg.TextThreads
		case "Archived":
			threads = 1
		}

		for i := 0; i < threads; i++ {
			wg.Add(1)
			go func(q chan models.ShrinkMedia) {
				defer wg.Done()
				for m := range q {
					e.processWorker(ctx, m, &stopAll, cancel)
				}
			}(queues[cat])
		}
	}

	// Distribute tasks per category to avoid blocking
	for _, cat := range categories {
		wg.Add(1)
		go func(targetCat string, q chan models.ShrinkMedia) {
			defer wg.Done()
			defer close(q) // Always close channel on exit
			for i := range media {
				if stopAll.Load() || ctx.Err() != nil {
					return
				}
				m := &media[i]
				mCat := m.Category
				if mCat == "" {
					mCat = "Archived"
				}
				if mCat == targetCat {
					select {
					case q <- *m:
					case <-ctx.Done():
						return
					}
				}
			}
		}(cat, queues[cat])
	}

	wg.Wait()
	close(done)

	// Give progress printer a moment to finish final update
	time.Sleep(50 * time.Millisecond)

	// Clear progress display before returning
	e.metrics.ClearProgress()
}

// ============================================================================
// Single File Processing
// ============================================================================

// processSingle handles the processing of a single media file.
func (e *Engine) processSingle(ctx context.Context, m models.ShrinkMedia) models.ProcessResult {
	// Handle broken archives - move to --move-broken without processing
	if m.IsBroken {
		return e.handleBrokenArchive(m)
	}

	// Capture original timestamps before processing
	originalAtime, originalMtime, err := e.captureTimestamps(m.Path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// File doesn't exist - mark as deleted (no status code needed)
			slog.Info("File not found, marking as deleted", "path", m.Path)
			e.metrics.RecordSkipped(m.DisplayCategory())
			db.MarkDeleted(e.sqlDBs, m.Path)
		} else {
			// Permission, I/O, or mount errors must not be treated as deletions.
			slog.Error("Failed to access file, leaving database state unchanged", "path", m.Path, "error", err)
			e.metrics.RecordSkipped(m.DisplayCategory())
		}
		return models.ProcessResult{SourcePath: m.Path, Error: err}
	}

	slog.Info("Processing",
		"path", m.Path,
		"category", m.Category,
		"size", utils.FormatSize(m.Size))

	processor := e.registry.GetProcessor(&m)
	if processor == nil {
		e.metrics.RecordFailure(m.DisplayCategory(), 0)
		return models.ProcessResult{SourcePath: m.Path, Error: fmt.Errorf("no processor found")}
	}

	processCtx, cancel := context.WithTimeout(ctx, e.getTimeout(m))
	defer cancel()

	startTime := time.Now()
	result := processor.Process(processCtx, &m, e.cfg, e.registry)
	elapsedSeconds := time.Since(startTime).Seconds()

	// Handle processing errors
	if result.Error != nil {
		return e.handleProcessingError(processCtx, m, result, elapsedSeconds)
	}

	// Handle unsuccessful processing (e.g., invalid output)
	if !result.Success {
		return e.handleUnsuccessfulProcessing(m, result, elapsedSeconds)
	}

	// Calculate new size and compare with original
	var totalNewSize int64
	for _, out := range result.Outputs {
		totalNewSize += out.Size
	}

	keepNewFiles := !e.cfg.Common.DeleteLarger || e.cfg.Common.ForceShrink || totalNewSize <= m.Size

	e.finalizeFileSwap(m, result, keepNewFiles)

	if keepNewFiles {
		e.finalizeSuccessfulProcessing(ctx, &m, result, originalAtime, originalMtime, elapsedSeconds)
	} else {
		// Note: If keepNewFiles is false, finalizeFileSwap already marked as TooLarge
		e.metrics.RecordFailure(m.DisplayCategory(), elapsedSeconds)
	}

	return result
}

// handleBrokenArchive handles broken archives by moving them to the broken directory.
func (e *Engine) handleBrokenArchive(m models.ShrinkMedia) models.ProcessResult {
	slog.Info("Broken archive detected, moving to broken directory", "path", m.Path)
	if e.cfg.Common.MoveBroken != "" {
		e.ui.MoveToBroken(m.Path, m.PartFiles)
	}
	// Mark as broken (don't mark as deleted - file is moved, not deleted)
	db.MarkBroken(e.sqlDBs, m.Path)
	e.metrics.RecordFailure(m.DisplayCategory(), 0)
	return models.ProcessResult{SourcePath: m.Path, Success: false}
}

// captureTimestamps captures the original access and modification times of a file.
func (e *Engine) captureTimestamps(path string) (time.Time, time.Time, error) {
	stat, err := os.Stat(path)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	return utils.GetAccessTime(stat), stat.ModTime(), nil
}

// handleProcessingError handles errors from processing.
func (e *Engine) handleProcessingError(ctx context.Context, m models.ShrinkMedia, result models.ProcessResult, elapsed float64) models.ProcessResult {
	// Log the error (including timeouts and cancellations for visibility)
	switch result.Error {
	case context.Canceled:
		slog.Warn("Processing canceled by user", "path", m.Path)
		// Don't mark as shrinked - allow retry on next run
	case context.DeadlineExceeded:
		slog.Error("Processing timed out", "path", m.Path)
		// Don't mark as shrinked - allow retry on next run
	default:
		if result.Output != "" {
			slog.Error("Processing failed", "path", m.Path, "error", result.Error, "output", result.Output)
		} else {
			slog.Error("Processing failed", "path", m.Path, "error", result.Error)
		}
	}
	e.metrics.RecordFailure(m.DisplayCategory(), elapsed)

	// Check for user-initiated cancellation (Ctrl+C)
	// When using systemd-run --scope, both Ctrl+C and OOM result in "signal: killed"
	// We distinguish by checking if the parent context was canceled
	isUserCancel := result.Error == context.Canceled ||
		(ctx.Err() == context.Canceled && strings.Contains(result.Error.Error(), "signal: killed"))

	// Check for environment errors (OOM kill, memory limit, hardware failure)
	// OOM kills happen when context is NOT canceled but process receives signal
	// These are file-specific errors, not environment-wide (don't stop all processing)
	isOOMKill := result.StopAll ||
		strings.Contains(result.Error.Error(), "exceeded memory limit") ||
		(ctx.Err() == nil && strings.Contains(result.Error.Error(), "signal: killed"))

	if isUserCancel {
		// User canceled - don't mark file status, allow retry on next run
		slog.Debug("Skipping database update due to user cancellation", "path", m.Path)
	} else if isOOMKill {
		// OOM kill - file is likely still playable, just heavy to convert
		// Mark as error but don't delete or mark as broken/unplayable
		slog.Debug("Marking file as error due to OOM/environment kill", "path", m.Path)
		if e.cfg.Common.MoveBroken != "" {
			e.ui.MoveToBroken(m.Path, result.PartFiles)
			db.MarkBroken(e.sqlDBs, m.Path)
		} else {
			// Just mark as error - don't delete (file is likely still playable)
			db.MarkProcessingError(e.sqlDBs, m.Path)
		}
	} else {
		// Mark as processing error or broken based on context
		if e.cfg.Common.MoveBroken != "" {
			e.ui.MoveToBroken(m.Path, result.PartFiles)
			db.MarkBroken(e.sqlDBs, m.Path)
		} else if e.cfg.Common.DeleteUnplayable && isUnplayableVideoAudio(m, result.Error) {
			// Only delete if ffprobe failed on video/audio files
			// For other media types, ffprobe failure is too risky to delete
			db.MarkDeleted(e.sqlDBs, m.Path)
			db.MarkUnplayable(e.sqlDBs, m.Path)
			_ = os.Remove(m.Path)
			if result.SourcePath != "" && result.SourcePath != m.Path {
				_ = os.Remove(result.SourcePath)
			}
		} else {
			// Just mark as error without moving/deleting
			db.MarkProcessingError(e.sqlDBs, m.Path)
		}
	}

	// Clean up any partial outputs and intermediate source files
	for _, out := range result.Outputs {
		if out.Path != m.Path && out.Path != result.SourcePath {
			_ = os.RemoveAll(out.Path)
		}
	}
	if result.SourcePath != "" && result.SourcePath != m.Path {
		// Only remove intermediate source if we're not deleting unplayable and not user-canceled
		if !isUnplayableVideoAudio(m, result.Error) && !isUserCancel {
			_ = os.Remove(result.SourcePath)
		}
	}

	return result
}

// isUnplayableVideoAudio checks if the error indicates an unplayable video/audio file.
// This is used to determine if --delete-unplayable should apply.
// Only returns true for video/audio files where ffprobe or ffmpeg detected corruption.
func isUnplayableVideoAudio(m models.ShrinkMedia, err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	// Check if error is from ffprobe or file corruption
	if !strings.Contains(errStr, "ffprobe failed") && !strings.Contains(errStr, "file error") {
		return false
	}
	// Only apply to video/audio categories
	return m.Category == "Video" || m.Category == "Video4K" || m.Category == "Audio"
}

// handleUnsuccessfulProcessing handles processing that succeeded but produced no valid output.
func (e *Engine) handleUnsuccessfulProcessing(m models.ShrinkMedia, result models.ProcessResult, elapsed float64) models.ProcessResult {
	// Processing succeeded but produced no valid output (e.g. invalid file)
	e.metrics.RecordFailure(m.DisplayCategory(), elapsed)

	if e.cfg.Common.MoveBroken != "" {
		// handleUnsuccessfulProcessing doesn't have PartFiles, but we can call it with nil
		e.ui.MoveToBroken(m.Path, nil)
		db.MarkBroken(e.sqlDBs, m.Path)
	} else if e.cfg.Common.DeleteUnplayable && isUnplayableVideoAudio(m, result.Error) {
		// Only delete if ffprobe failed on video/audio files
		// For other media types, ffprobe failure is too risky to delete
		db.MarkDeleted(e.sqlDBs, m.Path)
		db.MarkUnplayable(e.sqlDBs, m.Path)
		_ = os.Remove(m.Path)
		if result.SourcePath != "" && result.SourcePath != m.Path {
			_ = os.Remove(result.SourcePath)
		}
	} else {
		// Mark as error without moving/deleting
		db.MarkProcessingError(e.sqlDBs, m.Path)
	}

	// Clean up any partial outputs and intermediate source files
	for _, out := range result.Outputs {
		if out.Path != m.Path && out.Path != result.SourcePath {
			_ = os.RemoveAll(out.Path)
		}
	}
	if result.SourcePath != "" && result.SourcePath != m.Path {
		if !isUnplayableVideoAudio(m, result.Error) {
			_ = os.Remove(result.SourcePath)
		}
	}

	return models.ProcessResult{SourcePath: m.Path, Success: false}
}

// finalizeSuccessfulProcessing handles post-processing for successful operations.
func (e *Engine) finalizeSuccessfulProcessing(ctx context.Context, m *models.ShrinkMedia, result models.ProcessResult,
	originalAtime, originalMtime time.Time, elapsed float64,
) {
	e.preserveTimestamps(ctx, m, result, originalAtime, originalMtime)
	for _, out := range result.Outputs {
		e.ui.MoveTo(out.Path)
	}
	e.updateMetadata(*m, result)

	var totalNewSize int64
	for _, out := range result.Outputs {
		totalNewSize += out.Size
	}
	e.metrics.RecordSuccess(m.DisplayCategory(), m.Size, totalNewSize, elapsed, int64(m.Duration))
}

// finalizeFileSwap handles the actual file replacement or cleanup.
func (e *Engine) finalizeFileSwap(m models.ShrinkMedia, result models.ProcessResult, keepNewFiles bool) {
	if keepNewFiles {
		// Keep new files, delete original and any part files
		// Also delete any intermediate source files (like .ocr.pdf) if different from result.SourcePath
		if m.Path != "" {
			// Check if m.Path is one of the outputs we are keeping
			isOutput := false
			for _, out := range result.Outputs {
				if pathsEqual(out.Path, m.Path) {
					isOutput = true
					break
				}
			}

			if !isOutput {
				db.MarkDeleted(e.sqlDBs, m.Path)
				_ = os.Remove(m.Path)
			}
		}

		// If result.SourcePath was changed (e.g. by OCR) and it's not in the outputs, delete it too
		if result.SourcePath != "" && !pathsEqual(result.SourcePath, m.Path) {
			foundInOutputs := false
			for _, out := range result.Outputs {
				if pathsEqual(out.Path, result.SourcePath) {
					foundInOutputs = true
					break
				}
			}
			if !foundInOutputs {
				_ = os.Remove(result.SourcePath)
			}
		}

		// Delete part files for archives
		for _, partFile := range result.PartFiles {
			if !filepath.IsAbs(partFile) {
				partFile = filepath.Join(filepath.Dir(m.Path), partFile)
			}
			if !pathsEqual(partFile, m.Path) && !pathsEqual(partFile, result.SourcePath) {
				_ = os.Remove(partFile)
			}
		}
	} else {
		// Delete new files, keep original - mark as too large (don't mark deleted)
		db.MarkTooLarge(e.sqlDBs, m.Path)

		for _, out := range result.Outputs {
			if !pathsEqual(out.Path, m.Path) && !pathsEqual(out.Path, result.SourcePath) {
				_ = os.RemoveAll(out.Path)
			}
		}
		// If an intermediate source was created (e.g. OCR), delete it too
		if result.SourcePath != "" && !pathsEqual(result.SourcePath, m.Path) {
			_ = os.Remove(result.SourcePath)
		}
	}
}

// ============================================================================
// Metadata and Timestamps
// ============================================================================

func (e *Engine) updateMetadata(m models.ShrinkMedia, result models.ProcessResult) {
	for _, out := range result.Outputs {
		// We use updateDatabase when the original is replaced by a single output
		// to preserve metadata like play_count, etc.
		// Except for archives, where we want to keep the archive record as deleted.
		if len(result.Outputs) == 1 && !pathsEqual(out.Path, m.Path) && m.Category != "Archived" {
			db.UpdateMedia(e.sqlDBs, m.Path, out.Path, out.Size, m.Duration, m.Width, m.Height)
		} else if !pathsEqual(out.Path, m.Path) {
			db.AddMediaEntryWithDimensions(e.sqlDBs, out.Path, out.Size, m.Duration, m.Width, m.Height, db.ShrinkStatusSuccess)
		} else {
			db.MarkSuccess(e.sqlDBs, out.Path)
		}
	}
}

func (e *Engine) preserveTimestamps(ctx context.Context, m *models.ShrinkMedia, result models.ProcessResult, originalAtime, originalMtime time.Time) {
	if len(result.Outputs) > 0 && !originalAtime.IsZero() {
		outPath := result.Outputs[0].Path
		if outPath != "" {
			applyTimestamps(outPath, originalAtime, originalMtime)
			// Update duration if needed
			if m.Category == "Audio" || m.Category == "Video" {
				if newDuration := e.getActualDuration(ctx, outPath); newDuration > 0 {
					m.Duration = newDuration
				}
			}
		}
	}
}

// ============================================================================
// Utilities
// ============================================================================

func (e *Engine) getTimeout(m models.ShrinkMedia) time.Duration {
	timeoutMult := 1.0
	if utils.HasUnreliableDuration(m.Ext) {
		timeoutMult = 2.0 // Double timeout for unreliable formats (VOB, etc)
	}

	switch m.Category {
	case "Video":
		duration := utils.GetDurationForTimeout(m.Duration, m.Size, m.Ext)
		if duration > 30 {
			return time.Duration(duration*e.engCfg.Timeout.VideoTimeoutMult*timeoutMult) * time.Second
		}
		return utils.ParseDurationString(e.engCfg.Timeout.VideoTimeout)
	case "Audio":
		duration := m.Duration
		if duration > 30 {
			return time.Duration(duration*e.engCfg.Timeout.AudioTimeoutMult*timeoutMult) * time.Second
		}
		return utils.ParseDurationString(e.engCfg.Timeout.AudioTimeout)
	case "Image":
		return utils.ParseDurationString(e.engCfg.Timeout.ImageTimeout)
	case "Text":
		return utils.ParseDurationString(e.engCfg.Timeout.TextTimeout)
	case "Archived":
		// 100 seconds per GB, with a minimum of 10 minutes
		timeout := float64(m.Size) / (1024 * 1024 * 1024) * archiveTimeoutPerGB
		if timeout < minArchiveTimeoutSec {
			timeout = minArchiveTimeoutSec
		}
		return time.Duration(timeout) * time.Second
	default:
		return defaultTimeout
	}
}

// getActualDuration probes a file and returns its actual duration.
func (e *Engine) getActualDuration(ctx context.Context, path string) float64 {
	cmd := exec.CommandContext(ctx, "ffprobe",
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

	duration, err := strconv.ParseFloat(result.Format.Duration, 64)
	if err != nil {
		return 0
	}
	return duration
}

// getArchiveEstimateWithCache retrieves archive estimation with caching support.
// It checks the database cache first, and if not found, performs the estimation
// and updates the cache (only if analysis was successful - no timeouts).
func (e *Engine) getArchiveEstimateWithCache(m *models.ShrinkMedia, processor models.MediaProcessor) models.ProcessableInfo {
	sourceSize, sourceMtime := archiveSourceMetadata(m.Path)

	// Try to get from cache (use first available DB). Ignore entries that were
	// recorded for a different version of the file.
	if len(e.sqlDBs) > 0 {
		cached, err := db.GetArchiveCache(e.sqlDBs[0], m.Path)
		if err == nil && cached != nil {
			if sourceSize > 0 && sourceMtime > 0 &&
				cached.SourceSize == sourceSize && cached.SourceMtime == sourceMtime {
				slog.Debug("Using cached archive analysis", "path", m.Path)
				return models.ProcessableInfo{
					FutureSize:     cached.FutureSize,
					ProcessingTime: cached.ProcessingTime,
					IsProcessable:  cached.HasProcessable,
					ActualSize:     cached.TotalArchiveSize,
					IsBroken:       cached.IsBroken,
					PartFiles:      splitPartFiles(cached.PartFiles),
				}
			}
			slog.Debug("Ignoring stale archive cache entry", "path", m.Path,
				"cached_size", cached.SourceSize, "cached_mtime", cached.SourceMtime,
				"size", sourceSize, "mtime", sourceMtime)
		}
	}

	// Perform estimation
	info := processor.EstimateSize(m, e.cfg)

	// Update cache (use first available DB)
	// Only cache if analysis was successful (no timeouts)
	if !e.engCfg.Simulate && len(e.sqlDBs) > 0 && !info.IsTimeout {
		entry := &db.ArchiveCacheEntry{
			Path:             m.Path,
			TotalArchiveSize: info.ActualSize,
			FutureSize:       info.FutureSize,
			ProcessingTime:   info.ProcessingTime,
			HasProcessable:   info.IsProcessable,
			IsBroken:         info.IsBroken,
			PartFiles:        strings.Join(info.PartFiles, "|"),
			SourceSize:       sourceSize,
			SourceMtime:      sourceMtime,
		}

		if err := db.SetArchiveCache(e.sqlDBs[0], entry); err != nil {
			slog.Debug("Failed to cache archive analysis", "path", m.Path, "error", err)
		}
	}

	return info
}

func archiveSourceMetadata(path string) (size, mtime int64) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, 0
	}
	return info.Size(), info.ModTime().Unix()
}

// splitPartFiles decodes the pipe-separated cache representation. An empty
// value must decode to no parts, not to a single empty path.
func splitPartFiles(encoded string) []string {
	if encoded == "" {
		return nil
	}
	raw := strings.Split(encoded, "|")
	parts := make([]string, 0, len(raw))
	for _, part := range raw {
		if part != "" {
			parts = append(parts, part)
		}
	}
	return parts
}
