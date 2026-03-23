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
	"strconv"
	"strings"

	"github.com/chapmanjacobd/shrink/internal/ffmpeg"
	"github.com/chapmanjacobd/shrink/internal/models"
	"github.com/chapmanjacobd/shrink/internal/utils"
)

// ArchiveProcessor handles archive file processing
type ArchiveProcessor struct {
	BaseProcessor
	unarInstalled bool
	ffmpeg        *ffmpeg.FFmpegProcessor
}

func NewArchiveProcessor(ffmpeg *ffmpeg.FFmpegProcessor) *ArchiveProcessor {
	return &ArchiveProcessor{
		BaseProcessor: BaseProcessor{category: "Archived", requiredTool: "unar"},
		ffmpeg:        ffmpeg,
		unarInstalled: utils.CommandExists("lsar") && utils.CommandExists("unar"),
	}
}

// extractLSARJSON extracts valid JSON from lsar output, handling potential extra text on Windows
func extractLSARJSON(output []byte) []byte {
	// On Windows, lsar may output text before/after JSON. Find JSON boundaries.
	// Look for the first '{' and last '}' to extract valid JSON
	s := string(output)
	startIdx := strings.Index(s, "{")
	endIdx := strings.LastIndex(s, "}")

	if startIdx >= 0 && endIdx > startIdx {
		return []byte(s[startIdx : endIdx+1])
	}
	return output
}

func (p *ArchiveProcessor) CanProcess(m *models.ShrinkMedia) bool {
	return m.MediaType == "archive" || utils.ArchiveExtensionMap[m.Ext]
}

