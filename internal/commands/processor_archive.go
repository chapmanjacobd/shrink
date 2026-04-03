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
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/chapmanjacobd/shrink/internal/ffmpeg"
	"github.com/chapmanjacobd/shrink/internal/models"
	"github.com/chapmanjacobd/shrink/internal/utils"
)

// Archive processing timeout constants
const (
	defaultArchiveAnalyzeTimeout = 5 * time.Minute  // Default timeout for lsar analyze commands
	defaultArchiveGlobTimeout    = 2 * time.Minute  // Default timeout for glob operations
	defaultArchiveExtractTimeout = 30 * time.Minute // Default timeout for unar extraction
	defaultGetPartFilesTimeout   = 10 * time.Minute // Default timeout for getPartFiles operation
)

// getArchiveAnalyzeTimeout returns timeout for lsar analyze operations (listing contents)
func getArchiveAnalyzeTimeout(cfg *models.CommonConfig) time.Duration {
	timeout := time.Duration(cfg.ArchiveAnalyzeTimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = defaultArchiveAnalyzeTimeout
	}
	return timeout
}

// getArchiveGlobTimeout returns timeout for glob operations finding part files
func getArchiveGlobTimeout(cfg *models.CommonConfig, filePath string) time.Duration {
	baseTimeout := time.Duration(cfg.ArchiveGlobTimeoutSec) * time.Second
	if baseTimeout <= 0 {
		baseTimeout = defaultArchiveGlobTimeout
	}

	// Add extra time for large files on slow mounts (2s per 10MB, max additional 5m)
	if info, err := os.Stat(filePath); err == nil {
		extraTime := min(time.Duration(info.Size()/5/1024/1024)*time.Second, 5*time.Minute)
		return baseTimeout + extraTime
	}
	return baseTimeout
}

var splitArchiveRegex = regexp.MustCompile(`^\.(z|r|c|part)?\d{1,4}$`)

// globWithTimeout performs a filepath.Glob with a timeout to prevent hanging.
// Note: If filepath.Glob hangs indefinitely (e.g., on a hung network mount),
// the goroutine will complete in the background but the result will be discarded.
func globWithTimeout(pattern string, timeout time.Duration) ([]string, error) {
	resultChan := make(chan struct {
		matches []string
		err     error
	}, 1)

	go func() {
		matches, err := filepath.Glob(pattern)
		resultChan <- struct {
			matches []string
			err     error
		}{matches, err}
	}()

	select {
	case result := <-resultChan:
		return result.matches, result.err
	case <-time.After(timeout):
		slog.Warn("Glob operation timed out", "pattern", pattern, "timeout", timeout)
		return nil, fmt.Errorf("glob timeout after %v", timeout)
	}
}

// ArchiveProcessor handles archive file processing
type ArchiveProcessor struct {
	BaseProcessor
	unarInstalled bool
	ffmpeg        *ffmpeg.FFmpegProcessor
	cfg           *models.ProcessorConfig
}

