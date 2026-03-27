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

// ============================================================================
// Engine
// ============================================================================

// UI interface for user interaction and file operations.
// It abstracts CLI-specific actions from the core engine.
type UI interface {
	Confirm() bool
	MoveTo(path string)
	MoveToBroken(path string, partFiles []string)
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

// analyzeMedia evaluates each media item to determine if it should be processed.
func (e *Engine) analyzeMedia(media []models.ShrinkMedia) []models.ShrinkMedia {
	slog.Info("Starting media analysis", "total_files", len(media))

	// Determine analysis parallelism
	targetConcurrency := e.engCfg.AnalysisThreads
	if targetConcurrency <= 0 {
		targetConcurrency = runtime.NumCPU() * 4
	}
	slog.Info("Analysis parallelism configured", "workers", targetConcurrency)

	// Channel for distributing work
	jobs := make(chan int, len(media))
	results := make(chan struct {
		index  int
		media  models.ShrinkMedia
		skip   bool
		broken bool
		failed bool
	}, len(media))

	var wg sync.WaitGroup
	var completedJobs int64
	var failedJobs int64
	var activeWorkers int32
	var totalWorkerSamples int64
	var workerSum int64
	var concurrency atomic.Int32
	concurrency.Store(int32(targetConcurrency))

	// Worker function
	startWorker := func() {
		wg.Go(func() {
			atomic.AddInt32(&activeWorkers, 1)
			defer atomic.AddInt32(&activeWorkers, -1)

			for {
				if atomic.LoadInt32(&activeWorkers) > concurrency.Load() {
					return // Scale down
				}
				idx, ok := <-jobs
				if !ok {
					return
				}

				m := &media[idx]
				processor := e.registry.GetProcessor(m)
				skip := false
				broken := false
				failed := false

				if processor == nil {
					slog.Warn("No processor found for file", "path", m.Path, "ext", m.Ext)
					skip = true
				} else {
					// Get processor's category
					m.Category = processor.Category()
					// Check for 4K+ resolution videos and assign to separate category
					if m.Category == "Video" && (m.Width >= 2400 || m.Height >= 2400) {
						m.Category = "Video4K"
					}
					e.metrics.RecordStarted(m.DisplayCategory(), m.Path)

					// Estimate size and time
					slog.Debug("Estimating size", "path", m.Path)
					info := processor.EstimateSize(m, e.cfg)
					slog.Debug("Estimate complete", "path", m.Path, "processable", info.IsProcessable)

					if info.IsBroken {
						m.IsBroken = true
						m.PartFiles = info.PartFiles
						broken = true
					} else if !info.IsProcessable {
						e.metrics.RecordSkipped(m.DisplayCategory())
						skip = true
					} else {
						// Use actual size if provided (e.g. multi-part archives)
						if info.ActualSize > 0 {
							m.Size = info.ActualSize
						}

						// Check if we should shrink
						if !m.ShouldShrink(info.FutureSize, e.cfg) {
							e.metrics.RecordSkipped(m.DisplayCategory())
							skip = true
						} else {
							m.FutureSize = info.FutureSize
							m.ProcessingTime = info.ProcessingTime
							m.Savings = m.Size - info.FutureSize
						}
					}
				}

				// Check if estimation failed (archive timeout, etc.)
				// Failed analyses should not be counted as completed
				if m.Category == "Archived" && m.FutureSize == 0 && m.Size > 0 && !broken {
					// Archive analysis failed (likely timeout) - skip but don't count as completed
					failed = true
					atomic.AddInt64(&failedJobs, 1)
					slog.Debug("Archive analysis failed, skipping", "path", m.Path)
				}

				results <- struct {
					index  int
					media  models.ShrinkMedia
					skip   bool
					broken bool
					failed bool
				}{idx, *m, skip, broken, failed}

				if !failed {
					atomic.AddInt64(&completedJobs, 1)
				}
			}
		})
	}

	// Start initial workers
	for i := int32(0); i < concurrency.Load(); i++ {
		startWorker()
	}

	// Dynamic scaling monitor
	monitorDone := make(chan struct{})
	go func() {
		ticker := time.NewTicker(4500 * time.Millisecond)
		defer ticker.Stop()

		var lastCompleted int64
		var lastThroughput int64
		direction := int32(1)

		for {
			select {
			case <-ticker.C:
				completed := atomic.LoadInt64(&completedJobs)
				throughput := completed - lastCompleted
				lastCompleted = completed

				current := concurrency.Load()

				if throughput < lastThroughput {
					direction = -direction // Reverse direction if throughput drops
				} else if throughput == lastThroughput && throughput > 0 {
					direction = 1 // Gently push up if stable
				}

				newTarget := min(
					max(current+(direction*2), 1), 300)

				concurrency.Store(newTarget)

				active := atomic.LoadInt32(&activeWorkers)
				for active < newTarget {
					startWorker()
					active++
				}
				// Track worker statistics
				atomic.AddInt64(&workerSum, int64(active))
				atomic.AddInt64(&totalWorkerSamples, 1)
				lastThroughput = throughput
			case <-monitorDone:
				return
			}
		}
	}()

	// Progress reporter
	progressDone := make(chan struct{})
	go func() {
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				completed := atomic.LoadInt64(&completedJobs)
				failed := atomic.LoadInt64(&failedJobs)
				workers := atomic.LoadInt32(&activeWorkers)
				if completed > 0 || workers > 0 || failed > 0 {
					status := fmt.Sprintf("\rAnalyzing %d/%d files", completed, len(media))
					if failed > 0 {
						status += fmt.Sprintf(" (%d failed)", failed)
					}
					if workers > 0 {
						status += fmt.Sprintf(" (%d workers)", workers)
					} else if totalWorkerSamples > 0 {
						avgWorkers := float64(workerSum) / float64(totalWorkerSamples)
						status += fmt.Sprintf(" (avg: %.1f workers)", avgWorkers)
					}
					fmt.Printf("%s\033[K", status)
				}
			case <-progressDone:
				// Final update
				completed := atomic.LoadInt64(&completedJobs)
				failed := atomic.LoadInt64(&failedJobs)
				workers := atomic.LoadInt32(&activeWorkers)
				status := fmt.Sprintf("\rAnalyzed %d/%d files", completed, len(media))
				if failed > 0 {
					status += fmt.Sprintf(" (%d failed)", failed)
				}
				if workers == 0 && totalWorkerSamples > 0 {
					avgWorkers := float64(workerSum) / float64(totalWorkerSamples)
					status += fmt.Sprintf(" (avg: %.1f workers)", avgWorkers)
				}
				fmt.Printf("%s\033[K\n", status)
				return
			}
		}
	}()

	// Submit jobs
	go func() {
		for i := range media {
			jobs <- i
		}
		close(jobs)
	}()

	// Wait for completion and collect results
	go func() {
		wg.Wait()
		close(monitorDone)
		close(progressDone)
		close(results)
	}()

	// Collect results
	var toShrink []models.ShrinkMedia
	resultMap := make(map[int]models.ShrinkMedia)
	skipMap := make(map[int]bool)
	brokenMap := make(map[int]bool)
	failedMap := make(map[int]bool)

	for res := range results {
		resultMap[res.index] = res.media
		skipMap[res.index] = res.skip
		brokenMap[res.index] = res.broken
		failedMap[res.index] = res.failed
	}

	// Build final slice in original order
	// Failed analyses are skipped (not added to toShrink)
	for i := range media {
		if failedMap[i] {
			// Analysis failed (e.g., lsar timeout) - skip this file
			continue
		}
		if brokenMap[i] {
			toShrink = append(toShrink, resultMap[i])
		} else if !skipMap[i] {
			toShrink = append(toShrink, resultMap[i])
		}
	}

	slog.Info("Media analysis complete", "to_shrink", len(toShrink))
	return toShrink
}

