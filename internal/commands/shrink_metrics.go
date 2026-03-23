package commands

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chapmanjacobd/shrink/internal/utils"
	"golang.org/x/term"
)

// ProgressLogHandler is a custom slog.Handler that coordinates logs with the progress bar.
// It clears the progress bar before writing a log message.
type ProgressLogHandler struct {
	handler slog.Handler
	metrics *ShrinkMetrics
}

func NewProgressLogHandler(handler slog.Handler, metrics *ShrinkMetrics) *ProgressLogHandler {
	return &ProgressLogHandler{
		handler: handler,
		metrics: metrics,
	}
}

func (h *ProgressLogHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.handler.Enabled(ctx, level)
}

func (h *ProgressLogHandler) Handle(ctx context.Context, r slog.Record) error {
	if h.metrics.IsTTY() {
		h.metrics.ClearProgress()
	}
	return h.handler.Handle(ctx, r)
}

func (h *ProgressLogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &ProgressLogHandler{
		handler: h.handler.WithAttrs(attrs),
		metrics: h.metrics,
	}
}

func (h *ProgressLogHandler) WithGroup(name string) slog.Handler {
	return &ProgressLogHandler{
		handler: h.handler.WithGroup(name),
		metrics: h.metrics,
	}
}

// MediaTypeStats tracks processing statistics for a specific media type
type MediaTypeStats struct {
	Total         int
	Processed     int
	Success       int
	Failed        int
	Skipped       int
	Running       int
	TotalSize     int64
	FutureSize    int64
	TotalTime     float64 // processing time in seconds
	TotalDuration int64   // total media duration in seconds (for speed ratio)
}

// SpaceSaved returns bytes saved
func (s *MediaTypeStats) SpaceSaved() int64 {
	return s.TotalSize - s.FutureSize
}

// SpeedRatio returns the processing speed ratio (e.g., 2.5x realtime)
func (s *MediaTypeStats) SpeedRatio() float64 {
	if s.TotalTime == 0 || s.TotalDuration == 0 {
		return 0
	}
	return float64(s.TotalDuration) / s.TotalTime
}

// RunningFile tracks a file currently being processed
type RunningFile struct {
	MediaType string
	Path      string
}

// ShrinkMetrics aggregates statistics across all media types
type ShrinkMetrics struct {
	types         map[string]*MediaTypeStats
	runningFiles  []RunningFile
	mu            sync.RWMutex
	started       time.Time
	lastPrintTime time.Time
	linesPrinted  int // Track how many lines we printed for cursor repositioning
	isTTY         bool
}

// NewShrinkMetrics creates a new metrics tracker
func NewShrinkMetrics() *ShrinkMetrics {
	return &ShrinkMetrics{
		started: time.Now(),
		types:   make(map[string]*MediaTypeStats),
		isTTY:   term.IsTerminal(int(os.Stdout.Fd())),
	}
}

// IsTTY returns whether the output is a TTY
func (m *ShrinkMetrics) IsTTY() bool {
	return m.isTTY
}

// RecordStarted records that a media item is being processed
func (m *ShrinkMetrics) RecordStarted(mediaType string, path string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	stats := m.getOrCreateType(mediaType)
	stats.Total++
}

// RecordSuccess records a successful processing
func (m *ShrinkMetrics) RecordSuccess(mediaType string, size, futureSize int64, processingTime float64, duration int64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	stats := m.getOrCreateType(mediaType)
	stats.Processed++
	stats.Success++
	stats.TotalSize += size
	stats.FutureSize += futureSize
	stats.TotalTime += processingTime
	stats.TotalDuration += duration
}

// RecordFailure records a failed processing
func (m *ShrinkMetrics) RecordFailure(mediaType string, processingTime float64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	stats := m.getOrCreateType(mediaType)
	stats.Processed++
	stats.Failed++
	stats.TotalTime += processingTime
}