func NewArchiveProcessor(ffmpeg *ffmpeg.FFmpegProcessor, cfg *models.ProcessorConfig) *ArchiveProcessor {
	return &ArchiveProcessor{
		BaseProcessor: BaseProcessor{category: "Archived", requiredTool: "unar"},
		ffmpeg:        ffmpeg,
		cfg:           cfg,
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
	// Check if it's a known archive extension or a multi-part archive pattern
	return utils.ArchiveExtensionMap[m.Ext] || isMultiPartArchiveExt(m.Ext)
}

// isMultiPartArchiveExt checks if an extension is a multi-part archive pattern
// Returns true for .zNN, .rNN, .NNN patterns (e.g., .z01, .r00, .001)
func isMultiPartArchiveExt(ext string) bool {
	return splitArchiveRegex.MatchString(strings.ToLower(ext))
}

// ExtractAndProcess extracts archive contents and processes media recursively
func (p *ArchiveProcessor) ExtractAndProcess(ctx context.Context, m *models.ShrinkMedia, cfg *models.ProcessorConfig,
	imageProc *ImageProcessor, ffmpegProc *ffmpeg.FFmpegProcessor, registry models.ProcessorRegistry, depth int,
) models.ProcessResult {
	// Limit recursion depth to prevent stack overflow from maliciously nested archives
	if depth > 10 {
		slog.Warn("Max archive recursion depth reached, skipping nested archive", "path", m.Path)
		return models.ProcessResult{SourcePath: m.Path, Success: true}
	}

	// Skip secondary part files - only process the primary archive
	if isSecondaryPart(m.Path) {
		slog.Debug("Skipping secondary archive part", "path", m.Path)
		return models.ProcessResult{SourcePath: m.Path, Success: true} // Treat as successfully handled (no action needed)
	}

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
	if contents, lsarFailed, _ := p.lsarWithStatus(m.Path); !lsarFailed {
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

	// Setup systemd-run wrapper if configured (Linux only)
	systemdCfg := utils.SystemdRunConfig{
		MemoryLimit:   p.cfg.Common.MemoryLimit,
		MemorySwapMax: p.cfg.Common.MemorySwapMax,
		UseJournald:   p.cfg.Common.UseJournald,
		Enabled:       !p.cfg.Common.DisableSystemd,
		Dir:           filepath.Dir(m.Path),
	}

	unarArgs := []string{"-force-rename", "-o", outputDir, filepath.Base(m.Path)}
	output, err := utils.RunCommandWithSystemd(ctx, unar, unarArgs, systemdCfg)
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

	// Find and process all media recursively, collecting output files
	var outputFiles []models.ProcessOutputFile
	processedDirs := make(map[string]bool)

	filepath.Walk(outputDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		absPath, _ := filepath.Abs(path)
		if info.IsDir() {
			if processedDirs[absPath] {
				return filepath.SkipDir
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		fileSize := info.Size()

		if utils.ImageExtensionMap[ext] && !utils.IsOptimized(ext) {
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
						// Add processed outputs
						outputFiles = append(outputFiles, res.Outputs...)
					} else {
						// Keep original, delete outputs
						for _, out := range res.Outputs {
							if !pathsEqual(out.Path, path) {
								os.Remove(out.Path)
							}
						}
						// Add original as output
						outputFiles = append(outputFiles, models.ProcessOutputFile{Path: path, Size: fileSize})
					}
				} else {
					// Processing didn't produce outputs, keep original
					outputFiles = append(outputFiles, models.ProcessOutputFile{Path: path, Size: fileSize})
				}
			} else {
				// Shouldn't shrink, keep as-is
				outputFiles = append(outputFiles, models.ProcessOutputFile{Path: path, Size: fileSize})
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
						// Add processed outputs
						outputFiles = append(outputFiles, res.Outputs...)
					} else {
						// Delete transcode and keep original
						for _, out := range res.Outputs {
							if !pathsEqual(out.Path, path) {
								os.Remove(out.Path)
							}
						}
						// Add original as output
						outputFiles = append(outputFiles, models.ProcessOutputFile{Path: path, Size: fileSize})
					}
				} else {
					// Processing didn't produce outputs, keep original
					outputFiles = append(outputFiles, models.ProcessOutputFile{Path: path, Size: fileSize})
				}
			} else {
				// Shouldn't shrink, keep as-is
				outputFiles = append(outputFiles, models.ProcessOutputFile{Path: path, Size: fileSize})
			}
		} else if utils.ArchiveExtensionMap[ext] || isMultiPartArchiveExt(ext) {
			if isSecondaryPart(path) {
				slog.Debug("Skipping nested secondary archive part", "path", path)
				return nil
			}
			nestedMedia := &models.ShrinkMedia{Path: path, Size: info.Size(), Ext: ext, MediaType: "archive"}
			res := p.ExtractAndProcess(ctx, nestedMedia, cfg, imageProc, ffmpegProc, registry, depth+1)
			if res.Error != nil {
				if res.Output != "" {
					slog.Warn("Failed to extract nested archive", "path", path, "error", res.Error, "output", res.Output)
				} else {
					slog.Warn("Failed to extract nested archive", "path", path, "error", res.Error)
				}
				// Extraction failed - keep the original archive file and track it as output
				outputFiles = append(outputFiles, models.ProcessOutputFile{Path: path, Size: info.Size()})
				return nil
			}
			if res.Success {
				// Skip the output directory of the nested archive in the current walk
				nestedOutputDir := filepath.Join(filepath.Dir(path), filepath.Base(path)+".extracted")
				absNestedOutputDir, _ := filepath.Abs(nestedOutputDir)
				processedDirs[absNestedOutputDir] = true

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
				// Add nested archive outputs (extracted files from nested archive)
				outputFiles = append(outputFiles, res.Outputs...)
				// The extracted contents have already been processed recursively
				// and the decision to keep original or processed files was made
			}
		} else {
			// Unknown or unsupported file type, still track it
			outputFiles = append(outputFiles, models.ProcessOutputFile{Path: path, Size: fileSize})
		}
		return nil
	})

	return models.ProcessResult{
		SourcePath: m.Path,
		Outputs:    outputFiles,
		PartFiles:  partFiles,
		Success:    true,
	}
}