// ExtractAndProcess extracts archive contents and processes media recursively
func (p *ArchiveProcessor) ExtractAndProcess(ctx context.Context, m *models.ShrinkMedia, cfg *models.ProcessorConfig,
	imageProc *ImageProcessor, ffmpegProc *ffmpeg.FFmpegProcessor, registry models.ProcessorRegistry,
) models.ProcessResult {
	lsar := utils.GetCommandPath("lsar")
	unar := utils.GetCommandPath("unar")
	if lsar == "" || unar == "" {
		return models.ProcessResult{SourcePath: m.Path, Error: fmt.Errorf("unar/lsar not installed")}
	}

	// Check for multi-part archives
	partFiles := p.getPartFiles(m.Path)
	if len(partFiles) > 0 {
		slog.Info("Multi-part archive detected", "path", m.Path, "parts", len(partFiles))
		for i, p := range partFiles {
			slog.Info("Part file identified", "index", i, "path", p)
		}
	}

	// Log archive contents before extraction
	if contents, lsarFailed := p.lsarWithStatus(m.Path); !lsarFailed {
		slog.Info("Lsar identified files in archive", "path", m.Path, "count", len(contents))
		for _, c := range contents {
			slog.Debug("Archive content", "file", c.Path, "size", c.Size)
		}
	} else {
		slog.Warn("Lsar failed to list archive contents", "path", m.Path)
	}

	// Extract archive - use -force-rename to extract files.
	// We avoid -no-directory as it can sometimes cause issues with split archives on some platforms.
	// We use the base name and set cmd.Dir to help unar find parts.
	outputDir := filepath.Join(filepath.Dir(m.Path), filepath.Base(m.Path)+".extracted")
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return models.ProcessResult{SourcePath: m.Path, PartFiles: partFiles, Error: err}
	}

	cmd := exec.CommandContext(ctx, unar, "-force-rename", "-o", outputDir, filepath.Base(m.Path))
	cmd.Dir = filepath.Dir(m.Path)
	output, err := cmd.CombinedOutput()
	if err != nil {
		// Clean up on failure
		os.RemoveAll(outputDir)
		return models.ProcessResult{SourcePath: m.Path, PartFiles: partFiles, Error: err, Output: string(output)}
	}

	// Verify that something was actually extracted
	entries, err := os.ReadDir(outputDir)
	if err != nil || len(entries) == 0 {
		os.RemoveAll(outputDir)
		return models.ProcessResult{SourcePath: m.Path, PartFiles: partFiles, Error: fmt.Errorf("extraction produced no files"), Output: string(output)}
	}

	// Log extracted files for debugging
	for _, entry := range entries {
		slog.Info("Extracted item", "archive", m.Path, "name", entry.Name(), "isDir", entry.IsDir())
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
			imgMedia := &models.ShrinkMedia{Path: path, Size: fileSize, Ext: ext, Category: "Image"}
			info := imageProc.EstimateSize(imgMedia, cfg)
			if imgMedia.ShouldShrink(info.FutureSize, cfg) {
				res := imageProc.processImage(ctx, imgMedia, cfg)
				if res.Success && len(res.Outputs) > 0 {
					var totalSize int64
					for _, out := range res.Outputs {
						totalSize += out.Size
					}
					if totalSize < fileSize {
						os.Remove(path)
					} else {
						for _, out := range res.Outputs {
							if !pathsEqual(out.Path, path) {
								os.Remove(out.Path)
							}
						}
					}
				}
			}
		} else if utils.VideoExtensionMap[ext] || utils.AudioExtensionMap[ext] {
			category := "Video"
			processor := models.MediaProcessor(NewVideoProcessor(ffmpegProc))
			if utils.AudioExtensionMap[ext] {
				category = "Audio"
				processor = NewAudioProcessor(ffmpegProc)
			}
			media := &models.ShrinkMedia{Path: path, Size: fileSize, Ext: ext, Category: category}

			// Use ProbeMedia to get video count if needed
			if category == "Video" || category == "Audio" {
				if probed, err := ffmpeg.ProbeMedia(path); err == nil {
					media.VideoCount = len(probed.VideoStreams)
					media.AudioCount = len(probed.AudioStreams)
					media.Duration = probed.Duration
				}
			}

			futureInfo := processor.EstimateSize(media, cfg)
			if media.ShouldShrink(futureInfo.FutureSize, cfg) {
				res := ffmpegProc.Process(ctx, media, cfg, registry)
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
							if !pathsEqual(out.Path, path) {
								os.Remove(out.Path)
							}
						}
					}
				}
			}
		} else if utils.ArchiveExtensionMap[ext] {
			if isSecondaryPart(path) {
				slog.Debug("Skipping nested secondary archive part", "path", path)
				return nil
			}
			nestedMedia := &models.ShrinkMedia{Path: path, Size: info.Size(), Ext: ext, MediaType: "archive"}
			res := p.ExtractAndProcess(ctx, nestedMedia, cfg, imageProc, ffmpegProc, registry)
			if res.Error != nil {
				if res.Output != "" {
					slog.Warn("Failed to extract nested archive", "path", path, "error", res.Error, "output", res.Output)
				} else {
					slog.Warn("Failed to extract nested archive", "path", path, "error", res.Error)
				}
			}
			if res.Success {
				// Get part files for multi-part archives BEFORE deleting the main file
				nestedPartFiles := p.getPartFiles(path)
				// Delete the nested archive file and all its part files after extraction
				os.Remove(path)
				// Also delete any multi-part archive parts
				for _, partFile := range nestedPartFiles {
					if !pathsEqual(partFile, path) {
						os.Remove(partFile)
					}
				}
				// The extracted contents have already been processed recursively
				// and the decision to keep original or processed files was made
			}
		}
		return nil
	})

	return models.ProcessResult{
		SourcePath: m.Path,
		Outputs:    []models.ProcessOutputFile{{Path: outputDir, Size: utils.FolderSize(outputDir)}},
		PartFiles:  partFiles,
		Success:    true,
	}
}

