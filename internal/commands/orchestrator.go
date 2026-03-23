package commands

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
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
	VideoThreads int
	AudioThreads int
	ImageThreads int
	TextThreads  int
	Timeout      TimeoutFlags
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
	var toShrink []models.ShrinkMedia

	for i := range media {
		m := &media[i]
		processor := e.registry.GetProcessor(m)
		if processor == nil {
			// This should not happen after filterByTools, but log if it does
			slog.Warn("No processor found for file", "path", m.Path, "ext", m.Ext)
			continue
		}

		// Get processor's category
		m.Category = processor.Category()
		e.metrics.RecordStarted(m.DisplayCategory(), m.Path)

		// Estimate size and time
		info := processor.EstimateSize(m, e.cfg)

		if info.IsBroken {
			m.IsBroken = true
			m.PartFiles = info.PartFiles
			toShrink = append(toShrink, *m)
			continue
		}

		if !info.IsProcessable {
			e.metrics.RecordSkipped(m.DisplayCategory())
			continue
		}

		// Use actual size if provided (e.g. multi-part archives)
		if info.ActualSize > 0 {
			m.Size = info.ActualSize
		}

		// Check if we should shrink
		if m.ShouldShrink(info.FutureSize, e.cfg) {
			m.FutureSize = info.FutureSize
			m.ProcessingTime = info.ProcessingTime
			m.Savings = m.Size - info.FutureSize
			toShrink = append(toShrink, *m)
		} else {
			e.metrics.RecordSkipped(m.DisplayCategory())
		}
	}

	return toShrink
}

// ============================================================================
// Worker Pool
// ============================================================================

// WorkerPool manages concurrent execution using semaphores per media category.
type WorkerPool struct {
	sems map[string]chan struct{}
}

// NewWorkerPool initializes a worker pool with limits from EngineConfig.
func NewWorkerPool(cfg EngineConfig) *WorkerPool {
	return &WorkerPool{
		sems: map[string]chan struct{}{
			"Video":    make(chan struct{}, cfg.VideoThreads),
			"Audio":    make(chan struct{}, cfg.AudioThreads),
			"Image":    make(chan struct{}, cfg.ImageThreads),
			"Text":     make(chan struct{}, cfg.TextThreads),
			"Archived": make(chan struct{}, 1),
		},
	}
}

// Acquire blocks until a worker slot is available for the given category.
func (wp *WorkerPool) Acquire(ctx context.Context, category string) (func(), error) {
	// Normalize category name for case-insensitive lookup
	var cat string
	switch strings.ToLower(strings.TrimSpace(category)) {
	case "video":
		cat = "Video"
	case "audio":
		cat = "Audio"
	case "image":
		cat = "Image"
	case "text":
		cat = "Text"
	default:
		cat = "Archived"
	}

	sem := wp.sems[cat]
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case sem <- struct{}{}:
		return func() { <-sem }, nil
	}
}

// ============================================================================
// Processing Orchestration
// ============================================================================

// processMedia manages the concurrent processing of media files.
func (e *Engine) processMedia(ctx context.Context, media []models.ShrinkMedia) {
	pool := NewWorkerPool(e.engCfg)
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

	for _, m := range media {
		wg.Add(1)
		go func(original models.ShrinkMedia) {
			defer wg.Done()

			// Skip if stopAll already set
			if stopAll.Load() {
				return
			}

			release, err := pool.Acquire(ctx, original.Category)
			if err != nil {
				return
			}
			defer release()

			// Record that the file is actually running now
			cat := original.Category
			path := original.Path
			e.metrics.RecordRunning(cat, path)
			defer e.metrics.RecordStopped(cat, path)

			result := e.processSingle(ctx, original)

			// Check for stop-all signal (environment error)
			if result.StopAll {
				stopAll.Store(true)
				cancel()
			}
		}(m)
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
		// File doesn't exist - mark as skipped (deleted)
		slog.Info("File not found, marking as skipped", "path", m.Path)
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
		return e.handleUnsuccessfulProcessing(m, elapsedSeconds)
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
		db.MarkShrinked(e.sqlDBs, m.Path)
		e.metrics.RecordSuccess(m.DisplayCategory(), m.Size, m.Size, elapsedSeconds, int64(m.Duration))
	}

	return result
}

// handleBrokenArchive handles broken archives by moving them to the broken directory.
func (e *Engine) handleBrokenArchive(m models.ShrinkMedia) models.ProcessResult {
	slog.Info("Broken archive detected, moving to broken directory", "path", m.Path)
	if e.cfg.Common.MoveBroken != "" {
		e.ui.MoveToBroken(m.Path, m.PartFiles)
	}
	db.MarkDeleted(e.sqlDBs, m.Path)
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
	} else if result.Error == context.DeadlineExceeded {
		slog.Error("Processing timed out", "path", m.Path)
	} else {
		if result.Output != "" {
			slog.Error("Processing failed", "path", m.Path, "error", result.Error, "output", result.Output)
		} else {
			slog.Error("Processing failed", "path", m.Path, "error", result.Error)
		}
	}
	e.metrics.RecordFailure(m.DisplayCategory(), elapsed)
	if e.cfg.Common.MoveBroken != "" {
		e.ui.MoveToBroken(m.Path, result.PartFiles)
	}
	return result
}

// handleUnsuccessfulProcessing handles processing that succeeded but produced no valid output.
func (e *Engine) handleUnsuccessfulProcessing(m models.ShrinkMedia, elapsed float64) models.ProcessResult {
	// Processing succeeded but produced no valid output (e.g. invalid file)
	if e.cfg.Common.DeleteUnplayable {
		db.MarkDeleted(e.sqlDBs, m.Path)
		os.Remove(m.Path)
	}
	e.metrics.RecordFailure(m.DisplayCategory(), elapsed)
	return models.ProcessResult{SourcePath: m.Path, Success: false}
}

// finalizeSuccessfulProcessing handles post-processing for successful operations.
func (e *Engine) finalizeSuccessfulProcessing(m *models.ShrinkMedia, result models.ProcessResult,
	originalAtime, originalMtime time.Time, elapsed float64,
) {
	e.updateMetadata(*m, result)
	e.preserveTimestamps(m, result, originalAtime, originalMtime)
	for _, out := range result.Outputs {
		e.ui.MoveTo(out.Path)
	}

	var totalNewSize int64
	for _, out := range result.Outputs {
		totalNewSize += out.Size
	}
	e.metrics.RecordSuccess(m.DisplayCategory(), m.Size, totalNewSize, elapsed, int64(m.Duration))
}

// finalizeFileSwap handles the actual file replacement or cleanup.
func (e *Engine) finalizeFileSwap(m models.ShrinkMedia, result models.ProcessResult, keepNewFiles bool) {
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
				db.MarkDeleted(e.sqlDBs, m.Path)
				os.Remove(m.Path)
			}
		}
	} else {
		// Delete new files, keep original
		for _, out := range result.Outputs {
			if out.Path != m.Path {
				os.RemoveAll(out.Path) // RemoveAll because it might be a directory (TextProcessor/ArchiveProcessor)
			}
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
		if len(result.Outputs) == 1 && out.Path != m.Path && m.Category != "Archived" {
			db.UpdateMedia(e.sqlDBs, m.Path, out.Path, out.Size, m.Duration)
		} else if out.Path != m.Path {
			db.AddMediaEntry(e.sqlDBs, out.Path, out.Size, m.Duration)
		} else {
			db.MarkShrinked(e.sqlDBs, out.Path)
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
