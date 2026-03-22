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

	"github.com/chapmanjacobd/shrink/internal/ffmpeg"
	"github.com/chapmanjacobd/shrink/internal/models"
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

func (p *ImageProcessor) CanProcess(m *models.ShrinkMedia) bool {
	return (strings.HasPrefix(m.MediaType, "image/") || strings.Contains(m.MediaType, " image")) ||
		(shouldConvertToAVIF(m.Ext) && m.Duration == 0)
}

func (p *ImageProcessor) EstimateSize(m *models.ShrinkMedia, cfg *models.ProcessorConfig) (int64, int) {
	return cfg.Image.TargetImageSize, int(cfg.Image.TranscodingImageTime)
}

func (p *ImageProcessor) Process(ctx context.Context, m *models.ShrinkMedia, cfg *models.ProcessorConfig, registry models.ProcessorRegistry) models.ProcessResult {
	return p.processImage(ctx, m, cfg)
}

func (p *ImageProcessor) processImage(ctx context.Context, m *models.ShrinkMedia, cfg *models.ProcessorConfig) models.ProcessResult {
	if !utils.CommandExists("magick") {
		return models.ProcessResult{SourcePath: m.Path, Error: fmt.Errorf("ImageMagick not installed")}
	}

	// Get dimensions if missing
	if m.Width == 0 || m.Height == 0 {
		width, height, err := ffmpeg.GetImageDimensions(m.Path)
		if err == nil {
			m.Width = width
			m.Height = height
		}
	}

	outputPath := strings.TrimSuffix(m.Path, filepath.Ext(m.Path)) + ".avif"

	args := []string{m.Path}

	// Only resize if image exceeds limit + buffer
	shouldResize := false
	if m.Width > 0 && m.Height > 0 {
		if float64(m.Width) > float64(cfg.Image.MaxImageWidth)*(1+cfg.Common.MaxWidthBuffer) ||
			float64(m.Height) > float64(cfg.Image.MaxImageHeight)*(1+cfg.Common.MaxHeightBuffer) {
			shouldResize = true
		}
	} else {
		// If dimensions unknown, default to ImageMagick's internal shrink-only check
		shouldResize = true
	}

	if shouldResize {
		args = append(args, "-resize", fmt.Sprintf("%dx%d>", cfg.Image.MaxImageWidth, cfg.Image.MaxImageHeight))
	}

	args = append(args, outputPath)

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
			return models.ProcessResult{SourcePath: m.Path, Error: fmt.Errorf("ImageMagick environment error: %w", err)}
		} else if isUnsupported {
			os.Remove(outputPath)
			slog.Info("Unsupported image format, keeping original", "path", m.Path)
			return models.ProcessResult{SourcePath: m.Path, Success: true, Outputs: []models.ProcessOutputFile{{Path: m.Path, Size: m.Size}}}
		} else if isFileError {
			return models.ProcessResult{SourcePath: m.Path, Error: err}
		}

		return models.ProcessResult{SourcePath: m.Path, Error: err}
	}

	outputStats, err := os.Stat(outputPath)
	if err != nil || outputStats.Size() == 0 {
		os.Remove(outputPath)
		return models.ProcessResult{SourcePath: m.Path, Error: fmt.Errorf("output file empty or missing")}
	}

	// Small delay to ensure file is fully written and flushed
	time.Sleep(100 * time.Millisecond)

	// Verify AVIF file is valid using ffprobe
	if strings.HasSuffix(outputPath, ".avif") {
		width, height, err := ffmpeg.GetImageDimensions(outputPath)
		if err != nil {
			os.Remove(outputPath)
			return models.ProcessResult{SourcePath: m.Path, Error: fmt.Errorf("AVIF validation failed: %w", err)}
		}
		if width <= 1 || height <= 1 {
			os.Remove(outputPath)
			return models.ProcessResult{SourcePath: m.Path, Error: fmt.Errorf("AVIF file has invalid dimensions: %dx%d", width, height)}
		}
		if width > cfg.Image.MaxImageWidth || height > cfg.Image.MaxImageHeight {
			os.Remove(outputPath)
			return models.ProcessResult{SourcePath: m.Path, Error: fmt.Errorf("AVIF file exceeds max dimensions: %dx%d > %dx%d", width, height, cfg.Image.MaxImageWidth, cfg.Image.MaxImageHeight)}
		}
	}

	return models.ProcessResult{
		SourcePath: m.Path,
		Outputs:    []models.ProcessOutputFile{{Path: outputPath, Size: outputStats.Size()}},
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
