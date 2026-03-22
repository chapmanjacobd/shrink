// Package ffmpeg provides wrappers for FFmpeg and FFprobe operations.
package ffmpeg

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/chapmanjacobd/shrink/internal/models"
	"github.com/chapmanjacobd/shrink/internal/utils"
)

// FFmpegProcessor handles all FFmpeg-related operations
type FFmpegProcessor struct {
	config *models.ProcessorConfig
}

// NewFFmpegProcessor creates a new FFmpeg processor
func NewFFmpegProcessor(cfg *models.ProcessorConfig) *FFmpegProcessor {
	return &FFmpegProcessor{config: cfg}
}

// Process executes FFmpeg transcoding for audio/video files
func (p *FFmpegProcessor) Process(ctx context.Context, m *models.ShrinkMedia, cfg *models.ProcessorConfig, registry models.ProcessorRegistry) models.ProcessResult {
	ffmpeg := utils.GetCommandPath("ffmpeg")
	if ffmpeg == "" {
		return models.ProcessResult{SourcePath: m.Path, Error: fmt.Errorf("ffmpeg not installed")}
	}

	// Probe the file
	probe, err := ProbeMedia(m.Path)
	if err != nil {
		return models.ProcessResult{SourcePath: m.Path, Error: err}
	}

	// Check for streams
	if len(probe.Streams) == 0 {
		slog.Error("No media streams", "path", m.Path)
		return models.ProcessResult{SourcePath: m.Path, Error: fmt.Errorf("no media streams")}
	}

	// Handle animated images (GIF/webp with audio)
	if m.Ext == ".gif" || m.Ext == ".webp" {
		isAnimation := p.isAnimationFromProbe(probe)
		if isAnimation == nil {
			return models.ProcessResult{SourcePath: m.Path, Error: fmt.Errorf("could not determine animation status")}
		}
		if !*isAnimation {
			// Process as static image
			m.Category = "Image"
			processor := registry.GetProcessor(m)
			if processor != nil {
				return processor.Process(ctx, m, cfg, registry)
			}
			return models.ProcessResult{SourcePath: m.Path, Error: fmt.Errorf("no processor found for image")}
		}
	}

	// Get streams
	videoStream := getFirstStream(probe.VideoStreams)
	audioStream := getFirstStream(probe.AudioStreams)
	albumArtStream := getFirstStream(probe.AlbumArtStreams)

	// Stream validation - skip files without expected streams
	if videoStream == nil && cfg.Video.VideoOnly {
		return models.ProcessResult{SourcePath: m.Path, Success: true}
	}

	if audioStream == nil && cfg.Audio.AudioOnly {
		return models.ProcessResult{SourcePath: m.Path, Success: true}
	}

	// Check if already encoded optimally by codec
	if videoStream != nil && videoStream.CodecName == "av1" {
		return models.ProcessResult{SourcePath: m.Path, Success: true, Outputs: []models.ProcessOutputFile{{Path: m.Path, Size: m.Size}}}
	}
	if audioStream != nil && audioStream.CodecName == "opus" && videoStream == nil {
		return models.ProcessResult{SourcePath: m.Path, Success: true, Outputs: []models.ProcessOutputFile{{Path: m.Path, Size: m.Size}}}
	}

	// Determine output suffix
	var outputSuffix string
	if videoStream != nil && !cfg.Audio.AudioOnly {
		outputSuffix = ".av1.mkv"
	} else if audioStream != nil {
		outputSuffix = ".mka"
	} else {
		outputSuffix = ".gif.mkv"
	}

	outputPath := genOutputPath(m.Path, outputSuffix)

	// FFmpeg cannot edit existing files in-place
	if m.Path == outputPath {
		slog.Info("Input and output paths are identical, skipping", "path", m.Path)
		return models.ProcessResult{SourcePath: m.Path, Success: true, Outputs: []models.ProcessOutputFile{{Path: m.Path, Size: m.Size}}}
	}

	// Build and execute FFmpeg command
	args := p.buildFFmpegArgs(m.Path, outputPath, probe, videoStream, audioStream, albumArtStream, probe.SubtitleStreams)
	cmd := exec.CommandContext(ctx, ffmpeg, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		// Clean up any incomplete output file
		os.Remove(outputPath)

		// Categorize FFmpeg errors
		errorLog := strings.Split(string(output), "\n")
		isUnsupported := p.isUnsupportedError(errorLog)
		isFileError := p.isFileError(errorLog)
		isEnvError := p.isEnvironmentError(errorLog)

		if isEnvError {
			// Environment errors should be re-raised (they're not file-specific)
			return models.ProcessResult{SourcePath: m.Path, Error: fmt.Errorf("ffmpeg environment error: %w", err)}
		} else if isUnsupported {
			// Unsupported codec/format - remove transcode attempt and return original
			os.Remove(outputPath)
			slog.Info("Unsupported format, keeping original", "path", m.Path)
			return models.ProcessResult{SourcePath: m.Path, Success: true, Outputs: []models.ProcessOutputFile{{Path: m.Path, Size: m.Size}}}
		} else if isFileError {
			// File-specific error - continue processing (may be recoverable)
			slog.Warn("FFmpeg file error", "output", string(output), "path", m.Path)
		} else {
			slog.Error("FFmpeg error", "output", string(output), "path", m.Path)
		}

		if p.config.Common.DeleteUnplayable {
			return models.ProcessResult{SourcePath: m.Path, Success: false, Error: err}
		}
		return models.ProcessResult{SourcePath: m.Path, Error: err}
	}

	// Validate transcode (may return multiple results if splitting was used)
	return p.validateTranscode(*m, outputPath, probe)
}

