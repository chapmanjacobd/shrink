package commands

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/chapmanjacobd/shrink/internal/models"
	"github.com/chapmanjacobd/shrink/internal/utils"
)

// TextProcessor handles text/ebook file processing
type TextProcessor struct {
	BaseProcessor
	cssPath string
}

func NewTextProcessor() *TextProcessor {
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
	// Write CSS to temp file
	tmpFile, err := os.CreateTemp("", "calibre-*.css")
	cssPath := ""
	if err == nil {
		cssPath = tmpFile.Name()
		_, _ = tmpFile.WriteString(css)
		_ = tmpFile.Close()
	}

	return &TextProcessor{
		BaseProcessor: BaseProcessor{category: "Text", requiredTool: "calibre"},
		cssPath:       cssPath,
	}
}

func (p *TextProcessor) CanProcess(m *models.ShrinkMedia) bool {
	return utils.TextExtensionMap[m.Ext]
}

// Cleanup releases resources held by the TextProcessor
func (p *TextProcessor) Cleanup() {
	if p.cssPath != "" {
		_ = os.Remove(p.cssPath)
	}
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

	sourcePath := m.Path
	ext := strings.ToLower(filepath.Ext(sourcePath))
	workPath := sourcePath

	// Step 1: OCR for PDFs if needed. Keep the OCR output as an intermediate
	// file; the original path must remain the result source.
	if ext == ".pdf" && utils.CommandExists("ocrmypdf") {
		ocrPath := p.runOCR(ctx, sourcePath, cfg)
		if ocrPath != "" && ocrPath != sourcePath {
			workPath = ocrPath
			defer func() { _ = os.Remove(ocrPath) }()
		}
	}

	// Step 2: Convert with Calibre to folder format
	outputDir := filepath.Join(filepath.Dir(sourcePath), filepath.Base(sourcePath)+".OEB")
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return models.ProcessResult{SourcePath: sourcePath, Error: err}
	}

	args := []string{
		workPath,
		outputDir,
		"--minimum-line-height=105",
		"--unsmarten-punctuation",
	}

	// Use pdftohtml engine for PDFs with Calibre >= 7.19.0
	major, minor, _ := p.getCalibreVersionContext(ctx)
	if ext == ".pdf" && (major > 7 || (major == 7 && minor >= 19)) {
		args = append(args, "--pdf-engine", "pdftohtml")
	}

	// Setup systemd-run wrapper if configured (Linux only)
	systemdCfg := utils.SystemdRunConfig{
		MemoryLimit:   cfg.Common.MemoryLimit,
		MemorySwapMax: cfg.Common.MemorySwapMax,
		UseJournald:   cfg.Common.UseJournald,
		Enabled:       !cfg.Common.DisableSystemd,
	}

	output, err := utils.RunCommandWithSystemd(ctx, ebookConvert, args, systemdCfg)
	if err != nil {
		_ = os.RemoveAll(outputDir)
		return models.ProcessResult{SourcePath: sourcePath, Error: err, Output: string(output)}
	}

	if !p.folderExists(outputDir) {
		_ = os.RemoveAll(outputDir)
		return models.ProcessResult{SourcePath: sourcePath, Error: fmt.Errorf("calibre output folder missing")}
	}

	// Step 3: Process images inside ebook (convert to AVIF) and update content.opf
	converted := p.processEbookImagesWithManifest(ctx, outputDir, cfg)

	// Step 4: Update references in HTML files
	p.updateImageReferences(outputDir, converted)

	// Step 5: Repackage to EPUB
	epubPath := strings.TrimSuffix(sourcePath, filepath.Ext(sourcePath)) + ".OEB.epub"
	if err := p.packageToEPUB(ctx, outputDir, epubPath, cfg); err != nil {
		_ = os.RemoveAll(outputDir)
		return models.ProcessResult{SourcePath: sourcePath, Error: err}
	}

	// Step 6: Clean up .OEB folder
	_ = os.RemoveAll(outputDir)

	// Step 7: Return result with EPUB path
	var outputSize int64
	if info, err := os.Stat(epubPath); err == nil {
		outputSize = info.Size()
	}

	return models.ProcessResult{
		SourcePath: sourcePath,
		Outputs:    []models.ProcessOutputFile{{Path: epubPath, Size: outputSize}},
		Success:    true,
	}
}

