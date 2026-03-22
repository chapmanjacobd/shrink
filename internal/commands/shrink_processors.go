package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/chapmanjacobd/shrink/internal/utils"
)

// ProcessOutputFile represents a new file created by a processor
type ProcessOutputFile struct {
	Path string
	Size int64
}

// ProcessResult contains the comprehensive result of processing a media file
type ProcessResult struct {
	SourcePath string              // Original file being processed
	Outputs    []ProcessOutputFile // New files created
	PartFiles  []string            // Multi-part archive part files (for cleanup)
	Success    bool                // Whether the overall operation succeeded
	Error      error               // Error if the operation failed
}

// MediaProcessor defines the interface for processing different media types
type MediaProcessor interface {
	// CanProcess returns true if this processor can handle the given media
	CanProcess(m *ShrinkMedia) bool

	// EstimateSize calculates the future file size and processing time
	EstimateSize(m *ShrinkMedia, cfg *ProcessorConfig) (futureSize int64, processingTime int)

	// Process executes the transcoding/conversion
	// Returns a single ProcessResult containing all outputs and cleanup tasks
	Process(ctx context.Context, m *ShrinkMedia, cfg *ProcessorConfig) ProcessResult

	// Category returns the type identifier for this processor
	Category() string
}

// ProcessorConfig contains configuration for media processing
type ProcessorConfig struct {
	// Bitrates
	SourceAudioBitrate int64
	SourceVideoBitrate int64
	TargetAudioBitrate int64
	TargetVideoBitrate int64
	TargetImageSize    int64

	// Savings thresholds (as decimals, e.g., 0.05 for 5%)
	MinSavingsVideo float64
	MinSavingsAudio float64
	MinSavingsImage float64

	// Processing rates
	TranscodingVideoRate float64
	TranscodingAudioRate float64
	TranscodingImageTime float64

	// FFmpeg options
	Preset          string
	CRF             string
	MaxVideoWidth   int
	MaxVideoHeight  int
	MaxImageWidth   int
	MaxImageHeight  int
	Keyframes       bool
	AudioOnly       bool
	VideoOnly       bool
	AlwaysSplit     bool
	SplitLongerThan float64
	MinSplitSegment float64
	MaxWidthBuffer  float64
	MaxHeightBuffer float64
	NoPreserveVideo bool
	IncludeTimecode bool
	VerboseFFmpeg   bool
	SkipOCR         bool
	ForceOCR        bool
	RedoOCR         bool
	NoOCR           bool

	// General
	DeleteUnplayable bool
	DeleteLarger     bool
	MoveBroken       string
	Valid            bool
	Invalid          bool
	ForceShrink      bool
}

// BaseProcessor provides common functionality for all processors
type BaseProcessor struct {
	category string
}

// Category returns the media type for this processor
func (b *BaseProcessor) Category() string {
	return b.category
}

// VideoProcessor handles video file processing
type VideoProcessor struct {
	BaseProcessor
	ffmpeg *FFmpegProcessor
}

func NewVideoProcessor(ffmpeg *FFmpegProcessor) *VideoProcessor {
	return &VideoProcessor{
		BaseProcessor: BaseProcessor{category: "Video"},
		ffmpeg:        ffmpeg,
	}
}

func (p *VideoProcessor) CanProcess(m *ShrinkMedia) bool {
	filetype := strings.ToLower(m.MediaType)
	return (strings.HasPrefix(filetype, "video/") || strings.Contains(filetype, " video")) ||
		(utils.VideoExtensionMap[m.Ext] && m.VideoCount >= 1)
}

func (p *VideoProcessor) EstimateSize(m *ShrinkMedia, cfg *ProcessorConfig) (int64, int) {
	duration := m.Duration
	if duration <= 0 {
		duration = float64(m.Size) / float64(cfg.SourceVideoBitrate) * 8
	}

	futureSize := int64(duration * float64(cfg.TargetVideoBitrate) / 8)
	processingTime := int(math.Ceil(duration / cfg.TranscodingVideoRate))

	return futureSize, processingTime
}

func (p *VideoProcessor) Process(ctx context.Context, m *ShrinkMedia, cfg *ProcessorConfig) ProcessResult {
	return p.ffmpeg.Process(ctx, m, cfg)
}

// AudioProcessor handles audio file processing
type AudioProcessor struct {
	BaseProcessor
	ffmpeg *FFmpegProcessor
}

func NewAudioProcessor(ffmpeg *FFmpegProcessor) *AudioProcessor {
	return &AudioProcessor{
		BaseProcessor: BaseProcessor{category: "Audio"},
		ffmpeg:        ffmpeg,
	}
}

