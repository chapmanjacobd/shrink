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
	"strconv"
	"strings"

	"github.com/chapmanjacobd/shrink/internal/utils"
)

// FFmpegProcessor handles all FFmpeg-related operations
type FFmpegProcessor struct {
	config *ProcessorConfig
}

// NewFFmpegProcessor creates a new FFmpeg processor
func NewFFmpegProcessor(cfg *ProcessorConfig) *FFmpegProcessor {
	return &FFmpegProcessor{config: cfg}
}

// Process executes FFmpeg transcoding for audio/video files
func (p *FFmpegProcessor) Process(ctx context.Context, m *ShrinkMedia, cfg *ProcessorConfig) ProcessResult {
	// Check if FFmpeg is available
	if !utils.CommandExists("ffmpeg") {
		return ProcessResult{SourcePath: m.Path, Error: fmt.Errorf("ffmpeg not installed")}
	}

	// Probe the file
	probe, err := p.ffprobe(m.Path)
	if err != nil {
		return ProcessResult{SourcePath: m.Path, Error: err}
	}

	// Check for streams
	if len(probe.Streams) == 0 {
		slog.Error("No media streams", "path", m.Path)
		return ProcessResult{SourcePath: m.Path, Error: fmt.Errorf("no media streams")}
	}

	// Handle animated images (GIF/webp with audio)
	if m.Ext == ".gif" || m.Ext == ".webp" {
		isAnimation := p.isAnimationFromProbe(probe)
		if isAnimation == nil {
			return ProcessResult{SourcePath: m.Path, Error: fmt.Errorf("could not determine animation status")}
		}
		if !*isAnimation {
			// Process as static image
			imgProc := NewImageProcessor()
			return imgProc.Process(ctx, m, cfg)
		}
	}

	// Get streams
	videoStream := getFirstStream(probe.VideoStreams)
	audioStream := getFirstStream(probe.AudioStreams)
	subtitleStream := getFirstStream(probe.SubtitleStreams)
	albumArtStream := getFirstStream(probe.AlbumArtStreams)

	// Stream validation - skip files without expected streams
	if videoStream == nil && cfg.VideoOnly {
		return ProcessResult{SourcePath: m.Path, Success: true}
	}

	if audioStream == nil && cfg.AudioOnly {
		return ProcessResult{SourcePath: m.Path, Success: true}
	}

	// Check if already encoded optimally
	if videoStream != nil && videoStream.CodecName == "av1" {
		slog.Info("Already AV1", "path", m.Path)
		return ProcessResult{SourcePath: m.Path, Success: true, Outputs: []ProcessOutputFile{{Path: m.Path, Size: m.Size}}}
	}
	if audioStream != nil && audioStream.CodecName == "opus" && videoStream == nil {
		slog.Info("Already Opus", "path", m.Path)
		return ProcessResult{SourcePath: m.Path, Success: true, Outputs: []ProcessOutputFile{{Path: m.Path, Size: m.Size}}}
	}

	// Determine output suffix
	var outputSuffix string
	if videoStream != nil && !cfg.AudioOnly {
		outputSuffix = ".av1.mkv"
	} else if audioStream != nil {
		outputSuffix = ".mka"
	} else {
		outputSuffix = ".gif.mkv"
	}

	outputPath := genOutputPath(m.Path, outputSuffix)

	// Build and execute FFmpeg command
	args := p.buildFFmpegArgs(m.Path, outputPath, probe, videoStream, audioStream, subtitleStream, albumArtStream)
	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		// Check for timeout
		if ctx.Err() == context.DeadlineExceeded {
			slog.Error("FFmpeg timed out", "path", m.Path, "error", err, "output", string(output))
		} else {
			slog.Error("FFmpeg failed", "path", m.Path, "error", err, "output", string(output))
		}
		// Categorize FFmpeg errors
		errorLog := strings.Split(string(output), "\n")
		isUnsupported := p.isUnsupportedError(errorLog)
		isFileError := p.isFileError(errorLog)
		isEnvError := p.isEnvironmentError(errorLog)

		if isEnvError {
			// Environment errors should be re-raised (they're not file-specific)
			return ProcessResult{SourcePath: m.Path, Error: fmt.Errorf("ffmpeg environment error: %w", err)}
		} else if isUnsupported {
			// Unsupported codec/format - remove transcode attempt and return original
			os.Remove(outputPath)
			slog.Info("Unsupported format, keeping original", "path", m.Path)
			return ProcessResult{SourcePath: m.Path, Success: true, Outputs: []ProcessOutputFile{{Path: m.Path, Size: m.Size}}}
		} else if isFileError {
			// File-specific error - continue processing (may be recoverable)
			slog.Warn("FFmpeg file error", "output", string(output), "path", m.Path)
		} else {
			slog.Error("FFmpeg error", "output", string(output), "path", m.Path)
		}

		if p.config.DeleteUnplayable {
			return ProcessResult{SourcePath: m.Path, Success: false, Error: err}
		}
		return ProcessResult{SourcePath: m.Path, Error: err}
	}

	// Validate transcode (may return multiple results if splitting was used)
	return p.validateTranscode(*m, outputPath, probe)
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

