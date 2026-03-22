package commands

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"sort"
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

type Engine struct {
	cfg      *models.ProcessorConfig
	sqlDBs   []*sql.DB
	registry *MediaRegistry
	metrics  *ShrinkMetrics
	cmd      *ShrinkCmd // Still needed for some CLI-specific settings like Move, MoveBroken, etc.
}

func NewEngine(c *ShrinkCmd, cfg *models.ProcessorConfig, registry *MediaRegistry, metrics *ShrinkMetrics) *Engine {
	return &Engine{
		cfg:      cfg,
		sqlDBs:   c.sqlDBs,
		registry: registry,
		metrics:  metrics,
		cmd:      c,
	}
}

// ============================================================================
// Media Analysis
// ============================================================================

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
// Sorting
// ============================================================================

func (c *ShrinkCmd) sortByEfficiency(media []models.ShrinkMedia) {
	sort.Slice(media, func(i, j int) bool {
		timeI := max(media[i].ProcessingTime, 1)
		timeJ := max(media[j].ProcessingTime, 1)
		ratioI := float64(media[i].Savings) / float64(timeI)
		ratioJ := float64(media[j].Savings) / float64(timeJ)
		return ratioI > ratioJ
	})
}

// ============================================================================
// Summary and Output
// ============================================================================

func (c *ShrinkCmd) printSummary(media []models.ShrinkMedia) {
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

		key := m.DisplayCategory()
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
	headers := []string{"Media Type", "Count", "Current", "Future", "Savings", "ETA", "Speed"}
	var rows [][]string

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
		rows = append(rows, []string{
			key,
			strconv.Itoa(b.count),
			utils.FormatSize(b.size),
			utils.FormatSize(b.future),
			utils.FormatSize(b.savings),
			utils.FormatDuration(b.procTime),
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
		utils.FormatDuration(totalTime),
		"",
	})

	utils.PrintTable(headers, rows)
	fmt.Println()
}

func (c *ShrinkCmd) printUnknownExtensions() {
	// Combine unknown extensions and skipped by tool
	hasUnknown := len(c.unknownExtensions) > 0
	hasSkipped := len(c.skippedByTool) > 0

	if !hasUnknown && !hasSkipped {
		return
	}

	fmt.Println("Unknown File Extensions Scanned")
	headers := []string{"Extension", "Total Size"}
	var rows [][]string

	// Sort by size descending
	type extSize struct {
		ext  string
		size int64
	}
	var sorted []extSize

	for ext, size := range c.unknownExtensions {
		sorted = append(sorted, extSize{ext, size})
	}
	for ext, size := range c.skippedByTool {
		sorted = append(sorted, extSize{ext, size})
	}

	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].size > sorted[j].size
	})

	for _, es := range sorted {
		rows = append(rows, []string{es.ext, utils.FormatSize(es.size)})
	}
	utils.PrintTable(headers, rows)
	fmt.Println()
}

func (c *ShrinkCmd) confirm() bool {
	fmt.Print("Proceed with shrinking? [y/N] ")
	var response string
	fmt.Scanln(&response)
	return strings.ToLower(response) == "y"
}

// ============================================================================
// Worker Pool
// ============================================================================

type WorkerPool struct {
	sems map[string]chan struct{}
}

func NewWorkerPool(c *ShrinkCmd) *WorkerPool {
	return &WorkerPool{
		sems: map[string]chan struct{}{
			"Video":    make(chan struct{}, c.VideoThreads),
			"Audio":    make(chan struct{}, c.AudioThreads),
			"Image":    make(chan struct{}, c.ImageThreads),
			"Text":     make(chan struct{}, c.TextThreads),
			"Archived": make(chan struct{}, 1),
		},
	}
}