func (p *AudioProcessor) CanProcess(m *ShrinkMedia) bool {
	filetype := strings.ToLower(m.MediaType)
	return (strings.HasPrefix(filetype, "audio/") || strings.Contains(filetype, " audio")) ||
		(utils.AudioExtensionMap[m.Ext] && m.VideoCount == 0)
}

func (p *AudioProcessor) EstimateSize(m *ShrinkMedia, cfg *ProcessorConfig) (int64, int) {
	duration := m.Duration
	if duration <= 0 {
		duration = float64(m.Size) / float64(cfg.SourceAudioBitrate) * 8
	}

	futureSize := int64(duration * float64(cfg.TargetAudioBitrate) / 8)
	processingTime := int(math.Ceil(duration / cfg.TranscodingAudioRate))

	return futureSize, processingTime
}

func (p *AudioProcessor) Process(ctx context.Context, m *ShrinkMedia, cfg *ProcessorConfig) ProcessResult {
	return p.ffmpeg.Process(ctx, m, cfg)
}

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

// TextProcessor handles text/ebook file processing
type TextProcessor struct {
	BaseProcessor
}

func NewTextProcessor() *TextProcessor {
	return &TextProcessor{
		BaseProcessor: BaseProcessor{category: "Text"},
	}
}

func (p *TextProcessor) CanProcess(m *ShrinkMedia) bool {
	return utils.TextExtensionMap[m.Ext]
}

func (p *TextProcessor) EstimateSize(m *ShrinkMedia, cfg *ProcessorConfig) (int64, int) {
	// Rough estimate for ebooks
	return cfg.TargetImageSize * 50, int(cfg.TranscodingImageTime * 12)
}

func (p *TextProcessor) Process(ctx context.Context, m *ShrinkMedia, cfg *ProcessorConfig) ProcessResult {
	return p.processText(ctx, m, cfg)
}

// processText handles the actual text/ebook processing
func (p *TextProcessor) processText(ctx context.Context, m *ShrinkMedia, cfg *ProcessorConfig) ProcessResult {
	if !utils.CommandExists("ebook-convert") {
		return ProcessResult{SourcePath: m.Path, Error: fmt.Errorf("calibre not installed")}
	}

	ext := strings.ToLower(filepath.Ext(m.Path))

	// Step 1: OCR for PDFs if needed
	if ext == "pdf" && utils.CommandExists("ocrmypdf") {
		ocrPath := p.runOCR(m.Path, cfg)
		if ocrPath != "" && ocrPath != m.Path {
			m.Path = ocrPath
		}
	}

	// Step 2: Convert with Calibre to folder format
	outputDir := filepath.Join(filepath.Dir(m.Path), filepath.Base(m.Path)+".OEB")
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return ProcessResult{SourcePath: m.Path, Error: err}
	}

	args := []string{
		m.Path,
		outputDir,
		"--minimum-line-height=105",
		"--unsmarten-punctuation",
	}

	// Use pdftohtml engine for PDFs with Calibre >= 7.19.0
	major, minor, _ := p.getCalibreVersion()
	if ext == "pdf" && (major > 7 || (major == 7 && minor >= 19)) {
		args = append(args, "--pdf-engine", "pdftohtml")
	}

	cmd := exec.CommandContext(ctx, "ebook-convert", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		// Check for timeout
		if ctx.Err() == context.DeadlineExceeded {
			slog.Error("Calibre timed out", "path", m.Path, "error", err, "output", string(output))
		} else {
			slog.Error("Calibre error", "output", string(output), "path", m.Path)
		}
		os.RemoveAll(outputDir)
		return ProcessResult{SourcePath: m.Path, Error: err}
	}

	if !p.folderExists(outputDir) {
		os.RemoveAll(outputDir)
		return ProcessResult{SourcePath: m.Path, Error: fmt.Errorf("calibre output folder missing")}
	}

	// Step 3: Replace CSS with optimized version
	p.replaceCSS(outputDir)

	// Step 4: Process images inside ebook (convert to AVIF)
	imageFiles := p.findImages(outputDir)
	p.processEbookImages(ctx, imageFiles, cfg)

	// Step 5: Update references in HTML files
	p.updateImageReferences(outputDir)

	// Step 6: Return result
	outputSize := utils.FolderSize(outputDir)

	return ProcessResult{
		SourcePath: m.Path,
		Outputs:    []ProcessOutputFile{{Path: outputDir, Size: outputSize}},
		Success:    true,
	}
}