// buildFFmpegArgs constructs the FFmpeg command arguments
func (p *FFmpegProcessor) buildFFmpegArgs(inputPath, outputPath string, probe *FFProbeResult,
	videoStream, audioStream, subtitleStream, albumArtStream *FFProbeStream,
) []string {
	// Build base command
	logLevel := []string{"-hide_banner", "-loglevel", "warning"}
	if p.config.VerboseFFmpeg {
		logLevel = []string{"-v", "9", "-loglevel", "99"}
	}

	args := []string{
		"-nostdin", "-y",
	}
	args = append(args, logLevel...)
	args = append(args,
		"-i", inputPath,
	)

	// Video options
	if videoStream != nil && !p.config.AudioOnly {
		args = append(args, "-map", fmt.Sprintf("0:%d", videoStream.Index))

		if p.config.Keyframes {
			args = append(args, "-c:v", "copy", "-bsf:v", "noise=drop=not(key)")
		} else {
			args = append(args,
				"-c:v", "libsvtav1",
				"-preset", p.config.Preset,
				"-crf", p.config.CRF,
				"-pix_fmt", "yuv420p10le",
				"-svtav1-params", "tune=0:enable-overlays=1",
			)

			// Build video filters
			filters := p.buildVideoFilters(probe, videoStream)
			if len(filters) > 0 {
				args = append(args, "-vf", strings.Join(filters, ","))
			}
		}
	} else if albumArtStream != nil {
		args = append(args, "-map", fmt.Sprintf("0:%d", albumArtStream.Index), "-c:v", "copy")
	}

	// Audio options
	if audioStream != nil {
		args = append(args, "-map", fmt.Sprintf("0:%d", audioStream.Index))
		args = append(args, p.buildAudioOptions(audioStream)...)

		// Silence detection for splitting
		isSplit := p.config.AlwaysSplit || (videoStream == nil && p.config.SplitLongerThan > 0 && probe.Duration > p.config.SplitLongerThan)
		if isSplit {
			splits := p.detectSilence(inputPath)
			if len(splits) > 0 {
				segmentTimes := strings.Join(splits, ",")
				args = append(args, "-f", "segment", "-segment_times", segmentTimes)
				outputPath = strings.Replace(outputPath, filepath.Ext(outputPath), ".%03d"+filepath.Ext(outputPath), 1)
			}
		}
	}

	// Subtitle options
	if subtitleStream != nil {
		args = append(args, p.buildSubtitleOptions(subtitleStream)...)
	}

	// Timecode streams
	if p.config.IncludeTimecode {
		args = append(args, "-map", "0:t")
	}

	// Metadata and global flags (matching Python order)
	args = append(args,
		"-movflags", "use_metadata_tags",
		"-map_metadata", "0",
		"-map_chapters", "0",
		"-dn",
		"-max_interleave_delta", "0",
	)

	args = append(args, outputPath)
	return args
}