func (wp *WorkerPool) Acquire(ctx context.Context, category string) (func(), error) {
	// Normalize category name for case-insensitive lookup
	cat := strings.Title(strings.ToLower(strings.TrimSpace(category)))
	sem := wp.sems[cat]
	if sem == nil {
		slog.Debug("Unknown category, falling back to Archived limit", "category", category, "normalized", cat)
		sem = wp.sems["Archived"]
	}

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

func (e *Engine) processMedia(ctx context.Context, media []models.ShrinkMedia) {
	pool := NewWorkerPool(e.cmd)
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
			e.metrics.RecordRunning(original.DisplayCategory())

			// Set current file for progress display
			e.metrics.SetCurrentFile(original.Path)
			defer e.metrics.SetCurrentFile("")

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
		e.metrics.RecordFailure(m.DisplayCategory())
		return models.ProcessResult{SourcePath: m.Path, Error: fmt.Errorf("no processor found")}
	}

	processCtx, cancel := context.WithTimeout(ctx, e.getTimeout(m))
	defer cancel()

	result := processor.Process(processCtx, &m, e.cfg, e.registry)

	// Handle processing errors
	if result.Error != nil {
		return e.handleProcessingError(m, result)
	}

	// Handle unsuccessful processing (e.g., invalid output)
	if !result.Success {
		return e.handleUnsuccessfulProcessing(m)
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
		e.finalizeSuccessfulProcessing(&m, result, originalAtime, originalMtime)
	} else {
		db.MarkShrinked(e.sqlDBs, m.Path)
		e.metrics.RecordSuccess(m.DisplayCategory(), m.Size, m.Size, m.ProcessingTime, int64(m.Duration))
	}

	return result
}

// handleBrokenArchive handles broken archives by moving them to the broken directory
func (e *Engine) handleBrokenArchive(m models.ShrinkMedia) models.ProcessResult {
	slog.Info("Broken archive detected, moving to broken directory", "path", m.Path)
	if e.cfg.Common.MoveBroken != "" {
		e.cmd.moveToBroken(m.Path, m.PartFiles)
	}
	db.MarkDeleted(e.sqlDBs, m.Path)
	e.metrics.RecordFailure(m.DisplayCategory())
	return models.ProcessResult{SourcePath: m.Path, Success: false}
}

// captureTimestamps captures the original access and modification times of a file
func (e *Engine) captureTimestamps(path string) (time.Time, time.Time, error) {
	stat, err := os.Stat(path)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	return utils.GetAccessTime(stat), stat.ModTime(), nil
}

// handleProcessingError handles errors from processing
func (e *Engine) handleProcessingError(m models.ShrinkMedia, result models.ProcessResult) models.ProcessResult {
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
	e.metrics.RecordFailure(m.DisplayCategory())
	if e.cfg.Common.MoveBroken != "" {
		e.cmd.moveToBroken(m.Path, result.PartFiles)
	}
	return result
}

// handleUnsuccessfulProcessing handles processing that succeeded but produced no valid output
func (e *Engine) handleUnsuccessfulProcessing(m models.ShrinkMedia) models.ProcessResult {
	// Processing succeeded but produced no valid output (e.g. invalid file)
	if e.cfg.Common.DeleteUnplayable {
		db.MarkDeleted(e.sqlDBs, m.Path)
		os.Remove(m.Path)
	}
	e.metrics.RecordFailure(m.DisplayCategory())
	return models.ProcessResult{SourcePath: m.Path, Success: false}
}

// finalizeSuccessfulProcessing handles post-processing for successful operations
func (e *Engine) finalizeSuccessfulProcessing(m *models.ShrinkMedia, result models.ProcessResult,
	originalAtime, originalMtime time.Time,
) {
	e.updateMetadata(*m, result)
	e.preserveTimestamps(m, result, originalAtime, originalMtime)
	for _, out := range result.Outputs {
		e.cmd.moveTo(out.Path)
	}

	var totalNewSize int64
	for _, out := range result.Outputs {
		totalNewSize += out.Size
	}
	e.metrics.RecordSuccess(m.DisplayCategory(), m.Size, totalNewSize, m.ProcessingTime, int64(m.Duration))
}

// finalizeFileSwap handles the actual file replacement or cleanup
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
			return time.Duration(duration*e.cmd.VideoTimeoutMult*timeoutMult) * time.Second
		}
		return utils.ParseDurationString(e.cmd.VideoTimeout)
	case "Audio":
		duration := m.Duration
		if duration > 30 {
			return time.Duration(duration*e.cmd.AudioTimeoutMult*timeoutMult) * time.Second
		}
		return utils.ParseDurationString(e.cmd.AudioTimeout)
	case "Image":
		return utils.ParseDurationString(e.cmd.ImageTimeout)
	case "Text":
		return utils.ParseDurationString(e.cmd.TextTimeout)
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

func (c *ShrinkCmd) applyContinueFrom(media []models.ShrinkMedia) []models.ShrinkMedia {
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

// getActualDuration probes a file and returns its actual duration
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