// runOCR runs OCR on a PDF file using ocrmypdf
func (p *TextProcessor) runOCR(path string, cfg *ProcessorConfig) string {
	if !utils.CommandExists("ocrmypdf") {
		return ""
	}

	// Auto-detect OCR capabilities if no explicit flag is set
	// Matches Python behavior: if tesseract+gs available, default to --skip-text
	// Otherwise, skip OCR entirely
	useSkipText := cfg.SkipOCR
	useForceOCR := cfg.ForceOCR
	useRedoOCR := cfg.RedoOCR
	skipOCR := cfg.NoOCR

	if !useSkipText && !useForceOCR && !useRedoOCR && !skipOCR {
		// No explicit flag set - auto-detect
		hasTesseract := utils.CommandExists("tesseract")
		hasGS := utils.CommandExists("gs")
		if hasTesseract && hasGS {
			useSkipText = true // Default to skip-text if tools available
		} else {
			skipOCR = true // Skip OCR entirely if tools missing
		}
	}

	if skipOCR {
		slog.Debug("Skipping OCR (not requested or tools unavailable)", "path", path)
		return ""
	}

	outputPath := strings.TrimSuffix(path, ".pdf") + ".ocr.pdf"

	args := []string{
		"--optimize", "0",
		"--output-type", "pdf",
		"--fast-web-view", "999999",
	}

	// Add OCR mode flags
	if useSkipText {
		args = append(args, "--skip-text")
	} else if useForceOCR {
		args = append(args, "--force-ocr")
	} else if useRedoOCR {
		args = append(args, "--redo-ocr")
	}

	// Add language if configured
	if lang := os.Getenv("TESSERACT_LANGUAGE"); lang != "" {
		args = append(args, "--language", lang)
	}

	args = append(args, path, outputPath)

	cmd := exec.Command("ocrmypdf", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		outputStr := string(output)
		// Check if it's a "skip-text" message (not really an error)
		if strings.Contains(outputStr, "already contains text") ||
			strings.Contains(outputStr, "skipping") {
			slog.Info("Skipping OCR (PDF already has text)", "path", path)
			os.Remove(outputPath)
			return ""
		}
		slog.Warn("OCR failed", "path", path, "error", err, "output", outputStr)
		os.Remove(outputPath)
		return ""
	}

	if _, err := os.Stat(outputPath); err == nil {
		os.Remove(path)
		return outputPath
	}

	return ""
}

// getCalibreVersion returns the Calibre version as a tuple
func (p *TextProcessor) getCalibreVersion() (int, int, int) {
	cmd := exec.Command("ebook-convert", "--version")
	output, err := cmd.Output()
	if err != nil {
		return 0, 0, 0
	}

	// Parse version from output like "ebook-convert (calibre 7.19.0)"
	parts := strings.Fields(string(output))
	for i, part := range parts {
		if strings.HasPrefix(part, "(") && i+2 < len(parts) {
			version := strings.TrimSuffix(parts[i+2], ")")
			var major, minor, patch int
			fmt.Sscanf(version, "%d.%d.%d", &major, &minor, &patch)
			return major, minor, patch
		}
	}
	return 0, 0, 0
}

// replaceCSS replaces the stylesheet with an optimized version
func (p *TextProcessor) replaceCSS(outputDir string) {
	cssPath := filepath.Join(outputDir, "stylesheet.css")
	// Optimized CSS for ebooks (matching Python implementation)
	css := `.calibre, body {
  font-family: Times New Roman,serif;
  display: block;
  font-size: 1em;
  padding-left: 0;
  padding-right: 0;
  margin: 0 5pt;
}
@media (min-width: 40em) {
  .calibre, body {
    width: 38em;
    margin: 0 auto;
  }
}
.calibre1 {
  font-size: 1.25em;
  border-bottom: 0;
  border-top: 0;
  display: block;
  padding-bottom: 0;
  padding-top: 0;
  margin: 0.5em 0;
}
.calibre2, img {
  max-height:100%;
  max-width:100%;
}
.calibre3 {
  font-weight: bold;
}
.calibre4 {
  font-style: italic;
}
p > .calibre3:not(:only-of-type) {
  font-size: 1.5em;
}
.calibre5 {
  display: block;
  font-size: 2em;
  font-weight: bold;
  line-height: 1.05;
  page-break-before: always;
  margin: 0.67em 0;
}
.calibre6 {
  display: block;
  list-style-type: disc;
  margin: 1em 0;
}
.calibre7 {
  display: list-item;
}
`
	os.WriteFile(cssPath, []byte(css), 0o644)
}

// findImages finds all image files in the ebook folder
func (p *TextProcessor) findImages(dir string) []string {
	var images []string
	filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			slog.Warn("Error accessing path while finding images", "path", path, "error", err)
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if utils.ImageExtensionMap[ext] {
			images = append(images, path)
		}
		return nil
	})
	return images
}