// runOCR runs OCR on a PDF file using ocrmypdf
func (p *TextProcessor) runOCR(ctx context.Context, path string, cfg *models.ProcessorConfig) string {
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

	outputPath := strings.TrimSuffix(path, filepath.Ext(path)) + ".ocr.pdf"

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

	// Setup systemd-run wrapper if configured (Linux only)
	systemdCfg := utils.SystemdRunConfig{
		MemoryLimit:   cfg.Common.MemoryLimit,
		MemorySwapMax: cfg.Common.MemorySwapMax,
		UseJournald:   cfg.Common.UseJournald,
		Enabled:       !cfg.Common.DisableSystemd,
	}

	output, err := utils.RunCommandWithSystemd(ctx, ocrmypdf, args, systemdCfg)
	outputStr := string(output)
	if err != nil {
		// Check if it's a "skip-text" message (not really an error)
		if strings.Contains(outputStr, "already contains text") ||
			strings.Contains(outputStr, "skipping") {
			slog.Info("Skipping OCR (PDF already has text)", "path", path)
			_ = os.Remove(outputPath)
			return ""
		}
		slog.Warn("OCR failed", "path", path, "error", err, "output", outputStr)
		_ = os.Remove(outputPath)
		return ""
	}

	if _, err := os.Stat(outputPath); err == nil {
		return outputPath
	}

	return ""
}

var calibreVersionRegex = regexp.MustCompile(`\(calibre\s+(\d+)\.(\d+)\.(\d+)`)

// parseCalibreVersion extracts the version from output like
// "ebook-convert (calibre 7.19.0)".
func parseCalibreVersion(output string) (int, int, int) {
	match := calibreVersionRegex.FindStringSubmatch(output)
	if len(match) != 4 {
		return 0, 0, 0
	}
	major, _ := strconv.Atoi(match[1])
	minor, _ := strconv.Atoi(match[2])
	patch, _ := strconv.Atoi(match[3])
	return major, minor, patch
}

// getCalibreVersion returns the Calibre version as a tuple.
func (p *TextProcessor) getCalibreVersion() (int, int, int) {
	return p.getCalibreVersionContext(context.Background())
}