// ArchiveEstimateResult holds the result of archive size estimation
type ArchiveEstimateResult struct {
	FutureSize       int64
	ProcessingTime   int
	HasProcessable   bool
	TotalArchiveSize int64
	IsBroken         bool
	IsTimeout        bool
}

// EstimateSizeForArchive estimates size using compressed size and inspects archive contents
func (p *ArchiveProcessor) EstimateSizeForArchive(m *models.ShrinkMedia, cfg *models.ProcessorConfig) ArchiveEstimateResult {
	result := ArchiveEstimateResult{}

	slog.Debug("EstimateSizeForArchive starting", "path", m.Path)
	if !p.unarInstalled {
		return result
	}

	// Skip secondary parts of multi-part archives to avoid double processing
	if isSecondaryPart(m.Path) {
		slog.Debug("Skipping secondary archive part", "path", m.Path)
		return result
	}

	// Get archive contents and check for multi-part volumes
	contents, lsarFailed, isTimeout := p.lsarWithStatus(m.Path)
	slog.Debug("Archive contents retrieved", "path", m.Path, "count", len(contents), "lsarFailed", lsarFailed, "isTimeout", isTimeout)

	// Check for multi-part archives and verify all parts exist
	result.TotalArchiveSize = m.Size

	// Only call getPartFiles if lsar failed for a reason other than timeout
	// (timeout is likely due to large archives, not missing parts)
	var partFiles []string
	if !isTimeout {
		slog.Debug("Calling getPartFiles", "path", m.Path)
		partFiles = p.getPartFiles(m.Path)
		slog.Debug("getPartFiles returned", "path", m.Path, "parts", len(partFiles))

		// Check for missing parts in sequence for known multi-part types
		if isBrokenSequence(m.Path, partFiles) {
			slog.Info("Broken sequence detected for archive", "path", m.Path)
			result.IsBroken = true
			return result
		}

		// Sum up sizes
		if len(partFiles) > 0 {
			result.TotalArchiveSize = 0
			if info, err := os.Stat(m.Path); err == nil {
				result.TotalArchiveSize += info.Size()
			}
			for _, partFile := range partFiles {
				if info, err := os.Stat(partFile); err == nil {
					result.TotalArchiveSize += info.Size()
				} else {
					// Missing part file - archive is broken
					result.IsBroken = true
					return result
				}
			}
		}
	}

	// If lsar failed (empty contents due to error or missing parts), archive is broken
	if lsarFailed {
		result.IsBroken = true
		return result
	}

	// If lsar timed out, mark it so we don't cache this result
	if isTimeout {
		result.IsTimeout = true
		return result
	}

	if len(contents) == 0 {
		// Archive has no contents but lsar didn't fail - just no processable content
		return result
	}

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
		if ext != "" && (utils.ArchiveExtensionMap[ext] || isMultiPartArchiveExt(ext)) {
			if isSecondaryPart(content.Path) {
				slog.Debug("Skipping nested secondary archive part during estimation", "path", content.Path)
				continue
			}
			slog.Info("Found nested archive", "path", content.Path, "compressedSize", content.CompressedSize)
			isProcessable = true
			// Estimate based on compressed size (assume video content for simplicity)
			duration := float64(content.CompressedSize) / float64(cfg.Common.SourceVideoBitrate) * 8
			futureSize = int64(duration * float64(cfg.Video.TargetVideoBitrate) / 8)
			processingTime = int(math.Ceil(duration / cfg.Video.TranscodingVideoRate))
			slog.Info("Nested archive estimation", "path", content.Path, "futureSize", futureSize, "archiveFileSize", content.Size)
		}

		// Video files
		if !isProcessable && ext != "" && utils.VideoExtensionMap[ext] {
			isProcessable = true
			duration := content.Duration
			if duration <= 0 {
				// Estimate from compressed size (smaller = lower quality source)
				sourceBitrate := float64(utils.GetEstimatedBitrate(ext))
				if sourceBitrate <= 0 {
					sourceBitrate = float64(cfg.Common.SourceVideoBitrate)
				}
				duration = float64(content.CompressedSize) / sourceBitrate * 8
			}

			targetBitrate := float64(cfg.Video.TargetVideoBitrate)
			transcodeRate := cfg.Video.TranscodingVideoRate
			if content.Width > 0 && content.Height > 0 {
				maxW := float64(cfg.Video.MaxVideoWidth)
				maxH := float64(cfg.Video.MaxVideoHeight)
				actualW := float64(content.Width)
				actualH := float64(content.Height)
				outW, outH := actualW, actualH
				if outW > maxW || outH > maxH {
					scale := math.Min(maxW/outW, maxH/outH)
					outW *= scale
					outH *= scale
				}
				baselinePixels := maxW * maxH
				outPixels := outW * outH
				if baselinePixels > 0 {
					pixelRatio := outPixels / baselinePixels
					if pixelRatio < 0.25 {
						pixelRatio = 0.25
					}
					if pixelRatio > 1.0 {
						pixelRatio = 1.0
					}
					targetBitrate *= pixelRatio

					sourcePixels := actualW * actualH
					if sourcePixels > outPixels && outPixels > 0 {
						complexityRatio := outPixels / sourcePixels
						if complexityRatio < 0.2 {
							complexityRatio = 0.2
						}
						transcodeRate *= complexityRatio
					}
				}
			}

			futureSize = int64(duration * targetBitrate / 8)
			processingTime = int(math.Ceil(duration / transcodeRate))
		}
		// Audio files
		if !isProcessable && ext != "" && utils.AudioExtensionMap[ext] {
			isProcessable = true
			duration := content.Duration
			if duration <= 0 {
				sourceBitrate := float64(utils.GetEstimatedBitrate(ext))
				if sourceBitrate <= 0 {
					sourceBitrate = float64(cfg.Common.SourceAudioBitrate)
				}
				duration = float64(content.CompressedSize) / sourceBitrate * 8
			}
			futureSize = int64(duration * float64(cfg.Audio.TargetAudioBitrate) / 8)
			processingTime = int(math.Ceil(duration / cfg.Audio.TranscodingAudioRate))
		}
		// Image files
		if !isProcessable && ext != "" && utils.ImageExtensionMap[ext] {
			if !utils.IsOptimized(ext) { // Skip existing optimized formats
				isProcessable = true
				futureSize = cfg.Image.TargetImageSize
				processingTime = int(cfg.Image.TranscodingImageTime)
			}
		}
		// Text/Ebook files
		if !isProcessable && ext != "" && utils.TextExtensionMap[ext] {
			isProcessable = true
			// Rough estimate for ebooks (compressed text is small)
			futureSize = cfg.Image.TargetImageSize * 50
			processingTime = int(cfg.Image.TranscodingImageTime * 12)
		}

		if isProcessable {
			result.HasProcessable = true
			result.FutureSize += futureSize
			result.ProcessingTime += processingTime
		}
	}

	slog.Debug("EstimateSizeForArchive complete", "path", m.Path, "futureSize", result.FutureSize, "processable", result.HasProcessable)
	return result
}