// processEbookImages converts images to AVIF
func (p *TextProcessor) processEbookImages(ctx context.Context, images []string, cfg *ProcessorConfig) {
	for _, img := range images {
		ext := strings.ToLower(filepath.Ext(img))
		// Skip formats that shouldn't be converted to AVIF
		if !shouldConvertToAVIF(ext) {
			continue
		}

		outputPath := strings.TrimSuffix(img, ext) + ".avif"
		args := []string{
			img,
			"-resize", fmt.Sprintf("%dx%d>", cfg.MaxImageWidth, cfg.MaxImageHeight),
			outputPath,
		}

		cmd := exec.CommandContext(ctx, "magick", args...)
		if err := cmd.Run(); err != nil {
			slog.Warn("Failed to convert ebook image", "path", img, "error", err)
			continue
		}

		// Replace if smaller
		if info, err := os.Stat(outputPath); err == nil {
			if info.Size() > 0 {
				os.Remove(img)
			} else {
				os.Remove(outputPath)
			}
		}
	}
}

// shouldConvertToAVIF returns true if the extension should be converted to AVIF
func shouldConvertToAVIF(ext string) bool {
	if !utils.ImageExtensionMap[ext] {
		return false
	}
	// Skip vector formats and already-optimized formats
	skipExts := map[string]bool{
		".avif": true, // Already AVIF
		".svg":  true, // Vector format
		".svgz": true, // Compressed SVG
	}
	return !skipExts[ext]
}

// getImageDimensions uses ffprobe to get the actual width and height of an image
func getImageDimensions(path string) (int, int, error) {
	if !utils.CommandExists("ffprobe") {
		return 0, 0, fmt.Errorf("ffprobe not available")
	}

	cmd := exec.Command("ffprobe",
		"-v", "quiet",
		"-print_format", "json",
		"-show_streams",
		path)

	output, err := cmd.Output()
	if err != nil {
		return 0, 0, err
	}

	var data struct {
		Streams []struct {
			CodecType string `json:"codec_type"`
			Width     int    `json:"width"`
			Height    int    `json:"height"`
		} `json:"streams"`
	}

	if err := json.Unmarshal(output, &data); err != nil {
		return 0, 0, err
	}

	for _, stream := range data.Streams {
		if stream.CodecType == "video" && stream.Width > 0 && stream.Height > 0 {
			return stream.Width, stream.Height, nil
		}
	}

	return 0, 0, fmt.Errorf("no video stream found")
}

// updateImageReferences updates HTML files to reference new AVIF files
func (p *TextProcessor) updateImageReferences(dir string) {
	filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if ext == ".html" || ext == ".xhtml" || ext == ".htm" {
			p.updateReferencesInFile(path)
		}
		return nil
	})
}

// updateReferencesInFile updates image references in a single HTML file
func (p *TextProcessor) updateReferencesInFile(path string) {
	content, err := os.ReadFile(path)
	if err != nil {
		return
	}

	text := string(content)
	// Replace all image extensions that we convert to AVIF
	for ext := range utils.ImageExtensionMap {
		if shouldConvertToAVIF(ext) {
			text = strings.ReplaceAll(text, ext, ".avif")
		}
	}

	os.WriteFile(path, []byte(text), 0o644)
}

// folderExists checks if a folder exists
func (p *TextProcessor) folderExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// ArchiveProcessor handles archive file processing
type ArchiveProcessor struct {
	BaseProcessor
	ffmpeg        *FFmpegProcessor
	unarInstalled bool
}

func NewArchiveProcessor(ffmpeg *FFmpegProcessor) *ArchiveProcessor {
	return &ArchiveProcessor{
		BaseProcessor: BaseProcessor{category: "Archived"},
		ffmpeg:        ffmpeg,
		unarInstalled: utils.CommandExists("lsar"),
	}
}

func (p *ArchiveProcessor) CanProcess(m *ShrinkMedia) bool {
	return m.MediaType == "archive" || utils.ArchiveExtensionMap[m.Ext]
}