// getCalibreVersionContext returns the Calibre version as a tuple.
func (p *TextProcessor) getCalibreVersionContext(ctx context.Context) (int, int, int) {
	ebookConvert := utils.GetCommandPath("ebook-convert")
	if ebookConvert == "" {
		return 0, 0, 0
	}
	cmd := exec.CommandContext(ctx, ebookConvert, "--version")
	output, err := cmd.Output()
	if err != nil {
		return 0, 0, 0
	}
	return parseCalibreVersion(string(output))
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
		args := imageMagickLimitArgs(cfg)
		args = append(args,
			img,
			"-resize", fmt.Sprintf("%dx%d>", cfg.Image.MaxImageWidth, cfg.Image.MaxImageHeight),
			outputPath,
		)

		// Setup systemd-run wrapper if configured (Linux only)
		systemdCfg := utils.SystemdRunConfig{
			MemoryLimit:   cfg.Common.MemoryLimit,
			MemorySwapMax: cfg.Common.MemorySwapMax,
			UseJournald:   cfg.Common.UseJournald,
			Enabled:       !cfg.Common.DisableSystemd,
		}

		_, cmdErr := utils.RunCommandWithSystemd(ctx, imCmd, args, systemdCfg)
		if cmdErr != nil {
			slog.Warn("Failed to convert ebook image", "path", img, "error", cmdErr)
			// Clean up partial transcode if original still exists
			if _, statErr := os.Stat(img); statErr == nil {
				_ = os.Remove(outputPath)
			}
			continue
		}

		// Replace if smaller
		if info, err := os.Stat(outputPath); err == nil {
			if oldInfo, oldErr := os.Stat(img); oldErr == nil {
				if info.Size() > 0 && info.Size() < oldInfo.Size() {
					_ = os.Remove(img)
					converted[filepath.Base(img)] = filepath.Base(outputPath)
				} else {
					_ = os.Remove(outputPath)
				}
			} else {
				// If we can't stat original, something is very wrong, cleanup new file
				_ = os.Remove(outputPath)
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

		// Convert to absolute path and ensure it stays inside the extracted
		// book. A manifest href is untrusted input.
		imgPath := filepath.Join(outputDir, href)
		if !pathWithin(outputDir, imgPath) {
			slog.Warn("Ignoring manifest image outside book directory", "href", href)
			continue
		}
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
	lines := strings.Split(text, "\n")
	modified := false

	// Update manifest items (href attributes in <item> elements). Hrefs
	// commonly include a directory (images/cover.jpg), so compare against the
	// basename and preserve the prefix.
	for i, line := range lines {
		if !strings.Contains(line, "<item") {
			continue
		}
		href := p.extractAttribute(line, "href")
		if href == "" {
			continue
		}
		base := filepath.Base(href)
		newName, ok := converted[base]
		if !ok {
			continue
		}

		newHref := strings.TrimSuffix(href, base) + newName
		line = replaceAttribute(line, "href", href, newHref)

		if mediaType := p.getMediaType(filepath.Ext(newName)); mediaType != "" {
			if oldType := p.extractAttribute(line, "media-type"); oldType != "" {
				line = replaceAttribute(line, "media-type", oldType, mediaType)
			}
		}

		lines[i] = line
		modified = true
	}

	if modified {
		_ = os.WriteFile(manifestPath, []byte(strings.Join(lines, "\n")), 0o644)
	}
}

// replaceAttribute replaces an XML attribute value without changing the quote
// style. Attribute matching is intentionally simple because content.opf is
// generated by Calibre and uses plain attribute syntax.
func replaceAttribute(tag, attrName, oldValue, newValue string) string {
	for _, quote := range []string{`"`, `'`} {
		old := attrName + "=" + quote + oldValue + quote
		if strings.Contains(tag, old) {
			replacement := attrName + "=" + quote + newValue + quote
			return strings.Replace(tag, old, replacement, 1)
		}
	}
	return tag
}

// pathWithin reports whether target resolves inside base.
func pathWithin(base, target string) bool {
	rel, err := filepath.Rel(base, target)
	if err != nil {
		return false
	}
	if filepath.IsAbs(rel) {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// replaceImageReference replaces a basename reference without matching it as a
// substring of a longer filename (for example "1.jpg" inside "11.jpg").
func replaceImageReference(text, oldName, newName string) string {
	pattern := regexp.MustCompile(`(^|[^A-Za-z0-9_.-])` + regexp.QuoteMeta(oldName) + `([^A-Za-z0-9_.-]|$)`)
	return pattern.ReplaceAllString(text, `${1}`+newName+`${2}`)
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

// updateImageReferences updates HTML files to reference new AVIF files
func (p *TextProcessor) updateImageReferences(dir string, converted map[string]string) {
	if len(converted) == 0 {
		return
	}

	if err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if ext == ".html" || ext == ".xhtml" || ext == ".htm" {
			p.updateReferencesInFile(path, converted)
		}
		return nil
	}); err != nil {
		slog.Warn("Failed to walk directory for image reference updates", "dir", dir, "error", err)
	}
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
			updated := replaceImageReference(text, oldName, newName)
			if updated != text {
				text = updated
				modified = true
			}
		}
	}

	if modified {
		_ = os.WriteFile(path, []byte(text), 0o644)
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

	// Use content.opf as the input file (not the folder)
	opfPath := filepath.Join(folderPath, "content.opf")

	args := []string{
		opfPath,
		epubPath,
		"--no-default-epub-cover",
		"--epub-inline-toc",
		"--dont-split-on-page-breaks",
		"--epub-max-image-size=none",
		"--output-profile=tablet",
	}

	if p.cssPath != "" {
		args = append(args, "--extra-css="+p.cssPath)
	}

	// Setup systemd-run wrapper if configured (Linux only)
	systemdCfg := utils.SystemdRunConfig{
		MemoryLimit:   cfg.Common.MemoryLimit,
		MemorySwapMax: cfg.Common.MemorySwapMax,
		UseJournald:   cfg.Common.UseJournald,
		Enabled:       !cfg.Common.DisableSystemd,
	}

	output, err := utils.RunCommandWithSystemd(ctx, ebookConvert, args, systemdCfg)
	if err != nil {
		return fmt.Errorf("ebook-convert failed: %w, output: %s", err, output)
	}

	return nil
}
