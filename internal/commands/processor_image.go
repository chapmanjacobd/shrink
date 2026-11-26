package commands

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/chapmanjacobd/shrink/internal/ffmpeg"
	"github.com/chapmanjacobd/shrink/internal/geotiff"
	"github.com/chapmanjacobd/shrink/internal/models"
	"github.com/chapmanjacobd/shrink/internal/utils"
)

// ImageProcessor handles image file processing
type ImageProcessor struct {
	BaseProcessor
}

func NewImageProcessor() *ImageProcessor {
	return &ImageProcessor{
		BaseProcessor: BaseProcessor{category: "Image", requiredTool: "magick"},
	}
}

func (p *ImageProcessor) CanProcess(m *models.ShrinkMedia) bool {
	return utils.ImageExtensionMap[m.Ext]
}

func (p *ImageProcessor) EstimateSize(m *models.ShrinkMedia, cfg *models.ProcessorConfig) models.ProcessableInfo {
	if geotiff.IsGeospatial(m.Path) {
		return estimateGeospatial(m, cfg, geotiff.IsTiledCompressed(m.Path), utils.GetCommandPath("gdal_translate") != "")
	}

	if cfg.Image.MaxImageBytes > 0 && m.Size > cfg.Image.MaxImageBytes {
		slog.Info("Skipping oversized image",
			"path", m.Path,
			"size", utils.FormatSize(m.Size),
			"limit", utils.FormatSize(cfg.Image.MaxImageBytes))
		return models.ProcessableInfo{IsProcessable: false}
	}

	if cfg.Image.MaxImagePixels > 0 && m.Width > 0 && m.Height > 0 {
		pixels := int64(m.Width) * int64(m.Height)
		if pixels > cfg.Image.MaxImagePixels {
			slog.Info("Skipping image with too many pixels",
				"path", m.Path,
				"pixels", pixels,
				"limit", cfg.Image.MaxImagePixels)
			return models.ProcessableInfo{IsProcessable: false}
		}
	}

	return models.ProcessableInfo{
		FutureSize:     cfg.Image.TargetImageSize,
		ProcessingTime: int(cfg.Image.TranscodingImageTime),
		IsProcessable:  true,
	}
}

// estimateGeospatial decides how a georeferenced image should be handled.
// Recompressing to a Cloud Optimized GeoTIFF preserves the coordinate system
// and streams data through GDAL instead of building a full pixel cache.
func estimateGeospatial(m *models.ShrinkMedia, cfg *models.ProcessorConfig, alreadyOptimized, hasGDAL bool) models.ProcessableInfo {
	switch cfg.Image.GeotiffMode {
	case "skip":
		slog.Info("Skipping GeoTIFF", "path", m.Path, "reason", "geotiff-mode=skip")
		return models.ProcessableInfo{IsProcessable: false}
	case "avif":
		if cfg.Image.MaxImageBytes > 0 && m.Size > cfg.Image.MaxImageBytes {
			slog.Info("Skipping oversized GeoTIFF",
				"path", m.Path,
				"size", utils.FormatSize(m.Size),
				"limit", utils.FormatSize(cfg.Image.MaxImageBytes))
			return models.ProcessableInfo{IsProcessable: false}
		}
		return models.ProcessableInfo{
			FutureSize:     cfg.Image.TargetImageSize,
			ProcessingTime: int(cfg.Image.TranscodingImageTime),
			IsProcessable:  true,
		}
	}

	if !hasGDAL {
		slog.Info("Skipping GeoTIFF", "path", m.Path, "reason", "gdal_translate not installed")
		return models.ProcessableInfo{IsProcessable: false}
	}
	if alreadyOptimized {
		slog.Debug("Skipping GeoTIFF", "path", m.Path, "reason", "already tiled and compressed")
		return models.ProcessableInfo{IsProcessable: false}
	}
	return models.ProcessableInfo{
		FutureSize:     m.Size / 2,
		ProcessingTime: int(cfg.Image.TranscodingImageTime),
		IsProcessable:  true,
	}
}

func (p *ImageProcessor) Process(ctx context.Context, m *models.ShrinkMedia, cfg *models.ProcessorConfig, registry models.ProcessorRegistry) models.ProcessResult {
	return p.processImage(ctx, m, cfg)
}