// ExtractAndProcess extracts archive contents and processes media recursively
func (p *ArchiveProcessor) ExtractAndProcess(ctx context.Context, m *ShrinkMedia, cfg *ProcessorConfig,
	imageProc *ImageProcessor, ffmpeg *FFmpegProcessor,
) ProcessResult {
	if !p.unarInstalled {
		return ProcessResult{SourcePath: m.Path, Error: fmt.Errorf("unar not installed")}
	}

	// Check for multi-part archives (XAD volumes)
	var partFiles []string
	if lsarOutput, err := exec.Command("lsar", "-json", m.Path).CombinedOutput(); err == nil {
		var lsarJSON struct {
			LsarProperties struct {
				XADVolumes []string `json:"XADVolumes"`
			} `json:"lsarProperties"`
		}
		if json.Unmarshal(lsarOutput, &lsarJSON) == nil && len(lsarJSON.LsarProperties.XADVolumes) > 0 {
			partFiles = lsarJSON.LsarProperties.XADVolumes
			slog.Info("Multi-part archive detected", "path", m.Path, "parts", len(partFiles))
		}
	}

	// Extract archive - use -no-directory to prevent creating nested archive-name folders
	outputDir := filepath.Join(filepath.Dir(m.Path), filepath.Base(m.Path)+".extracted")
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return ProcessResult{SourcePath: m.Path, PartFiles: partFiles, Error: err}
	}

	// Use -no-directory and -force-rename to extract files directly into outputDir without creating subfolders
	// -force-rename is needed for nested multi-part archives
	cmd := exec.CommandContext(ctx, "unar", "-no-directory", "-force-rename", "-o", outputDir, m.Path)
	output, err := cmd.CombinedOutput()
	if err != nil {
		// Check for timeout
		if ctx.Err() == context.DeadlineExceeded {
			slog.Error("unar timed out", "path", m.Path, "error", err, "output", string(output))
		} else {
			slog.Error("unar error", "path", m.Path, "error", err, "output", string(output))
		}
		os.RemoveAll(outputDir)
		return ProcessResult{SourcePath: m.Path, PartFiles: partFiles, Error: err}
	}

	// Flatten any wrapper folders that might have been created
	flattenWrapperFolders(outputDir)

	// Find and process all media recursively
	filepath.Walk(outputDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		fileSize := info.Size()

		if shouldConvertToAVIF(ext) {
			imgMedia := &ShrinkMedia{Path: path, Size: fileSize, Ext: ext, Category: "Image"}
			futureSize, _ := imageProc.EstimateSize(imgMedia, cfg)
			if ShouldShrink(imgMedia, futureSize, cfg) {
				imgCtx, imgCancel := context.WithTimeout(context.Background(), 10*time.Minute)
				res := imageProc.processImage(imgCtx, imgMedia, cfg)
				imgCancel()
				if res.Success && len(res.Outputs) > 0 {
					var totalSize int64
					for _, out := range res.Outputs {
						totalSize += out.Size
					}
					if totalSize < fileSize {
						os.Remove(path)
					} else {
						for _, out := range res.Outputs {
							os.Remove(out.Path)
						}
					}
				}
			}
		} else if utils.VideoExtensionMap[ext] || utils.AudioExtensionMap[ext] {
			category := "Video"
			processor := MediaProcessor(NewVideoProcessor(ffmpeg))
			if utils.AudioExtensionMap[ext] {
				category = "Audio"
				processor = NewAudioProcessor(ffmpeg)
			}
			media := &ShrinkMedia{Path: path, Size: fileSize, Ext: ext, Category: category}
			futureSize, _ := processor.EstimateSize(media, cfg)
			if ShouldShrink(media, futureSize, cfg) {
				// Create a new context for media processing to avoid parent timeout issues
				mediaCtx, mediaCancel := context.WithTimeout(context.Background(), 30*time.Minute)
				res := ffmpeg.Process(mediaCtx, media, cfg)
				mediaCancel()
				if res.Success && len(res.Outputs) > 0 {
					var totalSize int64
					for _, out := range res.Outputs {
						totalSize += out.Size
					}
					// Only keep if smaller
					if totalSize < fileSize {
						os.Remove(path)
					} else {
						// Delete transcode and keep original
						for _, out := range res.Outputs {
							os.Remove(out.Path)
						}
					}
				}
			}
		} else if utils.ArchiveExtensionMap[ext] {
			nestedMedia := &ShrinkMedia{Path: path, Size: info.Size(), Ext: ext, MediaType: "archive"}
			res := p.ExtractAndProcess(ctx, nestedMedia, cfg, imageProc, ffmpeg)
			if res.Error != nil {
				slog.Warn("Failed to extract nested archive", "path", path, "error", res.Error)
			}
			if res.Success {
				// Delete the nested archive file and all its part files after extraction
				os.Remove(path)
				// Also delete any multi-part archive parts
				partFiles := p.getPartFiles(path)
				for _, partFile := range partFiles {
					os.Remove(partFile)
				}
				// The extracted contents have already been processed recursively
				// and the decision to keep original or processed files was made
			}
		}
		return nil
	})

	// Delete multi-part archive files after successful extraction
	for _, partFile := range partFiles {
		if !filepath.IsAbs(partFile) {
			partFile = filepath.Join(filepath.Dir(m.Path), partFile)
		}
		os.Remove(partFile)
		slog.Debug("Deleted multi-part archive part", "path", partFile)
	}

	return ProcessResult{
		SourcePath: m.Path,
		Outputs:    []ProcessOutputFile{{Path: outputDir, Size: utils.FolderSize(outputDir)}},
		PartFiles:  partFiles,
		Success:    true,
	}
}