func (p *ArchiveProcessor) EstimateSize(m *models.ShrinkMedia, cfg *models.ProcessorConfig) models.ProcessableInfo {
	slog.Debug("EstimateSize starting", "path", m.Path)
	est := p.EstimateSizeForArchive(m, cfg)

	var partFiles []string
	if !est.HasProcessable && est.TotalArchiveSize == 0 {
		slog.Debug("Archive has no processable content and size=0, checking parts", "path", m.Path)
		partFiles = p.getPartFiles(m.Path)
		slog.Debug("getPartFiles (broken check) returned", "path", m.Path, "parts", len(partFiles))
	} else if est.IsBroken {
		// Also get part files for broken archives
		partFiles = p.getPartFiles(m.Path)
	}

	slog.Debug("EstimateSize complete", "path", m.Path, "processable", est.HasProcessable, "broken", est.IsBroken, "timeout", est.IsTimeout)
	return models.ProcessableInfo{
		FutureSize:     est.FutureSize,
		ProcessingTime: est.ProcessingTime,
		IsProcessable:  est.HasProcessable,
		ActualSize:     est.TotalArchiveSize,
		IsBroken:       est.IsBroken,
		IsTimeout:      est.IsTimeout,
		PartFiles:      partFiles,
	}
}

func (p *ArchiveProcessor) Process(ctx context.Context, m *models.ShrinkMedia, cfg *models.ProcessorConfig, registry models.ProcessorRegistry) models.ProcessResult {
	// Archives are handled by extracting and processing contents separately
	imageProc := NewImageProcessor()
	return p.ExtractAndProcess(ctx, m, cfg, imageProc, p.ffmpeg, registry, 0)
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
			if strings.HasPrefix(pext, ".z") {
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
	if strings.HasPrefix(ext, ".z") {
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
	} else if strings.HasPrefix(ext, ".r") {
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
	if isMultiPartArchiveExt(ext) {
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
	slog.Debug("Getting part files", "path", path)

	// Use a channel and goroutine to implement timeout
	resultChan := make(chan []string, 1)
	timeout := time.Duration(p.cfg.Common.ArchiveGlobTimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = defaultGetPartFilesTimeout
	}

	go func() {
		resultChan <- p.getPartFilesImpl(path)
	}()

	select {
	case result := <-resultChan:
		return result
	case <-time.After(timeout):
		slog.Warn("getPartFiles timed out", "path", path, "timeout", timeout)
		return nil
	}
}

// getPartFilesImpl is the actual implementation of getPartFiles
func (p *ArchiveProcessor) getPartFilesImpl(path string) []string {
	partFilesMap := make(map[string]bool)
	dir := filepath.Dir(path)
	baseName := filepath.Base(path)

	// Get parts from lsar XADVolumes
	lsar := utils.GetCommandPath("lsar")
	if lsar == "" {
		// lsar not available, skip multi-part handling
		slog.Debug("lsar not found, skipping multi-part detection", "path", path)
		return nil
	}

	slog.Debug("Calling lsar for XADVolumes", "path", path)

	// Add timeout to prevent hanging for large archives
	lsarTimeout := getArchiveAnalyzeTimeout(&p.cfg.Common)
	ctx, cancel := context.WithTimeout(context.Background(), lsarTimeout)
	defer cancel()

	// Use systemd-run wrapper if configured (Linux only)
	systemdCfg := utils.SystemdRunConfig{
		MemoryLimit:   p.cfg.Common.MemoryLimit,
		MemorySwapMax: p.cfg.Common.MemorySwapMax,
		UseJournald:   p.cfg.Common.UseJournald,
		Enabled:       !p.cfg.Common.DisableSystemd,
		Dir:           dir,
	}

	lsarArgs := []string{"-json", path}
	lsarOutput, err := utils.RunCommandWithSystemd(ctx, lsar, lsarArgs, systemdCfg)

	if len(lsarOutput) == 0 && err == nil {
		// Fallback to CombinedOutput if systemd-run not used or failed
		fallbackCmd := exec.CommandContext(ctx, lsar, "-json", path)
		fallbackCmd.Dir = dir
		lsarOutput, err = fallbackCmd.CombinedOutput()
	}

	// Handle context timeout - err may not be set if systemd-run wrapper swallowed it
	if ctx.Err() == context.DeadlineExceeded {
		slog.Warn("lsar XADVolumes call timed out", "path", path)
		if err == nil {
			err = fmt.Errorf("lsar timeout")
		}
	}

	lsarFailed := false
	if err == nil || len(lsarOutput) > 0 {
		slog.Debug("lsar XADVolumes call returned", "path", path, "output_len", len(lsarOutput))
		jsonBytes := extractLSARJSON(lsarOutput)
		var lsarJSON struct {
			LsarProperties struct {
				XADVolumes []string `json:"XADVolumes"`
			} `json:"lsarProperties"`
		}
		if json.Unmarshal(jsonBytes, &lsarJSON) == nil && len(lsarJSON.LsarProperties.XADVolumes) > 0 {
			slog.Debug("lsar returned XADVolumes", "path", path, "count", len(lsarJSON.LsarProperties.XADVolumes))
			for _, partFile := range lsarJSON.LsarProperties.XADVolumes {
				if !filepath.IsAbs(partFile) {
					partFile = filepath.Join(dir, partFile)
				}
				// Only include files that exist and are not the main archive
				slog.Debug("Checking lsar part file existence", "part", partFile)
				if info, err := os.Stat(partFile); err == nil && !info.IsDir() {
					absPart, _ := filepath.Abs(partFile)
					absMain, _ := filepath.Abs(path)
					if !pathsEqual(absPart, absMain) {
						partFilesMap[absPart] = true
						slog.Debug("Found multi-part archive part (lsar)", "path", absPart)
					}
				} else {
					slog.Debug("lsar part file not found or is directory", "part", partFile, "err", err)
				}
			}
		} else {
			slog.Debug("lsar returned no XADVolumes or parse failed", "path", path)
			lsarFailed = true
		}
	} else {
		slog.Debug("lsar XADVolumes call failed", "path", path, "err", err)
		lsarFailed = true
	}

	// Only use glob as fallback if lsar failed
	if lsarFailed {
		slog.Debug("lsar failed, falling back to glob", "path", path)
		partFilesMap = p.getPartFilesByGlob(partFilesMap, path, dir, baseName)
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

// getPartFilesByGlob finds archive parts using glob patterns (fallback when lsar fails)
func (p *ArchiveProcessor) getPartFilesByGlob(partFilesMap map[string]bool, path, dir, baseName string) map[string]bool {
	ext := strings.ToLower(filepath.Ext(baseName))
	slog.Debug("Starting glob search for archive parts", "path", path, "ext", ext)

	// baseWithoutExt should be the name before the first archive-related extension
	// e.g., "test.zip" -> "test", "test.tar.gz" -> "test"
	baseWithoutExt := baseName
	loopCount := 0
	prevBaseWithoutExt := ""
	for {
		loopCount++
		if loopCount > 20 {
			slog.Warn("Extension stripping loop exceeded 20 iterations, breaking", "path", path, "baseWithoutExt", baseWithoutExt)
			break
		}
		// Safety check: if baseWithoutExt isn't changing, we're in an infinite loop
		if baseWithoutExt == prevBaseWithoutExt {
			slog.Warn("Extension stripping loop detected no change, breaking", "path", path, "baseWithoutExt", baseWithoutExt)
			break
		}
		prevBaseWithoutExt = baseWithoutExt

		e := strings.ToLower(filepath.Ext(baseWithoutExt))
		if e == "" {
			break
		}
		// If it's a known archive extension or a part extension (like .z01, .001), trim it
		isArchiveExt := utils.ArchiveExtensionMap[e] || isMultiPartArchiveExt(e)

		if isArchiveExt {
			// Use the original case extension for trimming, not the lowercased version
			originalExt := filepath.Ext(baseWithoutExt)
			newBase := strings.TrimSuffix(baseWithoutExt, originalExt)
			// Safety: ensure we actually removed something
			if newBase == baseWithoutExt {
				slog.Debug("TrimSuffix did not remove extension, breaking", "path", path, "ext", originalExt)
				break
			}
			baseWithoutExt = newBase
		} else {
			break
		}
	}
	slog.Debug("Extension stripping complete", "iterations", loopCount, "baseWithoutExt", baseWithoutExt)
	slog.Debug("Globbing for parts", "baseWithoutExt", baseWithoutExt, "dir", dir)

	globTimeout := getArchiveGlobTimeout(&p.cfg.Common, path)

	// Pattern 1: .zNN parts (Zip split files)
	pattern1 := filepath.Join(dir, baseWithoutExt+".z*")
	if pattern, err := globWithTimeout(pattern1, globTimeout); err == nil {
		for _, p := range pattern {
			if info, err := os.Stat(p); err == nil && !info.IsDir() {
				absP, _ := filepath.Abs(p)
				absPath, _ := filepath.Abs(path)
				if !pathsEqual(absP, absPath) {
					partFilesMap[absP] = true
				}
			}
		}
	}

	// Pattern 2: .NNN parts (generic split files)
	pattern2 := filepath.Join(dir, baseWithoutExt+".???")
	if pattern, err := globWithTimeout(pattern2, globTimeout); err == nil {
		for _, p := range pattern {
			if info, err := os.Stat(p); err == nil && !info.IsDir() {
				absP, _ := filepath.Abs(p)
				absPath, _ := filepath.Abs(path)
				if !pathsEqual(absP, absPath) {
					partFilesMap[absP] = true
				}
			}
		}
	}

	// Pattern 3: .partNN.rar or .rNN.rar (RAR split files)
	if strings.HasSuffix(ext, ".rar") {
		pattern3 := filepath.Join(dir, baseWithoutExt+".part*.rar")
		if pattern, err := globWithTimeout(pattern3, globTimeout); err == nil {
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
		pattern4 := filepath.Join(dir, baseWithoutExt+".r??")
		if pattern, err := globWithTimeout(pattern4, globTimeout); err == nil {
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

	return partFilesMap
}

// lsarWithStatus lists archive contents and returns whether lsar encountered an error
// Returns: contents, lsarFailed, isTimeout
func (p *ArchiveProcessor) lsarWithStatus(path string) ([]models.ShrinkMedia, bool, bool) {
	slog.Debug("lsarWithStatus starting", "path", path)
	lsar := utils.GetCommandPath("lsar")
	if lsar == "" {
		return nil, true, false
	}

	// Add timeout to prevent hanging for large archives
	lsarTimeout := getArchiveAnalyzeTimeout(&p.cfg.Common)
	ctx, cancel := context.WithTimeout(context.Background(), lsarTimeout)
	defer cancel()

	// Use systemd-run wrapper if configured (Linux only)
	systemdCfg := utils.SystemdRunConfig{
		MemoryLimit:   p.cfg.Common.MemoryLimit,
		MemorySwapMax: p.cfg.Common.MemorySwapMax,
		UseJournald:   p.cfg.Common.UseJournald,
		Enabled:       !p.cfg.Common.DisableSystemd,
		Dir:           filepath.Dir(path),
	}

	lsarArgs := []string{"-json", path}
	output, err := utils.RunCommandWithSystemd(ctx, lsar, lsarArgs, systemdCfg)

	// Use CombinedOutput if systemd-run not used or failed
	if len(output) == 0 && err == nil {
		slog.Debug("Running lsar with CombinedOutput", "path", path)
		lsarCmd := exec.CommandContext(ctx, lsar, "-json", path)
		lsarCmd.Dir = filepath.Dir(path)
		output, err = lsarCmd.CombinedOutput()
	}

	isTimeout := false
	if ctx.Err() == context.DeadlineExceeded {
		slog.Warn("lsar command timed out", "path", path)
		err = fmt.Errorf("lsar timeout")
		isTimeout = true
	}
	slog.Debug("lsar command completed", "path", path, "output_len", len(output), "err", err)
	lsarFailed := err != nil

	jsonBytes := extractLSARJSON(output)

	var result struct {
		LsarError    float64 `json:"lsarError"`
		LsarContents []struct {
			Filename       string `json:"XADFileName"`
			Size           int64  `json:"XADFileSize"`
			CompressedSize int64  `json:"XADCompressedSize"`
		} `json:"lsarContents"`
	}

	if err := json.Unmarshal(jsonBytes, &result); err != nil {
		if lsarFailed {
			return nil, true, isTimeout
		}
		slog.Error("Failed to unmarshal lsar output", "error", err, "path", path)
		return nil, false, false
	}

	if result.LsarError != 0 {
		lsarFailed = true
	}

	var media []models.ShrinkMedia
	for _, f := range result.LsarContents {
		ext := strings.ToLower(filepath.Ext(f.Filename))

		media = append(media, models.ShrinkMedia{
			Path:           f.Filename,
			Size:           f.Size,
			CompressedSize: f.CompressedSize,
			Ext:            ext,
		})
	}
	slog.Debug("lsarWithStatus complete", "path", path, "count", len(media), "failed", lsarFailed, "timeout", isTimeout)
	return media, lsarFailed, isTimeout
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
