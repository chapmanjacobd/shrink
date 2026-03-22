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

func (c *ShrinkCmd) analyzeMedia(media []models.ShrinkMedia, cfg *models.ProcessorConfig,
	registry *MediaRegistry, metrics *ShrinkMetrics,
) []models.ShrinkMedia {
	var toShrink []models.ShrinkMedia

	for i := range media {
		m := &media[i]
		processor := registry.GetProcessor(m)
		if processor == nil {
			metrics.RecordSkipped("Unknown")
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
					}
					metrics.RecordSkipped(m.DisplayCategory())
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

func (c *ShrinkCmd) sortByEfficiency(media []models.ShrinkMedia) {
	sort.Slice(media, func(i, j int) bool {
		timeI := max(media[i].ProcessingTime, 1)
		timeJ := max(media[j].ProcessingTime, 1)
		ratioI := float64(media[i].Savings) / float64(timeI)
		ratioJ := float64(media[j].Savings) / float64(timeJ)
		return ratioI > ratioJ
	})
}

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

func (c *ShrinkCmd) processSingle(ctx context.Context, m models.ShrinkMedia, registry *MediaRegistry,
	cfg *models.ProcessorConfig, metrics *ShrinkMetrics,
) models.ProcessResult {
	// Handle broken archives - move to --move-broken without processing
	if m.IsBroken {
		slog.Info("Broken archive detected, moving to broken directory", "path", m.Path)
		if cfg.Common.MoveBroken != "" {
			c.moveToBroken(m.Path, m.PartFiles)
		}
		db.MarkDeleted(c.sqlDBs, m.Path)
		metrics.RecordFailure(m.DisplayCategory())
		return models.ProcessResult{SourcePath: m.Path, Success: false}
	}

	// Capture original timestamps before processing
	var originalAtime, originalMtime time.Time
	if stat, err := os.Stat(m.Path); err != nil {
		// File doesn't exist - mark as skipped (deleted)
		slog.Warn("File not found, marking as skipped", "path", m.Path)
		metrics.RecordSkipped(m.DisplayCategory())
		db.MarkDeleted(c.sqlDBs, m.Path)
		return models.ProcessResult{SourcePath: m.Path, Error: err}
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
		metrics.RecordFailure(m.DisplayCategory())
		return models.ProcessResult{SourcePath: m.Path, Error: fmt.Errorf("no processor found")}
	}

	processCtx, cancel := context.WithTimeout(ctx, c.getTimeout(m))
	defer cancel()

	result := processor.Process(processCtx, &m, cfg, registry)

	if result.Error != nil {
		slog.Error("Processing failed", "path", m.Path, "error", result.Error)
		metrics.RecordFailure(m.DisplayCategory())
		if cfg.Common.MoveBroken != "" {
			c.moveToBroken(m.Path, result.PartFiles)
		}
		return result
	}

	if !result.Success {
		// Processing succeeded but produced no valid output (e.g. invalid file)
		if cfg.Common.DeleteUnplayable {
			db.MarkDeleted(c.sqlDBs, m.Path)
			os.Remove(m.Path)
		}
		metrics.RecordFailure(m.DisplayCategory())
		return result
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
		c.updateMetadata(m, result)
		c.preserveTimestamps(&m, result, originalAtime, originalMtime)
		for _, out := range result.Outputs {
			c.moveTo(out.Path)
		}
		metrics.RecordSuccess(m.DisplayCategory(), m.Size, totalNewSize, m.ProcessingTime, int64(m.Duration))
	} else {
		db.MarkShrinked(c.sqlDBs, m.Path)
		metrics.RecordSuccess(m.DisplayCategory(), m.Size, m.Size, m.ProcessingTime, int64(m.Duration))
	}

	return result
}

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

func (c *ShrinkCmd) getTimeout(m models.ShrinkMedia) time.Duration {
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