func (p *ImageProcessor) processImage(ctx context.Context, m *models.ShrinkMedia, cfg *models.ProcessorConfig) models.ProcessResult {
	if geotiff.IsGeospatial(m.Path) {
		switch cfg.Image.GeotiffMode {
		case "skip":
			return keepOriginalImage(m)
		case "cog":
			return p.processGeoTIFF(ctx, m, cfg)
		}
		// "avif" falls through to ImageMagick below.
	}

	imCmd := getImageMagickCommand()
	if imCmd == "" {
		return models.ProcessResult{SourcePath: m.Path, Error: fmt.Errorf("ImageMagick not installed")}
	}

	// Get dimensions if missing
	if m.Width == 0 || m.Height == 0 {
		width, height, err := ffmpeg.GetImageDimensionsContext(ctx, m.Path)
		if err == nil {
			m.Width = width
			m.Height = height
		}
	}

	if cfg.Image.MaxImagePixels > 0 && m.Width > 0 && m.Height > 0 {
		pixels := int64(m.Width) * int64(m.Height)
		if pixels > cfg.Image.MaxImagePixels {
			slog.Info("Skipping image with too many pixels",
				"path", m.Path,
				"pixels", pixels,
				"limit", cfg.Image.MaxImagePixels)
			return keepOriginalImage(m)
		}
	}

	outputPath := strings.TrimSuffix(m.Path, filepath.Ext(m.Path)) + ".avif"

	args := imageMagickLimitArgs(cfg)
	args = append(args, m.Path)

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

	// Setup systemd-run wrapper if configured (Linux only)
	systemdCfg := utils.SystemdRunConfig{
		MemoryLimit:   cfg.Common.MemoryLimit,
		MemorySwapMax: cfg.Common.MemorySwapMax,
		UseJournald:   cfg.Common.UseJournald,
		Enabled:       !cfg.Common.DisableSystemd,
	}

	output, err := utils.RunCommandWithSystemd(ctx, imCmd, args, systemdCfg)
	if err != nil {
		// Clean up on failure
		_ = os.Remove(outputPath)

		// Categorize ImageMagick errors
		errorLog := strings.Split(string(output), "\n")
		isUnsupported := isImageMagickUnsupportedError(errorLog)
		isFileError := isImageMagickFileError(errorLog)
		isEnvError := isImageMagickEnvironmentError(errorLog)
		isResourceLimit := isImageMagickResourceLimitError(errorLog)

		if isResourceLimit {
			slog.Info("Image exceeds ImageMagick resource limits, keeping original", "path", m.Path)
			return keepOriginalImage(m)
		} else if isEnvError {
			return models.ProcessResult{SourcePath: m.Path, Error: fmt.Errorf("ImageMagick environment error: %w", err), Output: string(output), StopAll: true}
		} else if isUnsupported {
			slog.Info("Unsupported image format, keeping original", "path", m.Path)
			return models.ProcessResult{SourcePath: m.Path, Success: true, Outputs: []models.ProcessOutputFile{{Path: m.Path, Size: m.Size}}}
		} else if isFileError {
			return models.ProcessResult{SourcePath: m.Path, Error: err, Output: string(output)}
		}

		return models.ProcessResult{SourcePath: m.Path, Error: err, Output: string(output)}
	}

	outputStats, err := os.Stat(outputPath)
	if err != nil || outputStats.Size() == 0 {
		_ = os.Remove(outputPath)
		return models.ProcessResult{SourcePath: m.Path, Error: fmt.Errorf("output file empty or missing")}
	}

	// Small delay to ensure file is fully written and flushed
	time.Sleep(100 * time.Millisecond)

	// Verify AVIF file is valid using ffprobe
	if strings.HasSuffix(outputPath, ".avif") {
		width, height, err := ffmpeg.GetImageDimensionsContext(ctx, outputPath)
		if err != nil {
			_ = os.Remove(outputPath)
			return models.ProcessResult{SourcePath: m.Path, Error: fmt.Errorf("AVIF validation failed: %w", err)}
		}
		if width <= 1 || height <= 1 {
			_ = os.Remove(outputPath)
			return models.ProcessResult{SourcePath: m.Path, Error: fmt.Errorf("AVIF file has invalid dimensions: %dx%d", width, height)}
		}
		// Allow buffer percentage plus 10px tolerance for non-resize conversions
		maxWidth := float64(cfg.Image.MaxImageWidth)*(1+cfg.Common.MaxWidthBuffer) + 10
		maxHeight := float64(cfg.Image.MaxImageHeight)*(1+cfg.Common.MaxHeightBuffer) + 10
		if float64(width) > maxWidth || float64(height) > maxHeight {
			_ = os.Remove(outputPath)
			return models.ProcessResult{SourcePath: m.Path, Error: fmt.Errorf("AVIF file exceeds max dimensions: %dx%d > %.0fx%.0f", width, height, maxWidth, maxHeight)}
		}
	}

	return models.ProcessResult{
		SourcePath: m.Path,
		Outputs:    []models.ProcessOutputFile{{Path: outputPath, Size: outputStats.Size()}},
		Success:    true,
	}
}

// imageMagickLimitArgs bounds ImageMagick's pixel cache. Without these limits
// ImageMagick defaults to an unlimited disk cache and can fill /tmp (which is
// often tmpfs) with a single large image.
func imageMagickLimitArgs(cfg *models.ProcessorConfig) []string {
	var args []string
	if cfg.Image.ImageCacheLimit > 0 {
		limit := strconv.FormatInt(cfg.Image.ImageCacheLimit, 10)
		args = append(args,
			"-limit", "memory", limit,
			"-limit", "map", limit,
			"-limit", "disk", limit,
		)
	}
	return args
}