// buildVideoFilters constructs video filter chain
func (p *FFmpegProcessor) buildVideoFilters(probe *FFProbeResult, stream *FFProbeStream) []string {
	var filters []string

	// FPS correction
	fps := parseFPS(probe)
	if fps > 240 {
		frames := p.countFrames(probe.Path)
		if frames > 0 && probe.Duration > 0 {
			actualFPS := float64(frames) / probe.Duration
			filters = append(filters, fmt.Sprintf("fps=%f", actualFPS))
		}
	}

	// Scaling and stereo mode
	stereoMode := p.detectStereoMode(stream)
	filters = append(filters, p.buildScaleFilter(stereoMode, stream.Width, stream.Height)...)

	return filters
}

// buildAudioOptions constructs audio encoding options
func (p *FFmpegProcessor) buildAudioOptions(stream *FFProbeStream) []string {
	var args []string

	channels := stream.Channels
	if channels == 0 {
		channels = 2
	}

	bitrate := parseBitrate(stream.BitRate)
	if bitrate == 0 {
		bitrate = 256000
	}

	sampleRate := parseSampleRate(stream.SampleRate)
	if sampleRate == 0 {
		sampleRate = 44100
	}

	// Channel config
	if channels == 1 {
		args = append(args, "-ac", "1")
	} else {
		args = append(args, "-ac", "2")
	}

	// Bitrate config
	if bitrate >= 256000 {
		args = append(args, "-b:a", "128k")
	} else {
		args = append(args, "-b:a", "64k", "-frame_duration", "40", "-apply_phase_inv", "0")
	}

	// Sample rate config
	var opusRate int
	if sampleRate >= 44100 {
		opusRate = 48000
	} else if sampleRate >= 22050 {
		opusRate = 24000
	} else {
		opusRate = 16000
	}

	args = append(args,
		"-c:a", "libopus",
		"-ar", strconv.Itoa(opusRate),
		"-af", "loudnorm=i=-18:tp=-3:lra=17",
	)

	return args
}

// buildSubtitleOptions constructs subtitle encoding options
func (p *FFmpegProcessor) buildSubtitleOptions(stream *FFProbeStream) []string {
	codec := strings.ToLower(stream.CodecName)
	idx := stream.Index

	// Comprehensive subtitle codec lists (matching Python implementation)
	textSubs := map[string]bool{
		"ass": true, "ssa": true, "subrip": true, "srt": true, "mov_text": true,
		"webvtt": true, "text": true, "utf8": true, "arib_caption": true,
		"libaribcaption": true, "libaribb24": true, "libzvbi_teletextdec": true,
		"dvb_teletext": true, "cc_dec": true, "jacosub": true, "microdvd": true,
		"mpl2": true, "pjs": true, "realtext": true, "sami": true, "stl": true,
		"subviewer": true, "subviewer1": true, "vplayer": true,
	}
	imageSubs := map[string]bool{
		"dvd_subtitle": true, "dvdsub": true, "pgssub": true,
		"hdmv_pgs_subtitle": true, "xsub": true, "dvb_subtitle": true, "dvbsub": true,
	}
	mkvTextSubs := map[string]bool{"subrip": true, "srt": true, "ass": true, "ssa": true, "webvtt": true}
	mkvImageSubs := map[string]bool{"pgssub": true, "hdmv_pgs_subtitle": true, "dvd_subtitle": true, "vobsub": true}

	if mkvTextSubs[codec] || mkvImageSubs[codec] {
		// Already in MKV-compatible format, copy as-is
		return []string{"-map", fmt.Sprintf("0:%d", idx), "-c:s", "copy"}
	} else if textSubs[codec] {
		// Convert text subtitles to SRT
		return []string{"-map", fmt.Sprintf("0:%d", idx), "-c:s", "srt"}
	} else if imageSubs[codec] {
		// Convert image subtitles to PGS
		return []string{"-map", fmt.Sprintf("0:%d", idx), "-c:s", "pgssub"}
	}

	// Unknown codec - log warning and skip
	slog.Warn("Unknown subtitle codec, skipping", "codec", codec, "index", idx)
	return nil
}

