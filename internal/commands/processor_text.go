package commands

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/chapmanjacobd/shrink/internal/models"
	"github.com/chapmanjacobd/shrink/internal/utils"
)

// TextProcessor handles text/ebook file processing
type TextProcessor struct {
	BaseProcessor
}

func NewTextProcessor() *TextProcessor {
	return &TextProcessor{
		BaseProcessor: BaseProcessor{category: "Text", requiredTool: "calibre"},
	}
}

func (p *TextProcessor) CanProcess(m *models.ShrinkMedia) bool {
	return utils.TextExtensionMap[m.Ext]
}

func (p *TextProcessor) EstimateSize(m *models.ShrinkMedia, cfg *models.ProcessorConfig) models.ProcessableInfo {
	// Rough estimate for ebooks
	return models.ProcessableInfo{
		FutureSize:     cfg.Image.TargetImageSize * 50,
		ProcessingTime: int(cfg.Image.TranscodingImageTime * 12),
		IsProcessable:  true,
	}
}

func (p *TextProcessor) Process(ctx context.Context, m *models.ShrinkMedia, cfg *models.ProcessorConfig, registry models.ProcessorRegistry) models.ProcessResult {
	return p.processText(ctx, m, cfg)
}

// processText handles the actual text/ebook processing
func (p *TextProcessor) processText(ctx context.Context, m *models.ShrinkMedia, cfg *models.ProcessorConfig) models.ProcessResult {
	ebookConvert := utils.GetCommandPath("ebook-convert")
	if ebookConvert == "" {
		return models.ProcessResult{SourcePath: m.Path, Error: fmt.Errorf("calibre not installed")}
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
		return models.ProcessResult{SourcePath: m.Path, Error: err}
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

	// Setup memory monitoring if configured
	var output []byte
	var err error

	if cfg.Common.MemoryLimit > 0 {
		cmd := exec.CommandContext(ctx, ebookConvert, args...)
		utils.SetupProcessGroup(cmd)

		stderrPipe, pipeErr := cmd.StderrPipe()
		if pipeErr != nil {
			os.RemoveAll(outputDir)
			return models.ProcessResult{SourcePath: m.Path, Error: pipeErr, StopAll: true}
		}

		monCfg := utils.ProcessMonitorConfig{
			MemoryLimit:   cfg.Common.MemoryLimit,
			CheckInterval: time.Duration(cfg.Common.MemoryCheckInterval) * time.Millisecond,
		}
		monitor := utils.NewProcessMonitor(cmd, monCfg)

		if startErr := cmd.Start(); startErr != nil {
			os.RemoveAll(outputDir)
			return models.ProcessResult{SourcePath: m.Path, Error: startErr, StopAll: true}
		}

		monitor.Start(ctx)
		defer monitor.Stop()

		waitErr := cmd.Wait()
		output, _ = io.ReadAll(stderrPipe)

		if waitErr != nil {
			os.RemoveAll(outputDir)

			if monitor.Exceeded() {
				return models.ProcessResult{
					SourcePath: m.Path,
					Error:      fmt.Errorf("process exceeded memory limit of %s", utils.FormatSize(cfg.Common.MemoryLimit)),
					Output:     string(output),
					StopAll:    true,
				}
			}

			return models.ProcessResult{SourcePath: m.Path, Error: waitErr, Output: string(output)}
		}
	} else {
		cmd := exec.CommandContext(ctx, ebookConvert, args...)
		output, err = cmd.CombinedOutput()
		if err != nil {
			// Clean up on failure
			os.RemoveAll(outputDir)
			return models.ProcessResult{SourcePath: m.Path, Error: err, Output: string(output)}
		}
	}

	if !p.folderExists(outputDir) {
		os.RemoveAll(outputDir)
		return models.ProcessResult{SourcePath: m.Path, Error: fmt.Errorf("calibre output folder missing")}
	}

	// Step 3: Replace CSS with optimized version
	p.replaceCSS(outputDir)

	// Step 4: Process images inside ebook (convert to AVIF) and update content.opf
	converted := p.processEbookImagesWithManifest(ctx, outputDir, cfg)

	// Step 5: Update references in HTML files
	p.updateImageReferences(outputDir, converted)

	// Step 6: Repackage to EPUB
	epubPath := strings.TrimSuffix(m.Path, ext) + ".epub"
	if err := p.packageToEPUB(ctx, outputDir, epubPath, cfg); err != nil {
		os.RemoveAll(outputDir)
		return models.ProcessResult{SourcePath: m.Path, Error: err}
	}

	// Step 7: Clean up .OEB folder
	os.RemoveAll(outputDir)

	// Step 8: Return result with EPUB path
	var outputSize int64
	if info, err := os.Stat(epubPath); err == nil {
		outputSize = info.Size()
	}

	return models.ProcessResult{
		SourcePath: m.Path,
		Outputs:    []models.ProcessOutputFile{{Path: epubPath, Size: outputSize}},
		Success:    true,
	}
}

// runOCR runs OCR on a PDF file using ocrmypdf
func (p *TextProcessor) runOCR(path string, cfg *models.ProcessorConfig) string {
	ocrmypdf := utils.GetCommandPath("ocrmypdf")
	if ocrmypdf == "" {
		return ""
	}

	// Auto-detect OCR capabilities if no explicit flag is set
	// Matches Python behavior: if tesseract+gs available, default to --skip-text
	// Otherwise, skip OCR entirely
	useSkipText := cfg.Text.SkipOCR
	useForceOCR := cfg.Text.ForceOCR
	useRedoOCR := cfg.Text.RedoOCR
	skipOCR := cfg.Text.NoOCR

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

	// Setup memory monitoring if configured
	var output []byte
	var err error

	if cfg.Common.MemoryLimit > 0 {
		cmd := exec.Command(ocrmypdf, args...)
		utils.SetupProcessGroup(cmd)

		stderrPipe, pipeErr := cmd.StderrPipe()
		if pipeErr != nil {
			slog.Debug("ocrmypdf StderrPipe failed", "error", pipeErr)
		} else {
			monCfg := utils.ProcessMonitorConfig{
				MemoryLimit:   cfg.Common.MemoryLimit,
				CheckInterval: time.Duration(cfg.Common.MemoryCheckInterval) * time.Millisecond,
			}
			monitor := utils.NewProcessMonitor(cmd, monCfg)

			if startErr := cmd.Start(); startErr != nil {
				slog.Debug("ocrmypdf Start failed", "error", startErr)
			} else {
				monitor.Start(context.Background())

				waitErr := cmd.Wait()
				output, _ = io.ReadAll(stderrPipe)

				monitor.Stop()

				if waitErr != nil {
					outputStr := string(output)
					// Check if it's a "skip-text" message (not really an error)
					if strings.Contains(outputStr, "already contains text") ||
						strings.Contains(outputStr, "skipping") {
						slog.Info("Skipping OCR (PDF already has text)", "path", path)
						os.Remove(outputPath)
						return ""
					}
					slog.Warn("OCR failed", "path", path, "error", waitErr, "output", outputStr)
					os.Remove(outputPath)
					return ""
				}
			}
		}
	}

	// Fallback to standard execution if monitoring not used or failed
	if err == nil {
		cmd := exec.Command(ocrmypdf, args...)
		output, err = cmd.CombinedOutput()
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
	}

	if _, err := os.Stat(outputPath); err == nil {
		return outputPath
	}

	return ""
}

// getCalibreVersion returns the Calibre version as a tuple
func (p *TextProcessor) getCalibreVersion() (int, int, int) {
	ebookConvert := utils.GetCommandPath("ebook-convert")
	if ebookConvert == "" {
		return 0, 0, 0
	}
	cmd := exec.Command(ebookConvert, "--version")
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

// processEbookImagesWithManifest converts images to AVIF and updates content.opf manifest
// Returns map of old basename -> new basename for successfully converted files
func (p *TextProcessor) processEbookImagesWithManifest(ctx context.Context, outputDir string, cfg *models.ProcessorConfig) map[string]string {
	converted := make(map[string]string)
	imCmd := getImageMagickCommand()
	if imCmd == "" {
		slog.Warn("ImageMagick not available, skipping ebook image conversion")
		return converted
	}

	// Read image files from content.opf manifest instead of scanning filesystem
	imageFiles := p.getImageFilesFromManifest(outputDir)

	for _, img := range imageFiles {
		// Respect context cancellation
		if ctx.Err() != nil {
			return converted
		}

		ext := filepath.Ext(img)
		extLower := strings.ToLower(ext)
		// Skip formats that shouldn't be converted to AVIF
		if !utils.ImageExtensionMap[extLower] || utils.IsOptimized(extLower) {
			continue
		}

		outputPath := strings.TrimSuffix(img, ext) + ".avif"
		args := []string{
			img,
			"-resize", fmt.Sprintf("%dx%d>", cfg.Image.MaxImageWidth, cfg.Image.MaxImageHeight),
			outputPath,
		}

		// Setup memory monitoring if configured
		var cmdErr error
		if cfg.Common.MemoryLimit > 0 {
			cmd := exec.CommandContext(ctx, imCmd, args...)
			utils.SetupProcessGroup(cmd)

			monCfg := utils.ProcessMonitorConfig{
				MemoryLimit:   cfg.Common.MemoryLimit,
				CheckInterval: time.Duration(cfg.Common.MemoryCheckInterval) * time.Millisecond,
			}
			monitor := utils.NewProcessMonitor(cmd, monCfg)

			if startErr := cmd.Start(); startErr != nil {
				cmdErr = startErr
			} else {
				monitor.Start(ctx)

				waitErr := cmd.Wait()
				monitor.Stop()

				if waitErr != nil {
					cmdErr = waitErr
				}
			}
		} else {
			cmd := exec.CommandContext(ctx, imCmd, args...)
			cmdErr = cmd.Run()
		}

		if cmdErr != nil {
			slog.Warn("Failed to convert ebook image", "path", img, "error", cmdErr)
			// Clean up partial transcode if original still exists
			if _, statErr := os.Stat(img); statErr == nil {
				os.Remove(outputPath)
			}
			continue
		}

		// Replace if smaller
		if info, err := os.Stat(outputPath); err == nil {
			if oldInfo, oldErr := os.Stat(img); oldErr == nil {
				if info.Size() > 0 && info.Size() < oldInfo.Size() {
					os.Remove(img)
					converted[filepath.Base(img)] = filepath.Base(outputPath)
				} else {
					os.Remove(outputPath)
				}
			} else {
				// If we can't stat original, something is very wrong, cleanup new file
				os.Remove(outputPath)
			}
		}
	}

	// Update content.opf manifest with converted image references
	if len(converted) > 0 {
		p.updateManifest(outputDir, converted)
	}

	return converted
}

// getImageFilesFromManifest reads the content.opf file and returns list of image file paths
func (p *TextProcessor) getImageFilesFromManifest(outputDir string) []string {
	var images []string
	manifestPath := filepath.Join(outputDir, "content.opf")

	content, err := os.ReadFile(manifestPath)
	if err != nil {
		slog.Warn("Failed to read content.opf", "error", err)
		return images
	}

	text := string(content)

	// Parse manifest items - look for <item> elements with image media types
	// Example: <item href="images/cover.jpg" media-type="image/jpeg" id="cover"/>
	lines := strings.SplitSeq(text, "\n")
	for line := range lines {
		if !strings.Contains(line, "<item") {
			continue
		}

		// Check if it's an image media-type
		if !strings.Contains(line, `media-type="image/`) {
			continue
		}

		// Extract href attribute
		href := p.extractAttribute(line, "href")
		if href == "" {
			continue
		}

		// Convert to absolute path
		imgPath := filepath.Join(outputDir, href)
		images = append(images, imgPath)
	}

	return images
}

// extractAttribute extracts an attribute value from an XML tag
func (p *TextProcessor) extractAttribute(tag, attrName string) string {
	// Look for attrName="value" or attrName='value'
	patterns := []string{
		attrName + `="`,
		attrName + `='`,
	}

	for _, pattern := range patterns {
		idx := strings.Index(tag, pattern)
		if idx == -1 {
			continue
		}

		start := idx + len(pattern)
		quote := pattern[len(pattern)-1]
		end := strings.Index(tag[start:], string(quote))
		if end == -1 {
			continue
		}

		return tag[start : start+end]
	}

	return ""
}

// updateManifest updates the content.opf file with new image references
func (p *TextProcessor) updateManifest(outputDir string, converted map[string]string) {
	manifestPath := filepath.Join(outputDir, "content.opf")

	content, err := os.ReadFile(manifestPath)
	if err != nil {
		slog.Warn("Failed to read content.opf for update", "error", err)
		return
	}

	text := string(content)
	modified := false

	// Update manifest items (href attributes in <item> elements)
	for oldName, newName := range converted {
		// Replace in href attributes: href="images/old.jpg" -> href="images/old.avif"
		if strings.Contains(text, `href="`+oldName+`"`) {
			text = strings.ReplaceAll(text, `href="`+oldName+`"`, `href="`+newName+`"`)
			modified = true
		}
		if strings.Contains(text, `href='`+oldName+`'`) {
			text = strings.ReplaceAll(text, `href='`+oldName+`'`, `href='`+newName+`'`)
			modified = true
		}

		// Update media-type attributes
		newMediaType := p.getMediaType(".avif")
		if newMediaType != "" {
			// Find and update media-type for this item
			text = p.updateMediaType(text, oldName, newMediaType)
		}
	}

	if modified {
		os.WriteFile(manifestPath, []byte(text), 0o644)
	}
}

// getMediaType returns the MIME type for a file extension
func (p *TextProcessor) getMediaType(ext string) string {
	switch strings.ToLower(ext) {
	case ".avif":
		return "image/avif"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".gif":
		return "image/gif"
	case ".svg":
		return "image/svg+xml"
	case ".webp":
		return "image/webp"
	case ".bmp":
		return "image/bmp"
	default:
		return ""
	}
}

// updateMediaType updates the media-type attribute for a specific item in the manifest
func (p *TextProcessor) updateMediaType(text, fileName, newMediaType string) string {
	// Find the item element containing this file
	// Look for patterns like: <item href="..." media-type="..." id="..."/>
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		if !strings.Contains(line, "<item") {
			continue
		}
		if !strings.Contains(line, fileName) {
			continue
		}

		// Update media-type in this line
		if strings.Contains(line, `media-type="`) {
			// Replace existing media-type
			oldType := p.extractAttribute(line, "media-type")
			if oldType != "" {
				line = strings.Replace(line, `media-type="`+oldType+`"`, `media-type="`+newMediaType+`"`, 1)
				lines[i] = line
			}
		}
	}

	return strings.Join(lines, "\n")
}

// updateImageReferences updates HTML files to reference new AVIF files
func (p *TextProcessor) updateImageReferences(dir string, converted map[string]string) {
	if len(converted) == 0 {
		return
	}

	filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if ext == ".html" || ext == ".xhtml" || ext == ".htm" {
			p.updateReferencesInFile(path, converted)
		}
		return nil
	})
}

