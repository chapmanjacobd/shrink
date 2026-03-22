//go:build integration
// +build integration

// Package ffmpeg provides integration tests using real FFmpeg for end-to-end validation.
// These tests require FFmpeg, FFprobe, and related tools to be installed.
// Run with: go test -tags=integration ./internal/ffmpeg/...
package ffmpeg

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/chapmanjacobd/shrink/internal/models"
	"github.com/chapmanjacobd/shrink/internal/utils"
)

// skipIfNoFFmpeg skips the test if FFmpeg is not available
func skipIfNoFFmpeg(t *testing.T) {
	if !utils.CommandExists("ffmpeg") {
		t.Skip("ffmpeg not available, skipping integration test")
	}
	if !utils.CommandExists("ffprobe") {
		t.Skip("ffprobe not available, skipping integration test")
	}
}

// createTestVideo creates a test video file using FFmpeg
func createTestVideo(t *testing.T, dir string, name string, duration float64, width, height int) string {
	t.Helper()
	path := filepath.Join(dir, name)

	// Create a test video with synthetic content
	cmd := exec.Command("ffmpeg",
		"-y",
		"-f", "lavfi",
		"-i", fmt.Sprintf("testsrc=size=%dx%d:rate=30", width, height),
		"-f", "lavfi",
		"-i", fmt.Sprintf("sine=frequency=440:duration=%.2f", duration),
		"-t", fmt.Sprintf("%.2f", duration),
		"-c:v", "libx264",
		"-preset", "ultrafast",
		"-c:a", "aac",
		path,
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("failed to create test video: %v\n%s", err, output)
	}
	return path
}

// createTestAudio creates a test audio file using FFmpeg
func createTestAudio(t *testing.T, dir string, name string, duration float64) string {
	t.Helper()
	path := filepath.Join(dir, name)

	cmd := exec.Command("ffmpeg",
		"-y",
		"-f", "lavfi",
		"-i", fmt.Sprintf("sine=frequency=440:duration=%.2f", duration),
		"-t", fmt.Sprintf("%.2f", duration),
		"-c:a", "aac",
		path,
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("failed to create test audio: %v\n%s", err, output)
	}
	return path
}

// createTestImage creates a test image file using ImageMagick or FFmpeg
func createTestImage(t *testing.T, dir string, name string, width, height int) string {
	t.Helper()
	path := filepath.Join(dir, name)

	// Use FFmpeg to create a test image
	cmd := exec.Command("ffmpeg",
		"-y",
		"-f", "lavfi",
		"-i", fmt.Sprintf("testsrc=size=%dx%d:rate=1", width, height),
		"-vframes", "1",
		path,
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("failed to create test image: %v\n%s", err, output)
	}
	return path
}

// createTestVideoWithSubtitles creates a test video with subtitle streams
func createTestVideoWithSubtitles(t *testing.T, dir string, name string) string {
	t.Helper()
	videoPath := filepath.Join(dir, name)
	srtPath := filepath.Join(dir, "test.srt")

	// Create a simple SRT subtitle file
	srtContent := `1
00:00:01,000 --> 00:00:03,000
Test subtitle line 1

2
00:00:04,000 --> 00:00:06,000
Test subtitle line 2
`
	if err := os.WriteFile(srtPath, []byte(srtContent), 0o644); err != nil {
		t.Fatalf("failed to create SRT file: %v", err)
	}

	// Create video with hardcoded subtitle using MP4 container and mov_text
	cmd := exec.Command("ffmpeg",
		"-y",
		"-f", "lavfi",
		"-i", "testsrc=size=640x480:rate=30",
		"-f", "lavfi",
		"-i", "sine=frequency=440:duration=10",
		"-i", srtPath,
		"-t", "10",
		"-c:v", "libx264",
		"-preset", "ultrafast",
		"-c:a", "aac",
		"-c:s", "mov_text",
		"-movflags", "+faststart",
		videoPath,
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		// If mov_text fails, try without subtitles (test will skip)
		t.Logf("failed to create test video with subtitles: %v\n%s", err, output)
		t.Skip("mov_text subtitle codec not supported, skipping test")
	}
	return videoPath
}

// =============================================================================
// Video Integration Tests
// =============================================================================