// buildFFmpegArgs constructs the FFmpeg command arguments
func (p *FFmpegProcessor) buildFFmpegArgs(inputPath, outputPath string, probe *FFProbeResult,
	videoStream, audioStream, albumArtStream *FFProbeStream, subtitleStreams []FFProbeStream,
) []string {
	// Build base command
	logLevel := []string{"-hide_banner", "-loglevel", "warning"}
	if p.config.Common.VerboseFFmpeg {
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
	if videoStream != nil && !p.config.Audio.AudioOnly {
		args = append(args, "-map", fmt.Sprintf("0:%d", videoStream.Index))

		if p.config.Video.Keyframes {
			args = append(args, "-c:v", "copy", "-bsf:v", "noise=drop=not(key)")
		} else {
			args = append(args,
				"-c:v", "libsvtav1",
				"-preset", p.config.Video.Preset,
				"-crf", p.config.Video.CRF,
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
		isSplit := p.config.Audio.AlwaysSplit || (videoStream == nil && p.config.Audio.SplitLongerThan > 0 && probe.Duration > p.config.Audio.SplitLongerThan)
		if isSplit {
			splits := p.detectSilence(inputPath)
			if len(splits) > 0 {
				segmentTimes := strings.Join(splits, ",")
				args = append(args, "-f", "segment", "-segment_times", segmentTimes)
				outputPath = strings.Replace(outputPath, filepath.Ext(outputPath), ".%03d"+filepath.Ext(outputPath), 1)
			}
		}
	}

	// Subtitle options - map all subtitle streams
	args = append(args, p.buildSubtitleOptions(probe.SubtitleStreams)...)

	// Timecode streams
	if p.config.Common.IncludeTimecode {
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

// buildSubtitleOptions constructs subtitle encoding options for all subtitle streams
func (p *FFmpegProcessor) buildSubtitleOptions(subtitleStreams []FFProbeStream) []string {
	if len(subtitleStreams) == 0 {
		return nil
	}

	var args []string

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

	for _, stream := range subtitleStreams {
		codec := strings.ToLower(stream.CodecName)
		idx := stream.Index

		if mkvTextSubs[codec] || mkvImageSubs[codec] {
			// Already in MKV-compatible format, copy as-is
			args = append(args, "-map", fmt.Sprintf("0:%d", idx), "-c:s", "copy")
		} else if textSubs[codec] {
			// Convert text subtitles to SRT
			args = append(args, "-map", fmt.Sprintf("0:%d", idx), "-c:s", "srt")
		} else if imageSubs[codec] {
			// Convert image subtitles to PGS
			args = append(args, "-map", fmt.Sprintf("0:%d", idx), "-c:s", "pgssub")
		} else {
			// Unknown codec - log warning and skip
			slog.Warn("Unknown subtitle codec, skipping", "codec", codec, "index", idx)
		}
	}

	return args
}

// buildScaleFilter constructs scaling filter based on stereo mode
func (p *FFmpegProcessor) buildScaleFilter(stereoMode string, width, height int) []string {
	var filters []string

	switch stereoMode {
	case "sbs":
		perEyeWidth := width / 2
		targetEyeWidth := p.config.Video.MaxVideoWidth
		targetTotalWidth := targetEyeWidth * 2

		if float64(perEyeWidth) > float64(targetEyeWidth)*(1+p.config.Common.MaxWidthBuffer) {
			filters = append(filters, fmt.Sprintf("scale=%d:-2", targetTotalWidth))
		} else if float64(height) > float64(p.config.Video.MaxVideoHeight)*(1+p.config.Common.MaxHeightBuffer) {
			filters = append(filters, fmt.Sprintf("scale=-2:%d", p.config.Video.MaxVideoHeight))
		} else {
			filters = append(filters, "pad=trunc((iw+1)/2)*2:trunc((ih+1)/2)*2")
		}
	case "ou":
		perEyeHeight := height / 2
		targetEyeHeight := p.config.Video.MaxVideoHeight
		targetTotalHeight := targetEyeHeight * 2

		if float64(perEyeHeight) > float64(targetEyeHeight)*(1+p.config.Common.MaxHeightBuffer) {
			filters = append(filters, fmt.Sprintf("scale=-2:%d", targetTotalHeight))
		} else if float64(width) > float64(p.config.Video.MaxVideoWidth)*(1+p.config.Common.MaxWidthBuffer) {
			filters = append(filters, fmt.Sprintf("scale=%d:-2", p.config.Video.MaxVideoWidth))
		} else {
			filters = append(filters, "pad=trunc((iw+1)/2)*2:trunc((ih+1)/2)*2")
		}
	default:
		if float64(width) > float64(p.config.Video.MaxVideoWidth)*(1+p.config.Common.MaxWidthBuffer) {
			filters = append(filters, fmt.Sprintf("scale=%d:-2", p.config.Video.MaxVideoWidth))
		} else if float64(height) > float64(p.config.Video.MaxVideoHeight)*(1+p.config.Common.MaxHeightBuffer) {
			filters = append(filters, fmt.Sprintf("scale=-2:%d", p.config.Video.MaxVideoHeight))
		} else {
			filters = append(filters, "pad=trunc((iw+1)/2)*2:trunc((ih+1)/2)*2")
		}
	}

	return filters
}

// Helper functions

func (p *FFmpegProcessor) detectSilence(path string) []string {
	ffmpeg := utils.GetCommandPath("ffmpeg")
	if ffmpeg == "" {
		return nil
	}
	args := []string{
		"-nostdin",
		"-hide_banner", "-v", "warning",
		"-i", path,
		"-af", "silencedetect=-55dB:d=0.3,ametadata=mode=print:file=-:key=lavfi.silence_start",
		"-vn", "-sn", "-f", "s16le", "-y", "/dev/null",
	}

	output, err := exec.Command(ffmpeg, args...).CombinedOutput()
	if err != nil {
		slog.Warn("Silence detection failed", "path", path, "error", err)
		return nil
	}
	lines := strings.Split(string(output), "\n")

	var splits []string
	var prev float64
	for _, line := range lines {
		if strings.Contains(line, "lavfi.silence_start") {
			parts := strings.Split(line, "=")
			if len(parts) > 1 {
				t, err := strconv.ParseFloat(parts[1], 64)
				if err == nil && t-prev >= p.config.Audio.MinSplitSegment {
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

// Helper functions
func getFirstStream(streams []FFProbeStream) *FFProbeStream {
	if len(streams) == 0 {
		return nil
	}
	return &streams[0]
}

func parseBitrate(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return n
}

func parseSampleRate(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return n
}

func parseInt(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return n
}

func genOutputPath(path, suffix string) string {
	dir := filepath.Dir(path)
	base := filepath.Base(path)
	name := strings.TrimSuffix(base, filepath.Ext(base))
	return filepath.Join(dir, name+suffix)
}