// EstimateSizeForArchive estimates size using compressed size and inspects archive contents
// Returns: futureSize, processingTime, hasProcessableContent, totalArchiveSize
func (p *ArchiveProcessor) EstimateSizeForArchive(m *ShrinkMedia, cfg *ProcessorConfig) (int64, int, bool, int64) {
	if !p.unarInstalled {
		return 0, 0, false, 0
	}

	// Get archive contents and check for multi-part volumes
	contents, lsarFailed := p.lsarWithStatus(m.Path)
	slog.Info("lsar returned", "path", m.Path, "count", len(contents), "lsarFailed", lsarFailed)
	
	// Check for multi-part archives and verify all parts exist
	totalArchiveSize := m.Size
	if lsarOutput, err := exec.Command("lsar", "-json", m.Path).CombinedOutput(); err == nil {
		var lsarJSON struct {
			LsarProperties struct {
				XADVolumes []string `json:"XADVolumes"`
			} `json:"lsarProperties"`
		}
		if json.Unmarshal(lsarOutput, &lsarJSON) == nil && len(lsarJSON.LsarProperties.XADVolumes) > 0 {
			// Sum up sizes of all parts
			totalArchiveSize = 0
			allPartsExist := true
			for _, partFile := range lsarJSON.LsarProperties.XADVolumes {
				if !filepath.IsAbs(partFile) {
					partFile = filepath.Join(filepath.Dir(m.Path), partFile)
				}
				if info, err := os.Stat(partFile); err == nil {
					totalArchiveSize += info.Size()
				} else {
					// Part file missing - archive is broken
					allPartsExist = false
					lsarFailed = true
				}
			}
			// If any part is missing, treat as broken archive
			if !allPartsExist {
				return 0, 0, false, 0
			}
		}
	}

	// If lsar failed (empty contents due to error), archive is broken
	if lsarFailed && len(contents) == 0 {
		return 0, 0, false, 0
	}

	if len(contents) == 0 {
		// Archive has no contents but lsar didn't fail - just no processable content
		return 0, 0, false, m.Size
	}
	var totalFutureSize int64
	var totalProcessingTime int
	hasProcessableContent := false

	for _, content := range contents {
		ext := content.Ext
		slog.Info("Checking archive content", "path", content.Path, "ext", ext, "type", content.MediaType)

		// Determine if this file is processable
		var futureSize int64
		var processingTime int
		isProcessable := false

		// Nested archives - use compressed size for estimation
		// We don't extract during estimation to avoid temp space issues
		// The actual contents will be analyzed during extraction
		if content.MediaType == "archive" {
			slog.Info("Found nested archive", "path", content.Path, "compressedSize", content.CompressedSize)
			isProcessable = true
			// Estimate based on compressed size (assume video content for simplicity)
			duration := float64(content.CompressedSize) / float64(cfg.SourceVideoBitrate) * 8
			futureSize = int64(duration * float64(cfg.TargetVideoBitrate) / 8)
			processingTime = int(math.Ceil(duration / cfg.TranscodingVideoRate))
			totalArchiveSize += content.Size
			slog.Info("Nested archive estimation", "path", content.Path, "futureSize", futureSize, "archiveFileSize", content.Size)
		}

		// Video files
		if !isProcessable && (content.MediaType == "video" || (ext != "" && utils.VideoExtensionMap[ext])) {
			isProcessable = true
			duration := content.Duration
			if duration <= 0 {
				// Estimate from compressed size (smaller = lower quality source)
				duration = float64(content.CompressedSize) / float64(cfg.SourceVideoBitrate) * 8
			}
			futureSize = int64(duration * float64(cfg.TargetVideoBitrate) / 8)
			processingTime = int(math.Ceil(duration / cfg.TranscodingVideoRate))
		}
		// Audio files
		if content.MediaType == "audio" || (ext != "" && utils.AudioExtensionMap[ext]) {
			isProcessable = true
			duration := content.Duration
			if duration <= 0 {
				duration = float64(content.CompressedSize) / float64(cfg.SourceAudioBitrate) * 8
			}
			futureSize = int64(duration * float64(cfg.TargetAudioBitrate) / 8)
			processingTime = int(math.Ceil(duration / cfg.TranscodingAudioRate))
		}
		// Image files
		if content.MediaType == "image" || (ext != "" && utils.ImageExtensionMap[ext]) {
			if ext != ".avif" { // Skip existing AVIF
				isProcessable = true
				futureSize = cfg.TargetImageSize
				processingTime = int(cfg.TranscodingImageTime)
			}
		}
		// Text/Ebook files
		if content.MediaType == "text" || (ext != "" && utils.TextExtensionMap[ext]) {
			isProcessable = true
			// Rough estimate for ebooks (compressed text is small)
			futureSize = cfg.TargetImageSize * 50
			processingTime = int(cfg.TranscodingImageTime * 12)
		}

		if isProcessable {
			hasProcessableContent = true
			totalFutureSize += futureSize
			totalProcessingTime += processingTime
		}
	}

	return totalFutureSize, totalProcessingTime, hasProcessableContent, totalArchiveSize
}