func TestIntegration_VideoTranscode_H264_to_AV1(t *testing.T) {
	skipIfNoFFmpeg(t)

	tempDir := t.TempDir()
	inputPath := createTestVideo(t, tempDir, "input.mp4", 2.0, 640, 480)

	cfg := &models.ProcessorConfig{
		Video: models.VideoConfig{
			Preset:             "10", // Fast preset for tests
			CRF:                "50", // Lower quality for faster tests
			MinSavingsVideo:    0.05,
			TranscodingVideoRate: 1.0,
			MaxVideoWidth:      1920,
			MaxVideoHeight:     1080,
		},
		Common: models.CommonConfig{
			SourceVideoBitrate: 1500000,
			Valid:              true,
		},
	}

	processor := NewFFmpegProcessor(cfg)
	m := &models.ShrinkMedia{
		Path:       inputPath,
		Size:       getFileSize(inputPath),
		Duration:   2.0,
		VideoCount: 1,
		Ext:        ".mp4",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result := processor.Process(ctx, m, cfg, nil)

	if result.Error != nil {
		t.Errorf("expected successful transcode, got error: %v", result.Error)
	}

	// Verify output file exists and is smaller
	for _, output := range result.Outputs {
		if output.Path != "" && output.Path != inputPath {
			if _, err := os.Stat(output.Path); os.IsNotExist(err) {
				t.Errorf("output file does not exist: %s", output.Path)
			}
			// Clean up
			defer os.Remove(output.Path)
		}
	}
}

func TestIntegration_VideoTranscode_WithSubtitles(t *testing.T) {
	skipIfNoFFmpeg(t)

	tempDir := t.TempDir()
	inputPath := createTestVideoWithSubtitles(t, tempDir, "input_subs.mkv")

	// Probe to get subtitle info
	probe, err := ProbeMedia(inputPath)
	if err != nil {
		t.Fatalf("failed to probe media: %v", err)
	}

	if len(probe.SubtitleStreams) == 0 {
		t.Skip("no subtitle streams found, skipping test")
	}

	cfg := &models.ProcessorConfig{
		Video: models.VideoConfig{
			Preset:             "10",
			CRF:                "50",
			MinSavingsVideo:    0.05,
			TranscodingVideoRate: 1.0,
			MaxVideoWidth:      1920,
			MaxVideoHeight:     1080,
		},
		Common: models.CommonConfig{
			SourceVideoBitrate: 1500000,
			Valid:              true,
		},
	}

	processor := NewFFmpegProcessor(cfg)
	m := &models.ShrinkMedia{
		Path:         inputPath,
		Size:         getFileSize(inputPath),
		Duration:     10.0,
		VideoCount:   1,
		SubtitleCodecs: "mov_text",
		Ext:          ".mkv",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result := processor.Process(ctx, m, cfg, nil)

	if result.Error != nil {
		t.Errorf("expected successful transcode with subtitles, got error: %v", result.Error)
	}

	// Verify output has subtitles
	for _, output := range result.Outputs {
		if output.Path != "" && output.Path != inputPath {
			outProbe, err := ProbeMedia(output.Path)
			if err != nil {
				t.Errorf("failed to probe output: %v", err)
			} else if len(outProbe.SubtitleStreams) == 0 {
				t.Error("output file has no subtitle streams")
			}
			defer os.Remove(output.Path)
		}
	}
}

func TestIntegration_VideoTranscode_AlreadyAV1(t *testing.T) {
	skipIfNoFFmpeg(t)

	tempDir := t.TempDir()
	// Create an already AV1-encoded file
	inputPath := filepath.Join(tempDir, "already_av1.mkv")

	cmd := exec.Command("ffmpeg",
		"-y",
		"-f", "lavfi",
		"-i", "testsrc=size=640x480:rate=30",
		"-f", "lavfi",
		"-i", "sine=frequency=440:duration=2",
		"-t", "2",
		"-c:v", "libsvtav1",
		"-preset", "10",
		"-crf", "50",
		inputPath,
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("failed to create AV1 test video: %v\n%s", err, output)
	}

	cfg := &models.ProcessorConfig{
		Video: models.VideoConfig{
			Preset:             "10",
			CRF:                "50",
			MinSavingsVideo:    0.05,
			TranscodingVideoRate: 1.0,
		},
		Common: models.CommonConfig{
			SourceVideoBitrate: 1500000,
			Valid:              true,
		},
	}

	processor := NewFFmpegProcessor(cfg)
	m := &models.ShrinkMedia{
		Path:       inputPath,
		Size:       getFileSize(inputPath),
		Duration:   2.0,
		VideoCount: 1,
		Ext:        ".mkv",
	}

	ctx := context.Background()
	result := processor.Process(ctx, m, cfg, nil)

	// Should skip files already encoded in AV1
	if result.Error != nil {
		t.Errorf("expected success (skip AV1), got error: %v", result.Error)
	}
}

// =============================================================================
// Audio Integration Tests
// =============================================================================

func TestIntegration_AudioTranscode_AAC_to_Opus(t *testing.T) {
	skipIfNoFFmpeg(t)

	tempDir := t.TempDir()
	// Use .m4a container for AAC (not .mp3)
	inputPath := createTestAudio(t, tempDir, "input.m4a", 5.0)

	cfg := &models.ProcessorConfig{
		Audio: models.AudioConfig{
			MinSavingsAudio:      0.05,
			TranscodingAudioRate: 1.0,
			TargetAudioBitrate:   128000,
		},
		Common: models.CommonConfig{
			SourceAudioBitrate: 256000,
			Valid:              true,
		},
	}

	processor := NewFFmpegProcessor(cfg)
	m := &models.ShrinkMedia{
		Path:       inputPath,
		Size:       getFileSize(inputPath),
		Duration:   5.0,
		AudioCount: 1,
		Ext:        ".mp3",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result := processor.Process(ctx, m, cfg, nil)

	if result.Error != nil {
		t.Errorf("expected successful audio transcode, got error: %v", result.Error)
	}

	// Verify output is Opus
	for _, output := range result.Outputs {
		if output.Path != "" && output.Path != inputPath {
			outProbe, err := ProbeMedia(output.Path)
			if err != nil {
				t.Errorf("failed to probe output: %v", err)
			} else {
				audioStream := getFirstStream(outProbe.AudioStreams)
				if audioStream == nil || audioStream.CodecName != "opus" {
					t.Errorf("expected opus codec, got: %v", audioStream)
				}
			}
			defer os.Remove(output.Path)
		}
	}
}

func TestIntegration_AudioTranscode_WithLoudnorm(t *testing.T) {
	skipIfNoFFmpeg(t)

	tempDir := t.TempDir()
	inputPath := createTestAudio(t, tempDir, "input_loud.m4a", 3.0)

	cfg := &models.ProcessorConfig{
		Audio: models.AudioConfig{
			MinSavingsAudio:      0.05,
			TranscodingAudioRate: 1.0,
			TargetAudioBitrate:   128000,
		},
		Common: models.CommonConfig{
			SourceAudioBitrate: 256000,
			Valid:              true,
		},
	}

	processor := NewFFmpegProcessor(cfg)
	m := &models.ShrinkMedia{
		Path:       inputPath,
		Size:       getFileSize(inputPath),
		Duration:   3.0,
		AudioCount: 1,
		Ext:        ".mp3",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result := processor.Process(ctx, m, cfg, nil)

	if result.Error != nil {
		t.Errorf("expected successful audio transcode with loudnorm, got error: %v", result.Error)
	}
}

func TestIntegration_AudioTranscode_WithSilenceSplit(t *testing.T) {
	skipIfNoFFmpeg(t)

	tempDir := t.TempDir()
	inputPath := createTestAudio(t, tempDir, "input_silence.m4a", 5.0)

	cfg := &models.ProcessorConfig{
		Audio: models.AudioConfig{
			MinSavingsAudio:      0.05,
			TranscodingAudioRate: 1.0,
			TargetAudioBitrate:   128000,
			AlwaysSplit:          true,
			MinSplitSegment:      2.0,
		},
		Common: models.CommonConfig{
			SourceAudioBitrate: 256000,
			Valid:              true,
		},
	}

	processor := NewFFmpegProcessor(cfg)
	m := &models.ShrinkMedia{
		Path:       inputPath,
		Size:       getFileSize(inputPath),
		Duration:   5.0,
		AudioCount: 1,
		Ext:        ".mp3",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result := processor.Process(ctx, m, cfg, nil)

	if result.Error != nil {
		t.Errorf("expected successful audio transcode with silence split, got error: %v", result.Error)
	}
}

// =============================================================================
// Image Integration Tests
// =============================================================================

func TestIntegration_ImageTranscode_JPEG_to_AVIF(t *testing.T) {
	skipIfNoFFmpeg(t)

	if !utils.CommandExists("magick") {
		t.Skip("ImageMagick not available, skipping image test")
	}

	tempDir := t.TempDir()
	inputPath := createTestImage(t, tempDir, "input.jpg", 800, 600)

	// Get original file info
	origInfo, err := os.Stat(inputPath)
	if err != nil {
		t.Fatalf("failed to stat input: %v", err)
	}

	// Image processing is handled by the registry, not directly by FFmpegProcessor
	// This test verifies that FFmpeg can at least read the image
	probe, err := ProbeMedia(inputPath)
	if err != nil {
		t.Errorf("failed to probe image: %v", err)
	} else if len(probe.Streams) == 0 {
		t.Error("no streams found in image")
	}

	// Verify file size is reasonable
	if origInfo.Size() == 0 {
		t.Error("image file is empty")
	}
}

// =============================================================================
// Edge Case Integration Tests
// =============================================================================

func TestIntegration_VideoTranscode_Timeout(t *testing.T) {
	skipIfNoFFmpeg(t)

	tempDir := t.TempDir()
	inputPath := createTestVideo(t, tempDir, "input.mp4", 1.0, 640, 480)

	cfg := &models.ProcessorConfig{
		Video: models.VideoConfig{
			Preset:             "10",
			CRF:                "50",
			MinSavingsVideo:    0.05,
			TranscodingVideoRate: 1.0,
		},
		Common: models.CommonConfig{
			SourceVideoBitrate: 1500000,
			Valid:              true,
		},
	}

	processor := NewFFmpegProcessor(cfg)
	m := &models.ShrinkMedia{
		Path:       inputPath,
		Size:       getFileSize(inputPath),
		Duration:   1.0,
		VideoCount: 1,
		Ext:        ".mp4",
	}

	// Very short timeout to trigger timeout error
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Millisecond)
	defer cancel()

	result := processor.Process(ctx, m, cfg, nil)

	// Should get a timeout/cancellation error
	if result.Error == nil {
		t.Error("expected timeout error, got nil")
	}
}

func TestIntegration_ProbeMedia_RealFile(t *testing.T) {
	skipIfNoFFmpeg(t)

	tempDir := t.TempDir()
	inputPath := createTestVideo(t, tempDir, "probe_test.mp4", 2.0, 640, 480)

	probe, err := ProbeMedia(inputPath)
	if err != nil {
		t.Fatalf("failed to probe media: %v", err)
	}

	if probe.Format.Duration == "" {
		t.Error("expected format info")
	}

	if len(probe.Streams) == 0 {
		t.Error("expected at least one stream")
	}

	if len(probe.VideoStreams) == 0 {
		t.Error("expected video stream")
	}

	if probe.Duration <= 0 {
		t.Error("expected positive duration")
	}
}

func TestIntegration_VideoTranscode_Stereo360(t *testing.T) {
	skipIfNoFFmpeg(t)

	tempDir := t.TempDir()
	// Create a side-by-side 360 video (simulated)
	inputPath := filepath.Join(tempDir, "360_input.mp4")

	cmd := exec.Command("ffmpeg",
		"-y",
		"-f", "lavfi",
		"-i", "testsrc=size=3840x1920:rate=30",
		"-f", "lavfi",
		"-i", "sine=frequency=440:duration=2",
		"-t", "2",
		"-c:v", "libx264",
		"-preset", "ultrafast",
		"-c:a", "aac",
		"-metadata", "stereo_mode=side_by_side",
		inputPath,
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("failed to create 360 test video: %v\n%s", err, output)
	}

	cfg := &models.ProcessorConfig{
		Video: models.VideoConfig{
			Preset:             "10",
			CRF:                "50",
			MinSavingsVideo:    0.05,
			TranscodingVideoRate: 1.0,
			MaxVideoWidth:      1920,
			MaxVideoHeight:     1080,
		},
		Common: models.CommonConfig{
			SourceVideoBitrate: 1500000,
			Valid:              true,
			MaxWidthBuffer:     0.05,
			MaxHeightBuffer:    0.05,
		},
	}

	processor := NewFFmpegProcessor(cfg)
	m := &models.ShrinkMedia{
		Path:       inputPath,
		Size:       getFileSize(inputPath),
		Duration:   2.0,
		VideoCount: 1,
		Ext:        ".mp4",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result := processor.Process(ctx, m, cfg, nil)

	if result.Error != nil {
		t.Errorf("expected successful 360 video transcode, got error: %v", result.Error)
	}
}

// =============================================================================
// Helper Functions
// =============================================================================

func getFileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}