// updateReferencesInFile updates image references in a single HTML file
func (p *TextProcessor) updateReferencesInFile(path string, converted map[string]string) {
	content, err := os.ReadFile(path)
	if err != nil {
		return
	}

	text := string(content)
	modified := false
	for oldName, newName := range converted {
		if strings.Contains(text, oldName) {
			text = strings.ReplaceAll(text, oldName, newName)
			modified = true
		}
	}

	if modified {
		os.WriteFile(path, []byte(text), 0o644)
	}
}

// folderExists checks if a folder exists
func (p *TextProcessor) folderExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// packageToEPUB creates an EPUB file from the OEB folder using Calibre
func (p *TextProcessor) packageToEPUB(ctx context.Context, folderPath, epubPath string, cfg *models.ProcessorConfig) error {
	ebookConvert := utils.GetCommandPath("ebook-convert")
	if ebookConvert == "" {
		return fmt.Errorf("calibre not installed")
	}

	args := []string{
		folderPath,
		epubPath,
		"--no-default-epub-cover",
		"--epub-inline-toc",
		"--dont-split-on-page-breaks",
	}

	// Setup memory monitoring if configured
	var output []byte
	var err error

	if cfg.Common.MemoryLimit > 0 {
		cmd := exec.CommandContext(ctx, ebookConvert, args...)
		utils.SetupProcessGroup(cmd)

		stderrPipe, pipeErr := cmd.StderrPipe()
		if pipeErr != nil {
			return pipeErr
		}

		monCfg := utils.ProcessMonitorConfig{
			MemoryLimit:   cfg.Common.MemoryLimit,
			CheckInterval: time.Duration(cfg.Common.MemoryCheckInterval) * time.Millisecond,
		}
		monitor := utils.NewProcessMonitor(cmd, monCfg)

		if startErr := cmd.Start(); startErr != nil {
			return startErr
		}

		monitor.Start(ctx)
		defer monitor.Stop()

		waitErr := cmd.Wait()
		output, _ = io.ReadAll(stderrPipe)

		if waitErr != nil {
			if monitor.Exceeded() {
				return fmt.Errorf("process exceeded memory limit of %s", utils.FormatSize(cfg.Common.MemoryLimit))
			}
			return fmt.Errorf("ebook-convert failed: %w, output: %s", waitErr, output)
		}
	} else {
		cmd := exec.CommandContext(ctx, ebookConvert, args...)
		output, err = cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("ebook-convert failed: %w, output: %s", err, output)
		}
	}

	return nil
}
