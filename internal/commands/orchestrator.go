package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chapmanjacobd/shrink/internal/db"
	"github.com/chapmanjacobd/shrink/internal/models"
	"github.com/chapmanjacobd/shrink/internal/utils"
)

// ============================================================================
// Media Analysis
// ============================================================================

func (c *ShrinkCmd) analyzeMedia(media []models.ShrinkMedia, cfg *models.ProcessorConfig,
	registry *MediaRegistry, metrics *ShrinkMetrics,
) []models.ShrinkMedia {
	var toShrink []models.ShrinkMedia

	for i := range media {
		m := &media[i]
		processor := registry.GetProcessor(m)
		if processor == nil {
			// This should not happen after filterByTools, but log if it does
			slog.Warn("No processor found for file", "path", m.Path, "ext", m.Ext)
			continue
		}

		// Get processor's category
		m.Category = processor.Category()
		metrics.RecordStarted(m.DisplayCategory(), m.Path)

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
					} else {
						metrics.RecordSkipped(m.DisplayCategory())
					}
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
			metrics.RecordSkipped(m.DisplayCategory())
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
// Processing Orchestration
// ============================================================================

func (c *ShrinkCmd) processMedia(ctx context.Context, media []models.ShrinkMedia, registry *MediaRegistry,
	cfg *models.ProcessorConfig, metrics *ShrinkMetrics,
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
		go func(original models.ShrinkMedia) {
			defer wg.Done()

			// Check for cancellation before starting work
			select {
			case <-ctx.Done():
				return
			default:
			}

			sem := sems[original.Category]
			if sem == nil {
				sem = sems["Archived"]
			}

			// Also wait on sem with cancellation
			select {
			case <-ctx.Done():
				return
			case sem <- struct{}{}:
			}
			defer func() { <-sem }()

			// Set current file for progress display
			metrics.SetCurrentFile(original.Path)

			c.processSingle(ctx, original, registry, cfg, metrics)

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

// ============================================================================
// Single File Processing
// ============================================================================

func (c *ShrinkCmd) processSingle(ctx context.Context, m models.ShrinkMedia, registry *MediaRegistry,
	cfg *models.ProcessorConfig, metrics *ShrinkMetrics,
) models.ProcessResult {
	// Handle broken archives - move to --move-broken without processing
	if m.IsBroken {
		return c.handleBrokenArchive(m, cfg, metrics)
	}

	// Capture original timestamps before processing
	originalAtime, originalMtime, err := c.captureTimestamps(m.Path)
	if err != nil {
		// File doesn't exist - mark as skipped (deleted)
		slog.Info("File not found, marking as skipped", "path", m.Path)
		metrics.RecordSkipped(m.DisplayCategory())
		db.MarkDeleted(c.sqlDBs, m.Path)
		return models.ProcessResult{SourcePath: m.Path, Error: err}
	}

	slog.Info("Processing",
		"path", m.Path,
		"category", m.Category,
		"size", utils.FormatSize(m.Size))

	processor := registry.GetProcessor(&m)
	if processor == nil {
		metrics.RecordFailure(m.DisplayCategory())
		return models.ProcessResult{SourcePath: m.Path, Error: fmt.Errorf("no processor found")}
	}

	processCtx, cancel := context.WithTimeout(ctx, c.getTimeout(m))
	defer cancel()

	result := processor.Process(processCtx, &m, cfg, registry)

	// Handle processing errors
	if result.Error != nil {
		return c.handleProcessingError(m, result, cfg, metrics)
	}

	// Handle unsuccessful processing (e.g., invalid output)
	if !result.Success {
		return c.handleUnsuccessfulProcessing(m, cfg, metrics)
	}

	// Calculate new size and compare with original
	var totalNewSize int64
	for _, out := range result.Outputs {
		totalNewSize += out.Size
	}

	keepNewFiles := true
	if cfg.Common.DeleteLarger && !cfg.Common.ForceShrink && totalNewSize > m.Size {
		keepNewFiles = false
	}

	c.finalizeFileSwap(m, result, keepNewFiles)

	if keepNewFiles {
		c.finalizeSuccessfulProcessing(&m, result, originalAtime, originalMtime, cfg, metrics)
	} else {
		db.MarkShrinked(c.sqlDBs, m.Path)
		metrics.RecordSuccess(m.DisplayCategory(), m.Size, m.Size, m.ProcessingTime, int64(m.Duration))
	}

	return result
}

// handleBrokenArchive handles broken archives by moving them to the broken directory
func (c *ShrinkCmd) handleBrokenArchive(m models.ShrinkMedia, cfg *models.ProcessorConfig, metrics *ShrinkMetrics) models.ProcessResult {
	slog.Info("Broken archive detected, moving to broken directory", "path", m.Path)
	if cfg.Common.MoveBroken != "" {
		c.moveToBroken(m.Path, m.PartFiles)
	}
	db.MarkDeleted(c.sqlDBs, m.Path)
	metrics.RecordFailure(m.DisplayCategory())
	return models.ProcessResult{SourcePath: m.Path, Success: false}
}

// captureTimestamps captures the original access and modification times of a file
func (c *ShrinkCmd) captureTimestamps(path string) (time.Time, time.Time, error) {
	stat, err := os.Stat(path)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	return utils.GetAccessTime(stat), stat.ModTime(), nil
}

// handleProcessingError handles errors from processing
func (c *ShrinkCmd) handleProcessingError(m models.ShrinkMedia, result models.ProcessResult,
	cfg *models.ProcessorConfig, metrics *ShrinkMetrics,
) models.ProcessResult {
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
	metrics.RecordFailure(m.DisplayCategory())
	if cfg.Common.MoveBroken != "" {
		c.moveToBroken(m.Path, result.PartFiles)
	}
	return result
}

// handleUnsuccessfulProcessing handles processing that succeeded but produced no valid output
func (c *ShrinkCmd) handleUnsuccessfulProcessing(m models.ShrinkMedia,
	cfg *models.ProcessorConfig, metrics *ShrinkMetrics,
) models.ProcessResult {
	// Processing succeeded but produced no valid output (e.g. invalid file)
	if cfg.Common.DeleteUnplayable {
		db.MarkDeleted(c.sqlDBs, m.Path)
		os.Remove(m.Path)
	}
	metrics.RecordFailure(m.DisplayCategory())
	return models.ProcessResult{SourcePath: m.Path, Success: false}
}

// finalizeSuccessfulProcessing handles post-processing for successful operations
func (c *ShrinkCmd) finalizeSuccessfulProcessing(m *models.ShrinkMedia, result models.ProcessResult,
	originalAtime, originalMtime time.Time, cfg *models.ProcessorConfig, metrics *ShrinkMetrics,
) {
	c.updateMetadata(*m, result)
	c.preserveTimestamps(m, result, originalAtime, originalMtime)
	for _, out := range result.Outputs {
		c.moveTo(out.Path)
	}

	var totalNewSize int64
	for _, out := range result.Outputs {
		totalNewSize += out.Size
	}
	metrics.RecordSuccess(m.DisplayCategory(), m.Size, totalNewSize, m.ProcessingTime, int64(m.Duration))
}

// ============================================================================
// File Finalization
// ============================================================================

func (c *ShrinkCmd) finalizeFileSwap(m models.ShrinkMedia, result models.ProcessResult, keepNewFiles bool) {
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
				db.MarkDeleted(c.sqlDBs, m.Path)
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

func (c *ShrinkCmd) updateMetadata(m models.ShrinkMedia, result models.ProcessResult) {
	for _, out := range result.Outputs {
		// We use updateDatabase when the original is replaced by a single output
		// to preserve metadata like play_count, etc.
		// Except for archives, where we want to keep the archive record as deleted.
		if len(result.Outputs) == 1 && out.Path != m.Path && m.Category != "Archived" {
			db.UpdateMedia(c.sqlDBs, m.Path, out.Path, out.Size, m.Duration)
		} else if out.Path != m.Path {
			db.AddMediaEntry(c.sqlDBs, out.Path, out.Size, m.Duration)
		} else {
			db.MarkShrinked(c.sqlDBs, out.Path)
		}
	}
}

func (c *ShrinkCmd) preserveTimestamps(m *models.ShrinkMedia, result models.ProcessResult, originalAtime, originalMtime time.Time) {
	if len(result.Outputs) > 0 && !originalAtime.IsZero() {
		outPath := result.Outputs[0].Path
		applyTimestamps(outPath, originalAtime, originalMtime)
		// Update duration if needed
		if m.Category == "Audio" || m.Category == "Video" {
			if newDuration := c.getActualDuration(outPath); newDuration > 0 {
				m.Duration = newDuration
			}
		}
	}
}

// ============================================================================
// Utilities
// ============================================================================

func (c *ShrinkCmd) getTimeout(m models.ShrinkMedia) time.Duration {
	timeoutMult := 1.0
	if utils.HasUnreliableDuration(m.Ext) {
		timeoutMult = 2.0 // Double timeout for unreliable formats (VOB, etc)
	}

	switch m.Category {
	case "Video":
		duration := utils.GetDurationForTimeout(m.Duration, m.Size, m.Ext)
		if duration > 30 {
			return time.Duration(duration*c.VideoTimeoutMult*timeoutMult) * time.Second
		}
		return utils.ParseDurationString(c.VideoTimeout)
	case "Audio":
		duration := m.Duration
		if duration > 30 {
			return time.Duration(duration*c.AudioTimeoutMult*timeoutMult) * time.Second
		}
		return utils.ParseDurationString(c.AudioTimeout)
	case "Image":
		return utils.ParseDurationString(c.ImageTimeout)
	case "Text":
		return utils.ParseDurationString(c.TextTimeout)
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
func (c *ShrinkCmd) getActualDuration(path string) float64 {
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