// EstimateSizeForArchive estimates size using compressed size and inspects archive contents
// Returns: futureSize, processingTime, hasProcessableContent, totalArchiveSize
func (p *ArchiveProcessor) EstimateSizeForArchive(m *models.ShrinkMedia, cfg *models.ProcessorConfig) (int64, int, bool, int64) {
	if !p.unarInstalled {
		return 0, 0, false, 0
	}

	// Skip secondary parts of multi-part archives to avoid double processing
	if isSecondaryPart(m.Path) {
		slog.Debug("Skipping secondary archive part", "path", m.Path)
		return 0, 0, false, 0
	}

	// Get archive contents and check for multi-part volumes
	contents, lsarFailed := p.lsarWithStatus(m.Path)
	slog.Info("lsar returned", "path", m.Path, "count", len(contents), "lsarFailed", lsarFailed)

	// Check for multi-part archives and verify all parts exist
	totalArchiveSize := m.Size
	partFiles := p.getPartFiles(m.Path)

	// Check for missing parts in sequence for known multi-part types
	if isBrokenSequence(m.Path, partFiles) {
		slog.Info("Broken sequence detected for archive", "path", m.Path)
		return 0, 0, false, 0
	}

	// Sum up sizes
	if len(partFiles) > 0 {
		totalArchiveSize = 0
		if info, err := os.Stat(m.Path); err == nil {
			totalArchiveSize += info.Size()
		}
		for _, partFile := range partFiles {
			if info, err := os.Stat(partFile); err == nil {
				totalArchiveSize += info.Size()
			} else {
				// Missing part file - archive is broken
				return 0, 0, false, 0
			}
		}
	}

	// If lsar failed (empty contents due to error or missing parts), archive is broken
	if lsarFailed {
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
			duration := float64(content.CompressedSize) / float64(cfg.Common.SourceVideoBitrate) * 8
			futureSize = int64(duration * float64(cfg.Video.TargetVideoBitrate) / 8)
			processingTime = int(math.Ceil(duration / cfg.Video.TranscodingVideoRate))
			totalArchiveSize += content.Size
			slog.Info("Nested archive estimation", "path", content.Path, "futureSize", futureSize, "archiveFileSize", content.Size)
		}

		// Video files
		if !isProcessable && (content.MediaType == "video" || (ext != "" && utils.VideoExtensionMap[ext])) {
			isProcessable = true
			duration := content.Duration
			if duration <= 0 {
				// Estimate from compressed size (smaller = lower quality source)
				duration = float64(content.CompressedSize) / float64(cfg.Common.SourceVideoBitrate) * 8
			}
			futureSize = int64(duration * float64(cfg.Video.TargetVideoBitrate) / 8)
			processingTime = int(math.Ceil(duration / cfg.Video.TranscodingVideoRate))
		}
		// Audio files
		if content.MediaType == "audio" || (ext != "" && utils.AudioExtensionMap[ext]) {
			isProcessable = true
			duration := content.Duration
			if duration <= 0 {
				duration = float64(content.CompressedSize) / float64(cfg.Common.SourceAudioBitrate) * 8
			}
			futureSize = int64(duration * float64(cfg.Audio.TargetAudioBitrate) / 8)
			processingTime = int(math.Ceil(duration / cfg.Audio.TranscodingAudioRate))
		}
		// Image files
		if content.MediaType == "image" || (ext != "" && utils.ImageExtensionMap[ext]) {
			if ext != ".avif" { // Skip existing AVIF
				isProcessable = true
				futureSize = cfg.Image.TargetImageSize
				processingTime = int(cfg.Image.TranscodingImageTime)
			}
		}
		// Text/Ebook files
		if content.MediaType == "text" || (ext != "" && utils.TextExtensionMap[ext]) {
			isProcessable = true
			// Rough estimate for ebooks (compressed text is small)
			futureSize = cfg.Image.TargetImageSize * 50
			processingTime = int(cfg.Image.TranscodingImageTime * 12)
		}

		if isProcessable {
			hasProcessableContent = true
			totalFutureSize += futureSize
			totalProcessingTime += processingTime
		}
	}

	return totalFutureSize, totalProcessingTime, hasProcessableContent, totalArchiveSize
}

func (p *ArchiveProcessor) EstimateSize(m *models.ShrinkMedia, cfg *models.ProcessorConfig) models.ProcessableInfo {
	futureSize, processingTime, hasProcessable, totalArchiveSize := p.EstimateSizeForArchive(m, cfg)
	isBroken := false
	var partFiles []string
	if !hasProcessable {
		if totalArchiveSize == 0 {
			isBroken = true
			partFiles = p.getPartFiles(m.Path)
		}
	}
	return models.ProcessableInfo{
		FutureSize:     futureSize,
		ProcessingTime: processingTime,
		IsProcessable:  hasProcessable,
		ActualSize:     totalArchiveSize,
		IsBroken:       isBroken,
		PartFiles:      partFiles,
	}
}