func (p *ArchiveProcessor) EstimateSize(m *ShrinkMedia, cfg *ProcessorConfig) (int64, int) {
	futureSize, processingTime, _, _ := p.EstimateSizeForArchive(m, cfg)
	return futureSize, processingTime
}

func (p *ArchiveProcessor) Process(ctx context.Context, m *ShrinkMedia, cfg *ProcessorConfig) ProcessResult {
	// Archives are handled by extracting and processing contents separately
	imageProc := NewImageProcessor()
	return p.ExtractAndProcess(ctx, m, cfg, imageProc, p.ffmpeg)
}

// lsar lists archive contents
func (p *ArchiveProcessor) lsar(path string) []ShrinkMedia {
	contents, _ := p.lsarWithStatus(path)
	return contents
}

// getPartFiles returns list of all part files for a multi-part archive
func (p *ArchiveProcessor) getPartFiles(path string) []string {
	partFilesMap := make(map[string]bool)
	dir := filepath.Dir(path)
	baseName := filepath.Base(path)
	
	// Get parts from lsar XADVolumes
	if lsarOutput, err := exec.Command("lsar", "-json", path).CombinedOutput(); err == nil {
		var lsarJSON struct {
			LsarProperties struct {
				XADVolumes []string `json:"XADVolumes"`
			} `json:"lsarProperties"`
		}
		if json.Unmarshal(lsarOutput, &lsarJSON) == nil && len(lsarJSON.LsarProperties.XADVolumes) > 0 {
			for _, partFile := range lsarJSON.LsarProperties.XADVolumes {
				if !filepath.IsAbs(partFile) {
					partFile = filepath.Join(dir, partFile)
				}
				// Only include files that exist
				if _, err := os.Stat(partFile); err == nil {
					partFilesMap[partFile] = true
				}
			}
		}
	}
	
	// Also use glob to find any additional part files that lsar might have missed
	// Common multi-part archive patterns: .z01, .z02, .zip, .001, .002, .rar, etc.
	ext := strings.ToLower(filepath.Ext(baseName))
	baseWithoutExt := strings.TrimSuffix(baseName, ext)
	
	// Pattern 1: .zNN parts (Zip split files)
	if pattern, err := filepath.Glob(filepath.Join(dir, baseWithoutExt+".z*")); err == nil {
		for _, p := range pattern {
			if _, err := os.Stat(p); err == nil {
				partFilesMap[p] = true
			}
		}
	}
	
	// Pattern 2: .NNN parts (generic split files)
	if pattern, err := filepath.Glob(filepath.Join(dir, baseWithoutExt+".???")); err == nil {
		for _, p := range pattern {
			if _, err := os.Stat(p); err == nil {
				partFilesMap[p] = true
			}
		}
	}
	
	// Pattern 3: .partNN.rar or .rNN.rar (RAR split files)
	if strings.HasSuffix(ext, ".rar") {
		if pattern, err := filepath.Glob(filepath.Join(dir, baseWithoutExt+".part*.rar")); err == nil {
			for _, p := range pattern {
				if _, err := os.Stat(p); err == nil {
					partFilesMap[p] = true
				}
			}
		}
		if pattern, err := filepath.Glob(filepath.Join(dir, baseWithoutExt+".r??")); err == nil {
			for _, p := range pattern {
				if _, err := os.Stat(p); err == nil {
					partFilesMap[p] = true
				}
			}
		}
	}
	
	// Convert map to slice
	var partFiles []string
	for p := range partFilesMap {
		partFiles = append(partFiles, p)
	}
	
	// Sort for consistent ordering
	sort.Strings(partFiles)
	
	return partFiles
}

// lsarWithStatus lists archive contents and returns whether lsar encountered an error
func (p *ArchiveProcessor) lsarWithStatus(path string) ([]ShrinkMedia, bool) {
	output, err := exec.Command("lsar", "-json", path).CombinedOutput()
	lsarFailed := err != nil

	// Parse JSON to check for lsarError field
	var rawJSON map[string]interface{}
	if jsonErr := json.Unmarshal(output, &rawJSON); jsonErr == nil {
		if lsarErr, ok := rawJSON["lsarError"]; ok {
			if lsarErrNum, ok := lsarErr.(float64); ok && lsarErrNum != 0 {
				lsarFailed = true
			}
		}
	}

	if err != nil {
		return nil, lsarFailed
	}

	var result struct {
		LsarContents []struct {
			Filename       string `json:"XADFileName"`
			Size           int64  `json:"XADFileSize"`
			CompressedSize int64  `json:"XADCompressedSize"`
		} `json:"lsarContents"`
	}

	if err := json.Unmarshal(output, &result); err != nil {
		slog.Error("Failed to unmarshal lsar output", "error", err, "path", path)
		return nil, lsarFailed
	}

	var media []ShrinkMedia
	for _, f := range result.LsarContents {
		ext := strings.ToLower(filepath.Ext(f.Filename))
		mediaType := detectMediaTypeFromExt(ext)

		media = append(media, ShrinkMedia{
			Path:           f.Filename,
			Size:           f.Size,
			CompressedSize: f.CompressedSize,
			MediaType:      mediaType,
			Ext:            ext,
		})
	}
	return media, lsarFailed
}