// buildScaleFilter constructs scaling filter based on stereo mode
func (p *FFmpegProcessor) buildScaleFilter(stereoMode string, width, height int) []string {
	var filters []string

	switch stereoMode {
	case "sbs":
		perEyeWidth := width / 2
		targetEyeWidth := p.config.MaxVideoWidth
		targetTotalWidth := targetEyeWidth * 2

		if float64(perEyeWidth) > float64(targetEyeWidth)*(1+p.config.MaxWidthBuffer) {
			filters = append(filters, fmt.Sprintf("scale=%d:-2", targetTotalWidth))
		} else if float64(height) > float64(p.config.MaxVideoHeight)*(1+p.config.MaxHeightBuffer) {
			filters = append(filters, fmt.Sprintf("scale=-2:%d", p.config.MaxVideoHeight))
		} else {
			filters = append(filters, "pad=trunc((iw+1)/2)*2:trunc((ih+1)/2)*2")
		}
	case "ou":
		perEyeHeight := height / 2
		targetEyeHeight := p.config.MaxVideoHeight
		targetTotalHeight := targetEyeHeight * 2

		if float64(perEyeHeight) > float64(targetEyeHeight)*(1+p.config.MaxHeightBuffer) {
			filters = append(filters, fmt.Sprintf("scale=-2:%d", targetTotalHeight))
		} else if float64(width) > float64(p.config.MaxVideoWidth)*(1+p.config.MaxWidthBuffer) {
			filters = append(filters, fmt.Sprintf("scale=%d:-2", p.config.MaxVideoWidth))
		} else {
			filters = append(filters, "pad=trunc((iw+1)/2)*2:trunc((ih+1)/2)*2")
		}
	default:
		if float64(width) > float64(p.config.MaxVideoWidth)*(1+p.config.MaxWidthBuffer) {
			filters = append(filters, fmt.Sprintf("scale=%d:-2", p.config.MaxVideoWidth))
		} else if float64(height) > float64(p.config.MaxVideoHeight)*(1+p.config.MaxHeightBuffer) {
			filters = append(filters, fmt.Sprintf("scale=-2:%d", p.config.MaxVideoHeight))
		} else {
			filters = append(filters, "pad=trunc((iw+1)/2)*2:trunc((ih+1)/2)*2")
		}
	}

	return filters
}

