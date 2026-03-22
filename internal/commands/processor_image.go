package commands

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/chapmanjacobd/shrink/internal/utils"
)

// ImageProcessor handles image file processing
type ImageProcessor struct {
	BaseProcessor
}

func NewImageProcessor() *ImageProcessor {
	return &ImageProcessor{
		BaseProcessor: BaseProcessor{category: "Image"},
	}
}

func (p *ImageProcessor) CanProcess(m *ShrinkMedia) bool {
	filetype := strings.ToLower(m.MediaType)
	return (strings.HasPrefix(filetype, "image/") || strings.Contains(filetype, " image")) ||
		(shouldConvertToAVIF(m.Ext) && m.Duration == 0)
}

func (p *ImageProcessor) EstimateSize(m *ShrinkMedia, cfg *ProcessorConfig) (int64, int) {
	return cfg.TargetImageSize, int(cfg.TranscodingImageTime)
}

func (p *ImageProcessor) Process(ctx context.Context, m *ShrinkMedia, cfg *ProcessorConfig) ProcessResult {
	return p.processImage(ctx, m, cfg)
}

func (p *ImageProcessor) processImage(ctx context.Context, m *ShrinkMedia, cfg *ProcessorConfig) ProcessResult {
	if !utils.CommandExists("magick") {
		return ProcessResult{SourcePath: m.Path, Error: fmt.Errorf("ImageMagick not installed")}
	}

	outputPath := strings.TrimSuffix(m.Path, filepath.Ext(m.Path)) + ".avif"

	args := []string{
		m.Path,
		"-resize", fmt.Sprintf("%dx%d>", cfg.MaxImageWidth, cfg.MaxImageHeight),
		outputPath,
	}

	cmd := exec.CommandContext(ctx, "magick", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		// Check for timeout
		if ctx.Err() == context.DeadlineExceeded {
			slog.Error("ImageMagick timed out", "path", m.Path, "error", err, "output", string(output))
		} else {
			slog.Error("ImageMagick error", "output", string(output), "path", m.Path)
		}
		// Categorize ImageMagick errors
		errorLog := strings.Split(string(output), "\n")
		isUnsupported := isImageMagickUnsupportedError(errorLog)
		isFileError := isImageMagickFileError(errorLog)
		isEnvError := isImageMagickEnvironmentError(errorLog)

		if isEnvError {
			return ProcessResult{SourcePath: m.Path, Error: fmt.Errorf("ImageMagick environment error: %w", err)}
		} else if isUnsupported {
			os.Remove(outputPath)
			slog.Info("Unsupported image format, keeping original", "path", m.Path)
			return ProcessResult{SourcePath: m.Path, Success: true, Outputs: []ProcessOutputFile{{Path: m.Path, Size: m.Size}}}
		} else if isFileError {
			return ProcessResult{SourcePath: m.Path, Error: err}
		}

		return ProcessResult{SourcePath: m.Path, Error: err}
	}

	outputStats, err := os.Stat(outputPath)
	if err != nil || outputStats.Size() == 0 {
		os.Remove(outputPath)
		return ProcessResult{SourcePath: m.Path, Error: fmt.Errorf("output file empty or missing")}
	}

	// Small delay to ensure file is fully written and flushed
	time.Sleep(100 * time.Millisecond)

	// Verify AVIF file is valid using ffprobe
	if strings.HasSuffix(outputPath, ".avif") {
		width, height, err := getImageDimensions(outputPath)
		if err != nil {
			os.Remove(outputPath)
			return ProcessResult{SourcePath: m.Path, Error: fmt.Errorf("AVIF validation failed: %w", err)}
		}
		if width <= 1 || height <= 1 {
			os.Remove(outputPath)
			return ProcessResult{SourcePath: m.Path, Error: fmt.Errorf("AVIF file has invalid dimensions: %dx%d", width, height)}
		}
		if width > cfg.MaxImageWidth || height > cfg.MaxImageHeight {
			os.Remove(outputPath)
			return ProcessResult{SourcePath: m.Path, Error: fmt.Errorf("AVIF file exceeds max dimensions: %dx%d > %dx%d", width, height, cfg.MaxImageWidth, cfg.MaxImageHeight)}
		}
	}

	return ProcessResult{
		SourcePath: m.Path,
		Outputs:    []ProcessOutputFile{{Path: outputPath, Size: outputStats.Size()}},
		Success:    true,
	}
}

// isImageMagickUnsupportedError checks if ImageMagick error is due to unsupported format
func isImageMagickUnsupportedError(errorLog []string) bool {
	unsupportedPatterns := []string{
		"not implemented", "unsupported", "no decode delegate", "no encode delegate",
		"unknown format", "invalid codec", "unrecognized image format",
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

// isImageMagickFileError checks if ImageMagick error is file-specific
func isImageMagickFileError(errorLog []string) bool {
	fileErrorPatterns := []string{
		"no such file", "not found", "permission denied", "corrupt image",
		"truncated image", "invalid image", "unable to open", "input/output error",
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

// isImageMagickEnvironmentError checks if ImageMagick error is environment-related
func isImageMagickEnvironmentError(errorLog []string) bool {
	envErrorPatterns := []string{
		"killed", "oom", "out of memory", "signal", "segmentation fault",
		"illegal instruction", "bus error", "aborted", "cache resources exhausted",
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