func (p *ArchiveProcessor) Process(ctx context.Context, m *models.ShrinkMedia, cfg *models.ProcessorConfig, registry models.ProcessorRegistry) models.ProcessResult {
	// Archives are handled by extracting and processing contents separately
	imageProc := NewImageProcessor()
	return p.ExtractAndProcess(ctx, m, cfg, imageProc, p.ffmpeg, registry)
}

// pathsEqual compares two paths for equality, handling Windows case-insensitivity and path styles
func pathsEqual(p1, p2 string) bool {
	if p1 == p2 {
		return true
	}
	abs1, err1 := filepath.Abs(p1)
	abs2, err2 := filepath.Abs(p2)
	if err1 != nil || err2 != nil {
		return strings.EqualFold(p1, p2)
	}

	// Remove \\?\ prefix on Windows if present
	abs1 = strings.TrimPrefix(abs1, `\\?\`)
	abs2 = strings.TrimPrefix(abs2, `\\?\`)

	// Clean paths
	abs1 = filepath.Clean(abs1)
	abs2 = filepath.Clean(abs2)

	// Case-insensitive comparison for Windows
	return strings.EqualFold(abs1, abs2)
}

// isBrokenSequence checks if a multi-part archive has gaps in its part sequence
func isBrokenSequence(mainPath string, partFiles []string) bool {
	ext := strings.ToLower(filepath.Ext(mainPath))
	if ext == ".zip" {
		// Look for .z01, .z02...
		maxN := 0
		found := make(map[int]bool)
		for _, p := range partFiles {
			pext := strings.ToLower(filepath.Ext(p))
			// Check for .zNN pattern
			if len(pext) >= 3 && pext[1] == 'z' {
				if n, err := strconv.Atoi(pext[2:]); err == nil && n > 0 {
					found[n] = true
					if n > maxN {
						maxN = n
					}
				}
			}
		}
		if maxN > 0 {
			for i := 1; i <= maxN; i++ {
				if !found[i] {
					return true // Gap in sequence
				}
			}
		}
	} else if ext == ".rar" || ext == "" { // "" for parts without extension if identified as rar
		// Look for .part1.rar, .part2.rar... or .r00, .r01...
		maxPart := 0
		foundPart := make(map[int]bool)
		for _, p := range partFiles {
			base := strings.ToLower(filepath.Base(p))
			if strings.Contains(base, ".part") {
				idx := strings.LastIndex(base, ".part")
				numPart := base[idx+5:]
				if endIdx := strings.Index(numPart, "."); endIdx != -1 {
					numPart = numPart[:endIdx]
				}
				if n, err := strconv.Atoi(numPart); err == nil && n > 0 {
					foundPart[n] = true
					if n > maxPart {
						maxPart = n
					}
				}
			}
		}
		if maxPart > 0 {
			for i := 1; i <= maxPart; i++ {
				if !foundPart[i] {
					return true
				}
			}
		}
	}
	return false
}

// isSecondaryPart returns true if the path is a part of a multi-part archive
// but NOT the primary entry point (e.g. .z01 is secondary if .zip exists)
func isSecondaryPart(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	dir := filepath.Dir(path)
	base := filepath.Base(path)
	nameWithoutExt := strings.TrimSuffix(base, filepath.Ext(base))

	// Zip: .z01, .z02... are secondary if .zip exists
	if len(ext) >= 3 && ext[1] == 'z' {
		if _, err := strconv.Atoi(ext[2:]); err == nil {
			// It's a .zNN file. Check if .zip exists
			if _, err := os.Stat(filepath.Join(dir, nameWithoutExt+".zip")); err == nil {
				return true
			}
			// If .zip doesn't exist, .z01 is the primary entry point for unar
			if ext != ".z01" {
				return true // .z02+ are always secondary
			}
		}
	}

	// RAR: .part2.rar, .part3.rar... or .r01, .r02...
	if strings.HasSuffix(ext, ".rar") {
		if strings.Contains(nameWithoutExt, ".part") {
			idx := strings.LastIndex(nameWithoutExt, ".part")
			partNum := nameWithoutExt[idx+5:]
			if n, err := strconv.Atoi(partNum); err == nil {
				if n > 1 {
					return true // .part2+ are secondary
				}
				// .part1.rar is secondary if .rar exists
				if _, err := os.Stat(filepath.Join(dir, nameWithoutExt[:idx]+".rar")); err == nil {
					return true
				}
			}
		}
	} else if len(ext) >= 3 && ext[1] == 'r' {
		if n, err := strconv.Atoi(ext[2:]); err == nil {
			// .r00, .r01...
			// Check if .rar exists
			if _, err := os.Stat(filepath.Join(dir, nameWithoutExt+".rar")); err == nil {
				return true
			}
			if n > 0 {
				return true // .r01+ are secondary if .rar doesn't exist but .r00 does
			}
		}
	}

	// 7z / Generic: .002, .003... are secondary if .001 exists
	if len(ext) >= 3 {
		if n, err := strconv.Atoi(ext[1:]); err == nil {
			if n > 1 {
				// Check if .001 or .000 or .0 exists
				prefixes := []string{".001", ".000", ".0", ".1"}
				for _, p := range prefixes {
					if p == ext {
						continue
					}
					if _, err := os.Stat(filepath.Join(dir, nameWithoutExt+p)); err == nil {
						return true
					}
				}
				return true // Higher numbers are generally secondary
			}
		}
	}

	return false
}

// getPartFiles returns list of all part files for a multi-part archive
func (p *ArchiveProcessor) getPartFiles(path string) []string {
	partFilesMap := make(map[string]bool)
	dir := filepath.Dir(path)
	baseName := filepath.Base(path)

	// Get parts from lsar XADVolumes
	lsar := utils.GetCommandPath("lsar")
	if lsar != "" {
		lsarCmd := exec.Command(lsar, "-json", path)
		lsarCmd.Dir = dir
		if lsarOutput, err := lsarCmd.CombinedOutput(); err == nil || len(lsarOutput) > 0 {
			jsonBytes := extractLSARJSON(lsarOutput)
			var lsarJSON struct {
				LsarProperties struct {
					XADVolumes []string `json:"XADVolumes"`
				} `json:"lsarProperties"`
			}
			if json.Unmarshal(jsonBytes, &lsarJSON) == nil && len(lsarJSON.LsarProperties.XADVolumes) > 0 {
				for _, partFile := range lsarJSON.LsarProperties.XADVolumes {
					if !filepath.IsAbs(partFile) {
						partFile = filepath.Join(dir, partFile)
					}
					// Only include files that exist and are not the main archive
					if info, err := os.Stat(partFile); err == nil && !info.IsDir() {
						absPart, _ := filepath.Abs(partFile)
						absMain, _ := filepath.Abs(path)
						if !pathsEqual(absPart, absMain) {
							partFilesMap[absPart] = true
							slog.Debug("Found multi-part archive part (lsar)", "path", absPart)
						}
					}
				}
			}
		}
	}

	// Also use glob to find any additional part files that lsar might have missed
	// Common multi-part archive patterns: .z01, .z02, .zip, .001, .002, .rar, etc.
	ext := strings.ToLower(filepath.Ext(baseName))

	// baseWithoutExt should be the name before the first archive-related extension
	// e.g., "test.zip" -> "test", "test.tar.gz" -> "test"
	baseWithoutExt := baseName
	for {
		e := strings.ToLower(filepath.Ext(baseWithoutExt))
		if e == "" {
			break
		}
		// If it's a known archive extension or a part extension (like .z01, .001), trim it
		isArchiveExt := false
		for _, ae := range utils.ArchiveExtensions {
			if e == "."+ae {
				isArchiveExt = true
				break
			}
		}
		// Check for .zNN or .NNN
		if !isArchiveExt {
			if len(e) == 4 && e[1] == 'z' && e[2] >= '0' && e[2] <= '9' && e[3] >= '0' && e[3] <= '9' {
				isArchiveExt = true
			} else if len(e) == 4 && e[1] >= '0' && e[1] <= '9' && e[2] >= '0' && e[2] <= '9' && e[3] >= '0' && e[3] <= '9' {
				isArchiveExt = true
			}
		}

		if isArchiveExt {
			baseWithoutExt = strings.TrimSuffix(baseWithoutExt, e)
		} else {
			break
		}
	}
	slog.Debug("Globbing for parts", "baseWithoutExt", baseWithoutExt, "dir", dir)

	// Pattern 1: .zNN parts (Zip split files)
	if pattern, err := filepath.Glob(filepath.Join(dir, baseWithoutExt+".z*")); err == nil {
		for _, p := range pattern {
			if info, err := os.Stat(p); err == nil && !info.IsDir() {
				absP, _ := filepath.Abs(p)
				absPath, _ := filepath.Abs(path)
				if !pathsEqual(absP, absPath) {
					partFilesMap[absP] = true
					slog.Debug("Found multi-part archive part (glob-z)", "path", absP)
				}
			}
		}
	}

	// Pattern 2: .NNN parts (generic split files)
	if pattern, err := filepath.Glob(filepath.Join(dir, baseWithoutExt+".???")); err == nil {
		for _, p := range pattern {
			if info, err := os.Stat(p); err == nil && !info.IsDir() {
				absP, _ := filepath.Abs(p)
				absPath, _ := filepath.Abs(path)
				if !pathsEqual(absP, absPath) {
					partFilesMap[absP] = true
					slog.Debug("Found multi-part archive part (glob-NNN)", "path", absP)
				}
			}
		}
	}

	// Pattern 3: .partNN.rar or .rNN.rar (RAR split files)
	if strings.HasSuffix(ext, ".rar") {
		if pattern, err := filepath.Glob(filepath.Join(dir, baseWithoutExt+".part*.rar")); err == nil {
			for _, p := range pattern {
				if _, err := os.Stat(p); err == nil {
					absP, _ := filepath.Abs(p)
					absPath, _ := filepath.Abs(path)
					if !pathsEqual(absP, absPath) {
						partFilesMap[absP] = true
					}
				}
			}
		}
		if pattern, err := filepath.Glob(filepath.Join(dir, baseWithoutExt+".r??")); err == nil {
			for _, p := range pattern {
				if _, err := os.Stat(p); err == nil {
					absP, _ := filepath.Abs(p)
					absPath, _ := filepath.Abs(path)
					if !pathsEqual(absP, absPath) {
						partFilesMap[absP] = true
					}
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
func (p *ArchiveProcessor) lsarWithStatus(path string) ([]models.ShrinkMedia, bool) {
	lsar := utils.GetCommandPath("lsar")
	if lsar == "" {
		return nil, true
	}
	lsarCmd := exec.Command(lsar, "-json", path)
	lsarCmd.Dir = filepath.Dir(path)
	output, err := lsarCmd.CombinedOutput()
	lsarFailed := err != nil

	jsonBytes := extractLSARJSON(output)

	// Parse JSON to check for lsarError field
	var rawJSON map[string]any
	if jsonErr := json.Unmarshal(jsonBytes, &rawJSON); jsonErr == nil {
		if lsarErr, ok := rawJSON["lsarError"]; ok {
			if lsarErrNum, ok := lsarErr.(float64); ok && lsarErrNum != 0 {
				lsarFailed = true
			}
		}
	}

	var result struct {
		LsarContents []struct {
			Filename       string `json:"XADFileName"`
			Size           int64  `json:"XADFileSize"`
			CompressedSize int64  `json:"XADCompressedSize"`
		} `json:"lsarContents"`
	}

	if err := json.Unmarshal(jsonBytes, &result); err != nil {
		if lsarFailed {
			return nil, true
		}
		slog.Error("Failed to unmarshal lsar output", "error", err, "path", path)
		return nil, false
	}

	var media []models.ShrinkMedia
	for _, f := range result.LsarContents {
		ext := strings.ToLower(filepath.Ext(f.Filename))
		mediaType := detectMediaTypeFromExt(ext)

		media = append(media, models.ShrinkMedia{
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

	// Filter out hidden files and system junk
	var nonHidden []string
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") || name == "__MACOSX" || name == "Thumbs.db" || name == "desktop.ini" {
			slog.Debug("Ignoring system junk during flatten check", "name", name)
			continue
		}
		nonHidden = append(nonHidden, name)
	}

	// Only flatten if there's exactly one entry
	if len(nonHidden) != 1 {
		slog.Debug("Not flattening wrapper folder: multiple or zero non-hidden entries", "count", len(nonHidden), "entries", nonHidden)
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
		} else {
			slog.Debug("Flattened entry", "from", oldPath, "to", newPath)
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

	// Recursive call to handle nested wrapper folders (e.g. wrapper/wrapper/contents)
	flattenWrapperFolders(rootDir)
}