// validateTranscode validates the transcoded output
func (p *FFmpegProcessor) validateTranscode(m ShrinkMedia, outputPath string, originalProbe *FFProbeResult) ProcessResult {
	// Check if output is a split file pattern (contains %03d)
	isSplit := strings.Contains(outputPath, "%03d")

	if !isSplit {
		// Single file output - original logic
		if _, err := os.Stat(outputPath); os.IsNotExist(err) {
			return ProcessResult{SourcePath: m.Path, Success: false}
		}

		outputStats, err := os.Stat(outputPath)
		if err != nil {
			return ProcessResult{SourcePath: m.Path, Success: false}
		}

		deleteTranscode := false
		// Check for invalid transcode
		if outputStats.Size() == 0 {
			deleteTranscode = true
		} else {
			// Validate duration and dimensions
			transcodeProbe, err := p.ffprobe(outputPath)
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
				// Check duration matches original (within 5% tolerance)
				// Skip this check for archives and text since extracted/converted contents may have different duration
				isArchive := utils.ArchiveExtensionMap[m.Ext]
				isText := utils.TextExtensionMap[m.Ext]
				if !isArchive && !isText {
					diff := math.Abs(originalProbe.Duration-transcodeProbe.Duration) / originalProbe.Duration * 100
					if diff > 5.0 {
						deleteTranscode = true
					}
				}
			}
		}

		if deleteTranscode {
			os.Remove(outputPath)
			return ProcessResult{SourcePath: m.Path, Success: false}
		}

		return ProcessResult{
			SourcePath: m.Path,
			Outputs:    []ProcessOutputFile{{Path: outputPath, Size: outputStats.Size()}},
			Success:    true,
		}
	}

	// Split file output - find all generated files
	pattern := strings.Replace(outputPath, "%03d", "*", 1)
	matches, err := filepath.Glob(pattern)
	if err != nil || len(matches) == 0 {
		return ProcessResult{SourcePath: m.Path, Error: fmt.Errorf("no split files found")}
	}

	// Validate each split file
	var totalNewSize int64
	var hasInvalidFile bool
	var validFiles []ProcessOutputFile

	for _, match := range matches {
		stats, err := os.Stat(match)
		if err != nil || stats.Size() == 0 {
			hasInvalidFile = true
			break
		}
		// Validate dimensions for video split files
		probe, err := p.ffprobe(match)
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
		validFiles = append(validFiles, ProcessOutputFile{Path: match, Size: stats.Size()})
	}

	if hasInvalidFile {
		// Clean up all split files
		for _, match := range matches {
			os.Remove(match)
		}
		return ProcessResult{SourcePath: m.Path, Error: fmt.Errorf("invalid split file")}
	}

	return ProcessResult{
		SourcePath: m.Path,
		Outputs:    validFiles,
		Success:    true,
	}
}

// ffprobe probes a media file and returns metadata
func (p *FFmpegProcessor) ffprobe(path string) (*FFProbeResult, error) {
	cmd := exec.Command("ffprobe",
		"-v", "quiet",
		"-print_format", "json",
		"-show_format",
		"-show_streams",
		path)
	cmd.Stdin = nil

	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	var probe FFProbeResult
	if err := json.Unmarshal(output, &probe); err != nil {
		return nil, err
	}

	probe.Path = path

	// Parse duration from format
	if probe.Format.Duration != "" {
		probe.Duration, _ = strconv.ParseFloat(probe.Format.Duration, 64)
	}

	// Categorize streams
	for _, s := range probe.Streams {
		switch s.CodecType {
		case "video":
			if s.Width == 0 && s.Height == 0 {
				probe.AlbumArtStreams = append(probe.AlbumArtStreams, s)
			} else {
				probe.VideoStreams = append(probe.VideoStreams, s)
			}
		case "audio":
			probe.AudioStreams = append(probe.AudioStreams, s)
		case "subtitle":
			probe.SubtitleStreams = append(probe.SubtitleStreams, s)
		}
	}

	return &probe, nil
}

// Helper functions

func (p *FFmpegProcessor) isAnimationFromProbe(probe *FFProbeResult) *bool {
	if len(probe.AudioStreams) > 0 {
		result := true
		return &result
	}

	for _, s := range probe.VideoStreams {
		frames := parseInt(s.NbFrames)
		if frames == 0 {
			cmd := exec.Command("ffprobe",
				"-v", "error",
				"-count_frames",
				"-select_streams", "v:0",
				"-show_entries", "stream=nb_read_frames",
				"-of", "default=nokey=1:noprint_wrappers=1",
				probe.Path)
			cmd.Stdin = nil
			output, err := cmd.Output()
			if err == nil {
				frames = parseInt(strings.TrimSpace(string(output)))
			}
		}
		if frames > 1 {
			result := true
			return &result
		}
	}

	result := false
	return &result
}

func (p *FFmpegProcessor) countFrames(path string) int {
	cmd := exec.Command("ffprobe",
		"-v", "fatal",
		"-select_streams", "v:0",
		"-count_frames",
		"-show_entries", "stream=nb_read_frames",
		"-of", "default=noprint_wrappers=1:nokey=1",
		path)
	cmd.Stdin = nil
	output, err := cmd.Output()
	if err != nil {
		return 0
	}
	return parseInt(strings.TrimSpace(string(output)))
}