// processGeoTIFF recompresses a georeferenced raster to a Cloud Optimized
// GeoTIFF. The source path is kept so sidecar files and database records stay
// valid.
func (p *ImageProcessor) processGeoTIFF(ctx context.Context, m *models.ShrinkMedia, cfg *models.ProcessorConfig) models.ProcessResult {
	gdal := utils.GetCommandPath("gdal_translate")
	if gdal == "" {
		slog.Info("GDAL not installed, keeping GeoTIFF", "path", m.Path)
		return keepOriginalImage(m)
	}

	originalInfo, err := os.Stat(m.Path)
	if err != nil {
		return models.ProcessResult{SourcePath: m.Path, Error: err}
	}

	output, err := os.CreateTemp(filepath.Dir(m.Path), ".shrink-cog-*.tif")
	if err != nil {
		return models.ProcessResult{SourcePath: m.Path, Error: err}
	}
	tempPath := output.Name()
	_ = output.Close()
	// gdal_translate creates the output itself.
	_ = os.Remove(tempPath)
	defer func() { _ = os.Remove(tempPath) }()

	args := []string{
		"-of", "COG",
		"-co", "COMPRESS=ZSTD",
		"-co", "PREDICTOR=2",
		"-co", "BIGTIFF=IF_SAFER",
		"-co", "OVERVIEWS=AUTO",
		"-co", "RESAMPLING=AVERAGE",
		"-co", "OVERVIEW_RESAMPLING=AVERAGE",
		m.Path, tempPath,
	}

	systemdCfg := utils.SystemdRunConfig{
		MemoryLimit:   cfg.Common.MemoryLimit,
		MemorySwapMax: cfg.Common.MemorySwapMax,
		UseJournald:   cfg.Common.UseJournald,
		Enabled:       !cfg.Common.DisableSystemd,
	}

	outputBytes, err := utils.RunCommandWithSystemd(ctx, gdal, args, systemdCfg)
	if err != nil {
		return models.ProcessResult{
			SourcePath: m.Path,
			Error:      fmt.Errorf("gdal_translate failed: %w", err),
			Output:     string(outputBytes),
		}
	}

	outputInfo, err := os.Stat(tempPath)
	if err != nil || outputInfo.Size() == 0 {
		return models.ProcessResult{SourcePath: m.Path, Error: fmt.Errorf("COG output file empty or missing")}
	}

	if !cfg.Common.DeleteLarger || cfg.Common.ForceShrink || outputInfo.Size() <= m.Size {
		if err := replaceFile(tempPath, m.Path, originalInfo.Mode()); err != nil {
			return models.ProcessResult{SourcePath: m.Path, Error: fmt.Errorf("replace GeoTIFF: %w", err)}
		}
		return models.ProcessResult{
			SourcePath: m.Path,
			Outputs:    []models.ProcessOutputFile{{Path: m.Path, Size: outputInfo.Size()}},
			Success:    true,
		}
	}

	slog.Info("COG output is not smaller, keeping original",
		"path", m.Path,
		"original", utils.FormatSize(m.Size),
		"cog", utils.FormatSize(outputInfo.Size()))
	return keepOriginalImage(m)
}

// keepOriginalImage reports the source file as the output. The orchestrator
// treats an unchanged size as a skip rather than a successful shrink.
func keepOriginalImage(m *models.ShrinkMedia) models.ProcessResult {
	return models.ProcessResult{
		SourcePath: m.Path,
		Outputs:    []models.ProcessOutputFile{{Path: m.Path, Size: m.Size}},
		Success:    true,
	}
}

// replaceFile atomically replaces dst with src on Unix and uses a backup swap
// on Windows, where os.Rename cannot overwrite an existing file.
func replaceFile(src, dst string, mode os.FileMode) error {
	_ = os.Chmod(src, mode.Perm())
	if runtime.GOOS != "windows" {
		return os.Rename(src, dst)
	}

	backup := dst + ".shrink-backup"
	_ = os.Remove(backup)
	if err := os.Rename(dst, backup); err != nil {
		return err
	}
	if err := os.Rename(src, dst); err != nil {
		if restoreErr := os.Rename(backup, dst); restoreErr != nil {
			slog.Error("Failed to restore original after COG replacement", "path", dst, "backup", backup, "error", restoreErr)
		}
		return err
	}
	return os.Remove(backup)
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

// isImageMagickResourceLimitError checks if ImageMagick rejected an image
// because it exceeded a configured resource limit. That is file-specific: the
// original should be kept and the rest of the queue should continue.
func isImageMagickResourceLimitError(errorLog []string) bool {
	resourcePatterns := []string{
		"cache resources exhausted",
		"image width or height exceeds limit",
		"image area exceeds limit",
	}
	for _, line := range errorLog {
		lineLower := strings.ToLower(line)
		for _, pattern := range resourcePatterns {
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

// getImageMagickCommand returns the appropriate ImageMagick command path
// ("magick" or, on platforms where it is not ambiguous, "convert").
// Returns empty string if neither is available.
func getImageMagickCommand() string {
	if path := utils.GetCommandPath("magick"); path != "" {
		return path
	}
	// On Windows, convert.exe is the system FAT-to-NTFS utility, not
	// ImageMagick's legacy command name.
	if runtime.GOOS != "windows" {
		if path := utils.GetCommandPath("convert"); path != "" {
			return path
		}
	}
	return ""
}