// ============================================================================
// Processing Orchestration
// ============================================================================

// processWorker handles a single media item with proper metrics tracking.
// It ensures RecordStopped is always called, even on context cancellation.
func (e *Engine) processWorker(ctx context.Context, m models.ShrinkMedia, stopAll *atomic.Bool, cancel context.CancelFunc) {
	displayCat := m.DisplayCategory()
	path := m.Path
	e.metrics.RecordRunning(displayCat, path)

	// Ensure Running count is decremented even on cancel
	stopped := false
	defer func() {
		if !stopped {
			e.metrics.RecordStopped(displayCat, path)
		}
	}()

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
			for i := range media {
				if stopAll.Load() || ctx.Err() != nil {
					break
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
						break
					}
				}
			}
			close(q)
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
		// File doesn't exist - mark as deleted (no status code needed)
		slog.Info("File not found, marking as deleted", "path", m.Path)
		e.metrics.RecordSkipped(m.DisplayCategory())
		db.MarkDeleted(e.sqlDBs, m.Path)
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
		return e.handleProcessingError(m, result, elapsedSeconds)
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

	keepNewFiles := true
	if e.cfg.Common.DeleteLarger && !e.cfg.Common.ForceShrink && totalNewSize > m.Size {
		keepNewFiles = false
	}

	e.finalizeFileSwap(m, result, keepNewFiles)

	if keepNewFiles {
		e.finalizeSuccessfulProcessing(&m, result, originalAtime, originalMtime, elapsedSeconds)
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
func (e *Engine) handleProcessingError(m models.ShrinkMedia, result models.ProcessResult, elapsed float64) models.ProcessResult {
	// Log the error (including timeouts and cancellations for visibility)
	if result.Error == context.Canceled {
		slog.Warn("Processing canceled by user", "path", m.Path)
		// Don't mark as shrinked - allow retry on next run
	} else if result.Error == context.DeadlineExceeded {
		slog.Error("Processing timed out", "path", m.Path)
		// Don't mark as shrinked - allow retry on next run
	} else {
		if result.Output != "" {
			slog.Error("Processing failed", "path", m.Path, "error", result.Error, "output", result.Output)
		} else {
			slog.Error("Processing failed", "path", m.Path, "error", result.Error)
		}
	}
	e.metrics.RecordFailure(m.DisplayCategory(), elapsed)

	// Don't move or delete files if processing was interrupted by user or system signal
	isInterrupted := result.Error == context.Canceled || strings.Contains(result.Error.Error(), "signal: killed")

	// Check for memory limit exceeded (environment error)
	isEnvironmentErr := result.StopAll || strings.Contains(result.Error.Error(), "exceeded memory limit")

	if isInterrupted || isEnvironmentErr {
		// Don't mark file status - allow retry on next run
		slog.Debug("Skipping database update due to interrupt/environment error", "path", m.Path)
	} else {
		// Mark as processing error or broken based on context
		if e.cfg.Common.MoveBroken != "" {
			e.ui.MoveToBroken(m.Path, result.PartFiles)
			db.MarkBroken(e.sqlDBs, m.Path)
		} else if e.cfg.Common.DeleteUnplayable {
			db.MarkDeleted(e.sqlDBs, m.Path)
			db.MarkUnplayable(e.sqlDBs, m.Path)
			os.Remove(m.Path)
			if result.SourcePath != "" && result.SourcePath != m.Path {
				os.Remove(result.SourcePath)
			}
		} else {
			// Just mark as error without moving/deleting
			db.MarkProcessingError(e.sqlDBs, m.Path)
		}
	}

	// Clean up any partial outputs and intermediate source files
	for _, out := range result.Outputs {
		if out.Path != m.Path && out.Path != result.SourcePath {
			os.RemoveAll(out.Path)
		}
	}
	if result.SourcePath != "" && result.SourcePath != m.Path {
		// Only remove intermediate source if we're not deleting unplayable (already handled)
		if !e.cfg.Common.DeleteUnplayable && !isInterrupted && !isEnvironmentErr {
			os.Remove(result.SourcePath)
		}
	}

	return result
}

// handleUnsuccessfulProcessing handles processing that succeeded but produced no valid output.
func (e *Engine) handleUnsuccessfulProcessing(m models.ShrinkMedia, result models.ProcessResult, elapsed float64) models.ProcessResult {
	// Processing succeeded but produced no valid output (e.g. invalid file)
	e.metrics.RecordFailure(m.DisplayCategory(), elapsed)

	if e.cfg.Common.MoveBroken != "" {
		// handleUnsuccessfulProcessing doesn't have PartFiles, but we can call it with nil
		e.ui.MoveToBroken(m.Path, nil)
		db.MarkBroken(e.sqlDBs, m.Path)
	} else if e.cfg.Common.DeleteUnplayable {
		db.MarkDeleted(e.sqlDBs, m.Path)
		db.MarkUnplayable(e.sqlDBs, m.Path)
		os.Remove(m.Path)
		if result.SourcePath != "" && result.SourcePath != m.Path {
			os.Remove(result.SourcePath)
		}
	} else {
		// Mark as error without moving/deleting
		db.MarkProcessingError(e.sqlDBs, m.Path)
	}

	// Clean up any partial outputs and intermediate source files
	for _, out := range result.Outputs {
		if out.Path != m.Path && out.Path != result.SourcePath {
			os.RemoveAll(out.Path)
		}
	}
	if result.SourcePath != "" && result.SourcePath != m.Path {
		if !e.cfg.Common.DeleteUnplayable {
			os.Remove(result.SourcePath)
		}
	}

	return models.ProcessResult{SourcePath: m.Path, Success: false}
}

// finalizeSuccessfulProcessing handles post-processing for successful operations.
func (e *Engine) finalizeSuccessfulProcessing(m *models.ShrinkMedia, result models.ProcessResult,
	originalAtime, originalMtime time.Time, elapsed float64,
) {
	e.preserveTimestamps(m, result, originalAtime, originalMtime)
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
				os.Remove(m.Path)
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
				os.Remove(result.SourcePath)
			}
		}

		// Delete part files for archives
		for _, partFile := range result.PartFiles {
			if !filepath.IsAbs(partFile) {
				partFile = filepath.Join(filepath.Dir(m.Path), partFile)
			}
			if !pathsEqual(partFile, m.Path) && !pathsEqual(partFile, result.SourcePath) {
				os.Remove(partFile)
			}
		}
	} else {
		// Delete new files, keep original - mark as too large (don't mark deleted)
		db.MarkTooLarge(e.sqlDBs, m.Path)

		for _, out := range result.Outputs {
			if !pathsEqual(out.Path, m.Path) && !pathsEqual(out.Path, result.SourcePath) {
				os.RemoveAll(out.Path)
			}
		}
		// If an intermediate source was created (e.g. OCR), delete it too
		if result.SourcePath != "" && !pathsEqual(result.SourcePath, m.Path) {
			os.Remove(result.SourcePath)
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
			db.UpdateMedia(e.sqlDBs, m.Path, out.Path, out.Size, m.Duration)
		} else if !pathsEqual(out.Path, m.Path) {
			db.AddMediaEntry(e.sqlDBs, out.Path, out.Size, m.Duration, db.ShrinkStatusSuccess)
		} else {
			db.MarkSuccess(e.sqlDBs, out.Path)
		}
	}
}

func (e *Engine) preserveTimestamps(m *models.ShrinkMedia, result models.ProcessResult, originalAtime, originalMtime time.Time) {
	if len(result.Outputs) > 0 && !originalAtime.IsZero() {
		outPath := result.Outputs[0].Path
		applyTimestamps(outPath, originalAtime, originalMtime)
		// Update duration if needed
		if m.Category == "Audio" || m.Category == "Video" {
			if newDuration := e.getActualDuration(outPath); newDuration > 0 {
				m.Duration = newDuration
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
		timeout := float64(m.Size) / (1024 * 1024 * 1024) * 100
		if timeout < 600 {
			timeout = 600
		}
		return time.Duration(timeout) * time.Second
	default:
		return 30 * time.Minute
	}
}

// getActualDuration probes a file and returns its actual duration.
func (e *Engine) getActualDuration(path string) float64 {
	cmd := exec.CommandContext(context.Background(), "ffprobe",
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