func (p *FFmpegProcessor) detectSilence(path string) []string {
	args := []string{
		"-nostdin",
		"-hide_banner", "-v", "warning",
		"-i", path,
		"-af", "silencedetect=-55dB:d=0.3,ametadata=mode=print:file=-:key=lavfi.silence_start",
		"-vn", "-sn", "-f", "s16le", "-y", "/dev/null",
	}

	output, _ := exec.Command("ffmpeg", args...).CombinedOutput()
	lines := strings.Split(string(output), "\n")

	var splits []string
	var prev float64
	for _, line := range lines {
		if strings.Contains(line, "lavfi.silence_start") {
			parts := strings.Split(line, "=")
			if len(parts) > 1 {
				t, _ := strconv.ParseFloat(parts[1], 64)
				if t-prev >= p.config.MinSplitSegment {
					splits = append(splits, fmt.Sprintf("%.2f", t))
					prev = t
				}
			}
		}
	}
	return splits
}

func (p *FFmpegProcessor) detectStereoMode(stream *FFProbeStream) string {
	tags := stream.Tags
	if tags != nil {
		stereoMode := strings.ToLower(tags["stereo_mode"])
		switch stereoMode {
		case "left_right", "side_by_side", "sbs":
			return "sbs"
		case "top_bottom", "over_under", "tb", "ou":
			return "ou"
		}
	}

	aspectRatio := float64(stream.Width) / float64(stream.Height)
	if 1.9 <= aspectRatio && aspectRatio <= 2.1 && stream.Width >= 4500 {
		return "sbs"
	}
	if 0.9 <= aspectRatio && aspectRatio <= 1.1 && stream.Height >= 2600 {
		return "ou"
	}
	return ""
}

// FFProbeResult represents ffprobe JSON output
type FFProbeResult struct {
	Streams         []FFProbeStream `json:"streams"`
	Format          FFProbeFormat   `json:"format"`
	Path            string
	Duration        float64
	VideoStreams    []FFProbeStream
	AudioStreams    []FFProbeStream
	SubtitleStreams []FFProbeStream
	AlbumArtStreams []FFProbeStream
}

// FFProbeStream represents a stream in ffprobe output
type FFProbeStream struct {
	Index      int               `json:"index"`
	CodecType  string            `json:"codec_type"`
	CodecName  string            `json:"codec_name"`
	Duration   string            `json:"duration"`
	NbFrames   string            `json:"nb_frames"`
	Width      int               `json:"width"`
	Height     int               `json:"height"`
	RFrameRate string            `json:"r_frame_rate"`
	BitRate    string            `json:"bit_rate"`
	SampleRate string            `json:"sample_rate"`
	Channels   int               `json:"channels"`
	Tags       map[string]string `json:"tags"`
}

// FFProbeFormat represents the format section of ffprobe output
type FFProbeFormat struct {
	Duration  string `json:"duration"`
	BitRate   string `json:"bit_rate"`
	NbStreams int    `json:"nb_streams"`
}

// Helper functions
func getFirstStream(streams []FFProbeStream) *FFProbeStream {
	if len(streams) == 0 {
		return nil
	}
	return &streams[0]
}

func parseFPS(probe *FFProbeResult) float64 {
	if len(probe.VideoStreams) == 0 {
		return 0
	}
	stream := probe.VideoStreams[0]
	parts := strings.Split(stream.RFrameRate, "/")
	if len(parts) != 2 {
		return 0
	}
	num, _ := strconv.ParseFloat(parts[0], 64)
	den, _ := strconv.ParseFloat(parts[1], 64)
	if den == 0 {
		return 0
	}
	return num / den
}

func parseBitrate(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

func parseSampleRate(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

func parseInt(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

func genOutputPath(path, suffix string) string {
	dir := filepath.Dir(path)
	base := filepath.Base(path)
	name := strings.TrimSuffix(base, filepath.Ext(base))
	return filepath.Join(dir, name+suffix)
}
