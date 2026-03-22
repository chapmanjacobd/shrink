package ffmpeg

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/chapmanjacobd/shrink/internal/models"
	"github.com/chapmanjacobd/shrink/internal/utils"
)

// validateTranscode validates the transcoded output
func (p *FFmpegProcessor) validateTranscode(m models.ShrinkMedia, outputPath string, originalProbe *FFProbeResult) models.ProcessResult {
	// Check if output is a split file pattern (contains %03d)
	isSplit := strings.Contains(outputPath, "%03d")

	if !isSplit {
		// Single file output - original logic
		if _, err := os.Stat(outputPath); os.IsNotExist(err) {
			return models.ProcessResult{SourcePath: m.Path, Success: false}
		}

		outputStats, err := os.Stat(outputPath)
		if err != nil {
			return models.ProcessResult{SourcePath: m.Path, Success: false}
		}

		deleteTranscode := false
		// Check for invalid transcode
		if outputStats.Size() == 0 {
			deleteTranscode = true
		} else {
			// Validate duration and dimensions
			transcodeProbe, err := ProbeMedia(outputPath)
			if err != nil {
				deleteTranscode = true
			} else if len(transcodeProbe.Streams) == 0 || transcodeProbe.Duration == 0 {
				deleteTranscode = true
			} else {
				// Check for invalid dimensions (e.g., 1x1 from overwrite race conditions)
				// Only check non-album-art video streams
				for _, stream := range transcodeProbe.VideoStreams {
					// Skip album art (0x0 dimensions in probe)
					if stream.Width > 0 && stream.Height > 0 {
						if stream.Width <= 1 || stream.Height <= 1 {
							deleteTranscode = true
							slog.Debug("Invalid video dimensions", "path", outputPath, "width", stream.Width, "height", stream.Height)
							break
						}
					}
				}
				// Check duration matches original
				// Skip this check for formats known to have unreliable duration metadata (DVD, etc)
				if !utils.HasUnreliableDuration(m.Ext) {
					// Ensure transcode duration is at least 90% of original
					// (Avoids deleting transcodes that were cut short while allowing some leeway)
					if transcodeProbe.Duration < originalProbe.Duration*0.9 {
						deleteTranscode = true
					}
				}
			}
		}

		if deleteTranscode {
			os.Remove(outputPath)
			return models.ProcessResult{SourcePath: m.Path, Success: false}
		}

		return models.ProcessResult{
			SourcePath: m.Path,
			Outputs:    []models.ProcessOutputFile{{Path: outputPath, Size: outputStats.Size()}},
			Success:    true,
		}
	}

	// Split file output - find all generated files
	pattern := strings.Replace(outputPath, "%03d", "*", 1)
	matches, err := filepath.Glob(pattern)
	if err != nil || len(matches) == 0 {
		return models.ProcessResult{SourcePath: m.Path, Error: fmt.Errorf("no split files found")}
	}

	// Validate each split file
	var totalNewSize int64
	var hasInvalidFile bool
	var validFiles []models.ProcessOutputFile

	for _, match := range matches {
		stats, err := os.Stat(match)
		if err != nil || stats.Size() == 0 {
			hasInvalidFile = true
			break
		}
		// Validate dimensions for video split files
		probe, err := ProbeMedia(match)
		if err != nil || len(probe.Streams) == 0 {
			hasInvalidFile = true
			break
		}
		// Check for invalid dimensions (e.g., 1x1 from overwrite race conditions)
		// Only check non-album-art video streams
		for _, stream := range probe.VideoStreams {
			if stream.Width > 0 && stream.Height > 0 {
				if stream.Width <= 1 || stream.Height <= 1 {
					hasInvalidFile = true
					slog.Debug("Invalid video dimensions in split file", "path", match, "width", stream.Width, "height", stream.Height)
					break
				}
			}
		}
		if hasInvalidFile {
			break
		}
		totalNewSize += stats.Size()
		validFiles = append(validFiles, models.ProcessOutputFile{Path: match, Size: stats.Size()})
	}

	if hasInvalidFile {
		// Clean up all split files
		for _, match := range matches {
			os.Remove(match)
		}
		return models.ProcessResult{SourcePath: m.Path, Error: fmt.Errorf("invalid split file")}
	}

	return models.ProcessResult{
		SourcePath: m.Path,
		Outputs:    validFiles,
		Success:    true,
	}
}

// isUnsupportedError checks if FFmpeg error is due to unsupported codec/format
func (p *FFmpegProcessor) isUnsupportedError(errorLog []string) bool {
	unsupportedPatterns := []string{
		"not implemented", "unsupported", "no codec", "unknown codec",
		"encoder not found", "decoder not found", "incompatible", "unknown encoder",
	}
	for _, line := range errorLog {
		lineLower := strings.ToLower(line)
		for _, pattern := range unsupportedPatterns {
			if strings.Contains(lineLower, pattern) {
				return true
			}
		}
	}
	return false
}

// isFileError checks if FFmpeg error is file-specific (corrupt, missing, etc.)
func (p *FFmpegProcessor) isFileError(errorLog []string) bool {
	fileErrorPatterns := []string{
		"invalid data", "corrupt", "truncated", "missing", "cannot open",
		"no such file", "permission denied", "input/output error",
	}
	for _, line := range errorLog {
		lineLower := strings.ToLower(line)
		for _, pattern := range fileErrorPatterns {
			if strings.Contains(lineLower, pattern) {
				return true
			}
		}
	}
	return false
}

// isEnvironmentError checks if FFmpeg error is environment-related (OOM, signal, etc.)
func (p *FFmpegProcessor) isEnvironmentError(errorLog []string) bool {
	envErrorPatterns := []string{
		"killed", "oom", "out of memory", "signal", "segmentation fault",
		"illegal instruction", "bus error", "aborted",
	}
	for _, line := range errorLog {
		lineLower := strings.ToLower(line)
		for _, pattern := range envErrorPatterns {
			if strings.Contains(lineLower, pattern) {
				return true
			}
		}
	}
	return false
}