// detectMediaTypeFromExt determines media type from file extension
func detectMediaTypeFromExt(ext string) string {
	switch {
	case utils.VideoExtensionMap[ext]:
		return "video"
	case utils.AudioExtensionMap[ext]:
		return "audio"
	case utils.ImageExtensionMap[ext]:
		return "image"
	case utils.TextExtensionMap[ext]:
		return "text"
	case utils.ArchiveExtensionMap[ext]:
		return "archive"
	default:
		return ""
	}
}

// flattenWrapperFolders moves files from single subfolders up to the parent directory
// This handles archives that contain a single wrapper folder
func flattenWrapperFolders(rootDir string) {
	entries, err := os.ReadDir(rootDir)
	if err != nil {
		return
	}

	// Filter out hidden files
	var nonHidden []string
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), ".") {
			nonHidden = append(nonHidden, entry.Name())
		}
	}

	// Only flatten if there's exactly one entry
	if len(nonHidden) != 1 {
		return
	}

	singleEntry := nonHidden[0]
	singlePath := filepath.Join(rootDir, singleEntry)

	// Check if it's a directory
	info, err := os.Stat(singlePath)
	if err != nil || !info.IsDir() {
		return
	}

	slog.Info("Flattening wrapper folder", "folder", singleEntry)

	// Move all items up
	innerEntries, err := os.ReadDir(singlePath)
	if err != nil {
		return
	}

	// Check for conflict item (file/folder with same name as the wrapper folder)
	var conflictItem string
	for _, entry := range innerEntries {
		if filepath.Join(rootDir, entry.Name()) == singlePath {
			conflictItem = entry.Name()
			break
		}
	}

	// Move non-conflict items first
	for _, entry := range innerEntries {
		if entry.Name() == conflictItem {
			continue
		}
		oldPath := filepath.Join(singlePath, entry.Name())
		newPath := filepath.Join(rootDir, entry.Name())
		if err := os.Rename(oldPath, newPath); err != nil {
			slog.Warn("Failed to flatten wrapper folder entry", "from", oldPath, "to", newPath, "error", err)
		}
	}

	// Handle conflict item if it exists
	if conflictItem != "" {
		oldPath := filepath.Join(singlePath, conflictItem)
		tempPath := filepath.Join(rootDir, conflictItem+".tmp")
		finalPath := filepath.Join(rootDir, conflictItem)
		os.Rename(oldPath, tempPath)
		os.RemoveAll(singlePath)
		os.Rename(tempPath, finalPath)
	} else {
		os.RemoveAll(singlePath)
	}
}

// ProcessorRegistry manages all media processors
type ProcessorRegistry struct {
	processors []MediaProcessor
}

// NewProcessorRegistry creates a new registry with all available processors
func NewProcessorRegistry(ffmpeg *FFmpegProcessor) *ProcessorRegistry {
	return &ProcessorRegistry{
		processors: []MediaProcessor{
			NewVideoProcessor(ffmpeg),
			NewAudioProcessor(ffmpeg),
			NewImageProcessor(),
			NewTextProcessor(),
			NewArchiveProcessor(ffmpeg),
		},
	}
}

// GetProcessor returns the appropriate processor for a media item
func (r *ProcessorRegistry) GetProcessor(m *ShrinkMedia) MediaProcessor {
	for _, p := range r.processors {
		if p.CanProcess(m) {
			return p
		}
	}
	return nil
}

// GetAllProcessors returns all registered processors
func (r *ProcessorRegistry) GetAllProcessors() []MediaProcessor {
	return r.processors
}

// ShouldShrink determines if a file should be shrinked based on savings threshold
func ShouldShrink(m *ShrinkMedia, futureSize int64, cfg *ProcessorConfig) bool {
	if cfg.ForceShrink {
		return true
	}
	shouldShrinkBuffer := int64(float64(futureSize) * getMinSavings(m, cfg))
	return m.Size > (futureSize + shouldShrinkBuffer)
}

func getMinSavings(m *ShrinkMedia, cfg *ProcessorConfig) float64 {
	switch strings.ToLower(m.Category) {
	case "video":
		return cfg.MinSavingsVideo
	case "audio":
		return cfg.MinSavingsAudio
	case "image", "text":
		return cfg.MinSavingsImage
	default:
		return 0.05 // Default 5%
	}
}