// RecordRunning records that a media item is starting to be processed
func (m *ShrinkMetrics) RecordRunning(mediaType string, path string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	stats := m.getOrCreateType(mediaType)
	stats.Running++
	m.runningFiles = append(m.runningFiles, RunningFile{
		MediaType: mediaType,
		Path:      path,
	})
}

// RecordStopped records that a media item has finished processing
func (m *ShrinkMetrics) RecordStopped(mediaType string, path string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	stats := m.getOrCreateType(mediaType)
	stats.Running--

	// Remove the file from running files
	for i, rf := range m.runningFiles {
		if rf.MediaType == mediaType && rf.Path == path {
			m.runningFiles = append(m.runningFiles[:i], m.runningFiles[i+1:]...)
			break
		}
	}
}

// RecordSkipped records a skipped media item
func (m *ShrinkMetrics) RecordSkipped(mediaType string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	stats := m.getOrCreateType(mediaType)
	stats.Skipped++
}

// getOrCreateType gets or creates stats for a media type
func (m *ShrinkMetrics) getOrCreateType(mediaType string) *MediaTypeStats {
	if stats, ok := m.types[mediaType]; ok {
		return stats
	}
	stats := &MediaTypeStats{}
	m.types[mediaType] = stats
	return stats
}

// PrintProgress prints the current progress with summary table
// Errors are printed normally via slog and will temporarily overwrite progress
// Progress is reprinted on next update cycle
func (m *ShrinkMetrics) PrintProgress() {
	if !m.isTTY {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Rate limit to avoid excessive updates (max 2 per second)
	now := time.Now()
	if now.Sub(m.lastPrintTime) < 500*time.Millisecond {
		return
	}
	m.lastPrintTime = now

	// Build the progress output
	var sb strings.Builder
	clearSeq := utils.GetClearLineSequence()

	// Calculate totals
	var totalSuccess, totalFailed, totalSkipped, totalQueued, totalRunning int
	var totalSavings int64
	var totalDuration int64
	var totalTime float64

	for _, stats := range m.types {
		totalSuccess += stats.Success
		totalFailed += stats.Failed
		totalSkipped += stats.Skipped
		totalRunning += stats.Running
		totalSavings += stats.SpaceSaved()
		totalTime += stats.TotalTime
		totalDuration += stats.TotalDuration
		totalQueued += stats.Total - stats.Processed - stats.Skipped
	}

	// Print summary table
	headers := []string{"Media Type", "Queued", "Running", "Skip", "Fail", "OK", "Saved", "Speed"}
	var rows [][]string

	// Sort media types by Queue (descending) for consistent ordering
	type mediaTypeStats struct {
		name  string
		stats *MediaTypeStats
		queue int
	}
	var sortedTypes []mediaTypeStats

	// Check if we should collapse categories (if too many entries for terminal)
	collapse := len(m.types) > (utils.GetTerminalHeight() + 8)

	if collapse {
		collapsed := make(map[string]*MediaTypeStats)
		for mt, stats := range m.types {
			category := strings.Split(mt, ":")[0]
			cStats := collapsed[category]
			if cStats == nil {
				cStats = &MediaTypeStats{}
				collapsed[category] = cStats
			}
			cStats.Total += stats.Total
			cStats.Processed += stats.Processed
			cStats.Success += stats.Success
			cStats.Failed += stats.Failed
			cStats.Skipped += stats.Skipped
			cStats.Running += stats.Running
			cStats.TotalSize += stats.TotalSize
			cStats.FutureSize += stats.FutureSize
			cStats.TotalTime += stats.TotalTime
			cStats.TotalDuration += stats.TotalDuration
		}
		for category, stats := range collapsed {
			queue := stats.Total - stats.Processed - stats.Skipped
			sortedTypes = append(sortedTypes, mediaTypeStats{name: category, stats: stats, queue: queue})
		}
	} else {
		for mediaType, stats := range m.types {
			queue := stats.Total - stats.Processed - stats.Skipped
			sortedTypes = append(sortedTypes, mediaTypeStats{name: mediaType, stats: stats, queue: queue})
		}
	}

	sort.Slice(sortedTypes, func(i, j int) bool {
		a, b := sortedTypes[i], sortedTypes[j]
		if a.queue != b.queue {
			return a.queue > b.queue
		}
		return a.name < b.name
	})

	for _, mt := range sortedTypes {
		if mt.queue == 0 && mt.stats.Running == 0 {
			continue
		}
		speed := ""
		if mt.stats.SpeedRatio() > 0 {
			speed = fmt.Sprintf("%.1fx", mt.stats.SpeedRatio())
		}
		rows = append(rows, []string{
			mt.name,
			strconv.Itoa(mt.queue),
			strconv.Itoa(mt.stats.Running),
			strconv.Itoa(mt.stats.Skipped),
			strconv.Itoa(mt.stats.Failed),
			strconv.Itoa(mt.stats.Success),
			utils.FormatSize(mt.stats.SpaceSaved()),
			speed,
		})
	}

	// Print totals
	overallSpeed := ""
	if totalTime > 0 && totalDuration > 0 {
		overallSpeed = fmt.Sprintf("%.1fx", float64(totalDuration)/totalTime)
	}
	rows = append(rows, []string{
		"TOTAL",
		strconv.Itoa(totalQueued),
		strconv.Itoa(totalRunning),
		strconv.Itoa(totalSkipped),
		strconv.Itoa(totalFailed),
		strconv.Itoa(totalSuccess),
		utils.FormatSize(totalSavings),
		overallSpeed,
	})

	tableOutput := utils.PrintTableToString(headers, rows)
	// Add clearSeq to each line of the table
	tableLines := strings.Split(strings.TrimSuffix(tableOutput, "\n"), "\n")
	for _, line := range tableLines {
		sb.WriteString(line + clearSeq + "\n")
	}

	// Calculate how many running files we can display
	// Reserve space for: table lines + running files section
	terminalHeight := utils.GetTerminalHeight()
	tableLineCount := len(tableLines)
	maxRunningLines := terminalHeight - tableLineCount - 2 // -2 for spacing

	// Show running files only if there's enough space
	if maxRunningLines > 2 && len(m.runningFiles) > 0 {
		sb.WriteString(clearSeq + "\n")
		// Current running files section
		sb.WriteString("Currently processing:" + clearSeq + "\n")

		// Determine how many files to show
		filesToShow := min(len(m.runningFiles),
			// Reserve 1 line for "...and X more" if needed
			maxRunningLines-1)

		for i := 0; i < filesToShow; i++ {
			rf := m.runningFiles[i]
			displayPath := utils.TruncateMiddle(rf.Path, utils.GetTerminalWidth()-3)
			sb.WriteString("  " + displayPath + clearSeq + "\n")
		}

		if len(m.runningFiles) > filesToShow {
			remaining := len(m.runningFiles) - filesToShow
			sb.WriteString("  ...and " + strconv.Itoa(remaining) + " more" + clearSeq + "\n")
		}
	}

	output := sb.String()
	lineCount := strings.Count(output, "\n")

	// Clear old progress area first (if any)
	if m.linesPrinted > 0 {
		fmt.Printf("\033[%dF", m.linesPrinted) // Move up to first line of old progress
		for i := 0; i < m.linesPrinted; i++ {
			fmt.Printf("%s\n", clearSeq) // Clear each line moving down
		}
		fmt.Printf("\033[%dF", m.linesPrinted) // Move back up to where we started
	}

	// Print new progress
	fmt.Print(output)

	// Track lines printed for next iteration
	m.linesPrinted = lineCount
}

// ClearProgress erases the currently printed progress block from the screen
func (m *ShrinkMetrics) ClearProgress() {
	if !m.isTTY {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.linesPrinted == 0 {
		return
	}

	// Move cursor up to the initial line of our progress display
	fmt.Printf("\033[%dF", m.linesPrinted)
	// Clear each line moving down
	clearSeq := utils.GetClearLineSequence()
	for i := 0; i < m.linesPrinted; i++ {
		fmt.Printf("%s\n", clearSeq)
	}
	// Move back up to where we started clearing
	fmt.Printf("\033[%dF", m.linesPrinted)

	m.linesPrinted = 0
}

// LogSummary logs the final metrics summary
func (m *ShrinkMetrics) LogSummary() {
	m.mu.Lock()
	duration := time.Since(m.started)

	// Calculate totals
	var totalProcessed, totalSuccess, totalFailed int
	var totalSavings int64
	var totalDuration int64
	var totalTime float64

	for _, stats := range m.types {
		totalProcessed += stats.Processed
		totalSuccess += stats.Success
		totalFailed += stats.Failed
		totalSavings += stats.SpaceSaved()
		totalTime += stats.TotalTime
		totalDuration += stats.TotalDuration
	}

	// Sort media types for consistent output
	type mediaTypeStats struct {
		name  string
		stats *MediaTypeStats
	}
	var sortedTypes []mediaTypeStats
	for mediaType, stats := range m.types {
		// Create a copy of stats to use outside the lock
		statsCopy := *stats
		sortedTypes = append(sortedTypes, mediaTypeStats{name: mediaType, stats: &statsCopy})
	}
	m.mu.Unlock()

	sort.Slice(sortedTypes, func(i, j int) bool {
		return sortedTypes[i].name < sortedTypes[j].name
	})

	// Print summary table to stdout (always visible regardless of log level)
	fmt.Println()
	fmt.Println(strings.Repeat("=", 78))
	fmt.Println("PROCESSING COMPLETE")
	fmt.Println(strings.Repeat("=", 78))

	headers := []string{"Media Type", "Success", "Failed", "Skipped", "Saved", "Speed"}
	var rows [][]string

	for _, mt := range sortedTypes {
		speed := ""
		if mt.stats.SpeedRatio() > 0 {
			speed = fmt.Sprintf("%.1fx", mt.stats.SpeedRatio())
		}
		rows = append(rows, []string{
			mt.name,
			strconv.Itoa(mt.stats.Success),
			strconv.Itoa(mt.stats.Failed),
			strconv.Itoa(mt.stats.Skipped),
			utils.FormatSize(mt.stats.SpaceSaved()),
			speed,
		})
	}

	overallSpeed := ""
	if totalTime > 0 && totalDuration > 0 {
		overallSpeed = fmt.Sprintf("%.1fx", float64(totalDuration)/totalTime)
	}
	rows = append(rows, []string{
		"TOTAL",
		strconv.Itoa(totalSuccess),
		strconv.Itoa(totalFailed),
		strconv.Itoa(totalProcessed - totalSuccess - totalFailed),
		utils.FormatSize(totalSavings),
		overallSpeed,
	})

	utils.PrintTable(headers, rows)
	fmt.Println(strings.Repeat("=", 78))
	fmt.Printf("Total duration: %s\n", duration.String())
	fmt.Println()

	// Also log for verbose mode
	// This can safely call slog.Info because we no longer hold the lock
	slog.Info("Processing complete",
		"duration", duration.String(),
		"processed", totalProcessed,
		"success", totalSuccess,
		"failed", totalFailed,
		"savings", utils.FormatSize(totalSavings))
}

// GetStats returns stats for a specific media type
func (m *ShrinkMetrics) GetStats(mediaType string) *MediaTypeStats {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.types[mediaType]
}

// GetAllStats returns all stats (read-only copy)
func (m *ShrinkMetrics) GetAllStats() map[string]*MediaTypeStats {
	m.mu.RLock()
	defer m.mu.RUnlock()

	copy := make(map[string]*MediaTypeStats, len(m.types))
	maps.Copy(copy, m.types)
	return copy
}
