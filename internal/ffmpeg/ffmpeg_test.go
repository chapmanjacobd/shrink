package ffmpeg

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chapmanjacobd/shrink/internal/models"
)

func setupMockFFmpeg(t *testing.T, ffprobeOutput string, ffmpegBehavior string, silenceOutput string) (string, func()) {
	t.Helper()
	tempDir := t.TempDir()

	// Mock ffprobe
	ffprobeScript := filepath.Join(tempDir, "ffprobe")
	ffprobeContent := `#!/bin/bash
if [[ "$*" == *"-count_frames"* ]]; then
	echo "10"
	exit 0
fi
echo '` + strings.ReplaceAll(ffprobeOutput, "'", "'\\''") + `'
`
	os.WriteFile(ffprobeScript, []byte(ffprobeContent), 0o755)

	// Mock ffmpeg
	ffmpegScript := filepath.Join(tempDir, "ffmpeg")
	var ffmpegContent string
	switch ffmpegBehavior {
	case "success":
		ffmpegContent = `#!/bin/bash
# Find output argument
for arg in "$@"; do
	if [[ $arg == *.mkv || $arg == *.mka || $arg == *.avif ]]; then
		echo "mock output" > "$arg"
		exit 0
	fi
done
# In case of silence detection
if [[ "$*" == *silencedetect* ]]; then
	echo '` + silenceOutput + `' >&2
	exit 0
fi
`
	case "fail":
		ffmpegContent = `#!/bin/bash
echo "Unknown encoder" >&2
exit 1
`
	case "timeout":
		ffmpegContent = `#!/bin/bash
sleep 5
exit 0
`
	}
	os.WriteFile(ffmpegScript, []byte(ffmpegContent), 0o755)

	oldPath := os.Getenv("PATH")
	os.Setenv("PATH", tempDir+string(os.PathListSeparator)+oldPath)

	return tempDir, func() {
		os.Setenv("PATH", oldPath)
	}
}

func TestProcess_Video(t *testing.T) {
	mockProbe := `{"streams": [{"index": 0, "codec_type": "video", "codec_name": "h264", "width": 1920, "height": 1080}], "format": {"duration": "10.0", "size": "1000000", "bit_rate": "800000"}}`
	_, cleanup := setupMockFFmpeg(t, mockProbe, "success", "")
	defer cleanup()

	cfg := &models.ProcessorConfig{
		Video: models.VideoConfig{
			TranscodingVideoRate: 1.0,
			MinSavingsVideo:      0.1,
		},
		Common: models.CommonConfig{
			SourceVideoBitrate: 1500000,
		},
	}
	processor := NewFFmpegProcessor(cfg)
	m := &models.ShrinkMedia{
		Path:     "test.mp4",
		Size:     1000000,
		Duration: 10.0,
	}
	os.WriteFile("test.mp4", []byte("dummy"), 0o644)
	defer os.Remove("test.mp4")

	ctx := context.Background()
	result := processor.Process(ctx, m, cfg, nil)

	if result.Error != nil {
		t.Errorf("expected success, got error: %v", result.Error)
	}
}

func TestProcess_AudioSilence(t *testing.T) {
	mockProbe := `{"streams": [{"index": 0, "codec_type": "audio", "codec_name": "aac", "channels": 2}], "format": {"duration": "100.0", "size": "1000000"}}`
	silenceOutput := `[silencedetect @ 0x1234] silence_end: 15.5 | silence_duration: 3.5\n[silencedetect @ 0x1234] silence_end: 45.2 | silence_duration: 2.1`
	_, cleanup := setupMockFFmpeg(t, mockProbe, "success", silenceOutput)
	defer cleanup()

	cfg := &models.ProcessorConfig{
		Audio: models.AudioConfig{
			TranscodingAudioRate: 1.0,
			MinSavingsAudio:      0.1,
			AlwaysSplit:          true,
			MinSplitSegment:      10.0,
		},
	}
	processor := NewFFmpegProcessor(cfg)
	m := &models.ShrinkMedia{
		Path:     "test.mp3",
		Size:     1000000,
		Duration: 100.0,
	}
	os.WriteFile("test.mp3", []byte("dummy"), 0o644)
	defer os.Remove("test.mp3")

	ctx := context.Background()
	result := processor.Process(ctx, m, cfg, nil)

	if result.Error != nil {
		t.Errorf("expected success, got error: %v", result.Error)
	}
}

func TestProcess_Stereo360(t *testing.T) {
	mockProbe := `{"streams": [{"index": 0, "codec_type": "video", "codec_name": "h264", "width": 3840, "height": 1920, "tags": {"stereo_mode": "top_bottom"}}], "format": {"duration": "10.0", "size": "1000000"}}`
	_, cleanup := setupMockFFmpeg(t, mockProbe, "success", "")
	defer cleanup()

	cfg := &models.ProcessorConfig{}
	processor := NewFFmpegProcessor(cfg)
	m := &models.ShrinkMedia{
		Path:     "test_360.mp4",
		Size:     1000000,
		Duration: 10.0,
	}
	os.WriteFile("test_360.mp4", []byte("dummy"), 0o644)
	defer os.Remove("test_360.mp4")

	ctx := context.Background()
	result := processor.Process(ctx, m, cfg, nil)

	if result.Error != nil {
		t.Errorf("expected success, got error: %v", result.Error)
	}
}

func TestProcess_AlbumArt(t *testing.T) {
	mockProbe := `{"streams": [
		{"index": 0, "codec_type": "audio", "codec_name": "mp3"},
		{"index": 1, "codec_type": "video", "codec_name": "mjpeg", "disposition": {"attached_pic": 1}}
	], "format": {"duration": "10.0", "size": "1000000"}}`
	_, cleanup := setupMockFFmpeg(t, mockProbe, "success", "")
	defer cleanup()

	cfg := &models.ProcessorConfig{}
	processor := NewFFmpegProcessor(cfg)
	m := &models.ShrinkMedia{
		Path:     "test.mp3",
		Size:     1000000,
		Duration: 10.0,
	}
	os.WriteFile("test.mp3", []byte("dummy"), 0o644)
	defer os.Remove("test.mp3")

	ctx := context.Background()
	result := processor.Process(ctx, m, cfg, nil)

	if result.Error != nil {
		t.Errorf("expected success, got error: %v", result.Error)
	}
}

func TestProcess_Subtitles(t *testing.T) {
	mockProbe := `{"streams": [
		{"index": 0, "codec_type": "video", "codec_name": "h264", "width": 1920, "height": 1080},
		{"index": 1, "codec_type": "subtitle", "codec_name": "srt"},
		{"index": 2, "codec_type": "subtitle", "codec_name": "pgs"}
	], "format": {"duration": "10.0", "size": "1000000"}}`
	_, cleanup := setupMockFFmpeg(t, mockProbe, "success", "")
	defer cleanup()

	cfg := &models.ProcessorConfig{}
	processor := NewFFmpegProcessor(cfg)
	m := &models.ShrinkMedia{
		Path:     "test_subs.mkv",
		Size:     1000000,
		Duration: 10.0,
	}
	os.WriteFile("test_subs.mkv", []byte("dummy"), 0o644)
	defer os.Remove("test_subs.mkv")

	ctx := context.Background()
	result := processor.Process(ctx, m, cfg, nil)

	if result.Error != nil {
		t.Errorf("expected success, got error: %v", result.Error)
	}
}

func TestProcess_Timeout(t *testing.T) {
	mockProbe := `{"streams": [{"index": 0, "codec_type": "video", "codec_name": "h264", "width": 1920, "height": 1080}], "format": {"duration": "10.0", "size": "1000000"}}`
	_, cleanup := setupMockFFmpeg(t, mockProbe, "timeout", "")
	defer cleanup()

	cfg := &models.ProcessorConfig{}
	processor := NewFFmpegProcessor(cfg)
	m := &models.ShrinkMedia{
		Path:     "test.mp4",
		Size:     1000000,
		Duration: 10.0,
	}
	os.WriteFile("test.mp4", []byte("dummy"), 0o644)
	defer os.Remove("test.mp4")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	result := processor.Process(ctx, m, cfg, nil)

	if result.Error == nil {
		t.Errorf("expected timeout error")
	}
}

func TestIsAnimationFromProbe(t *testing.T) {
	mockProbe := `{"streams": [{"index": 0, "codec_type": "video", "codec_name": "gif"}], "format": {"duration": "10.0"}}`
	_, cleanup := setupMockFFmpeg(t, mockProbe, "success", "")
	defer cleanup()

	cfg := &models.ProcessorConfig{}
	processor := NewFFmpegProcessor(cfg)
	probe, _ := ProbeMedia("test.gif")
	isAnim := processor.isAnimationFromProbe(probe)
	if isAnim == nil || !*isAnim {
		t.Errorf("expected animation to be true")
	}
}

func TestParseFPS(t *testing.T) {
	probe := &FFProbeResult{
		VideoStreams: []FFProbeStream{{RFrameRate: "30000/1001"}},
	}
	fps := parseFPS(probe)
	if fps < 29.9 || fps > 30.0 {
		t.Errorf("expected ~29.97, got %v", fps)
	}
	probe.VideoStreams[0].RFrameRate = "24/1"
	fps = parseFPS(probe)
	if fps != 24.0 {
		t.Errorf("expected 24.0, got %v", fps)
	}
}

func TestParseBitrate(t *testing.T) {
	br := parseBitrate("1500000")
	if br != 1500000 {
		t.Errorf("expected 1500000, got %v", br)
	}
}

func TestParseSampleRate(t *testing.T) {
	sr := parseSampleRate("44100")
	if sr != 44100 {
		t.Errorf("expected 44100, got %v", sr)
	}
}

func TestBuildScaleFilter(t *testing.T) {
	cfg := &models.ProcessorConfig{
		Video:  models.VideoConfig{MaxVideoWidth: 1920, MaxVideoHeight: 1080},
		Common: models.CommonConfig{MaxWidthBuffer: 0.05, MaxHeightBuffer: 0.05},
	}
	p := NewFFmpegProcessor(cfg)

	// Normal
	filters := p.buildScaleFilter("", 3840, 2160)
	foundScale := false
	for _, f := range filters {
		if strings.Contains(f, "scale=") {
			foundScale = true
		}
	}
	if !foundScale {
		t.Errorf("expected scale filter")
	}

	// Stereo top_bottom
	// Note: buildScaleFilter uses "ou" for over-under (top-bottom)
	filters = p.buildScaleFilter("ou", 3840, 3840)
	foundScale = false
	for _, f := range filters {
		if strings.Contains(f, "scale=") {
			foundScale = true
		}
	}
	if !foundScale {
		t.Errorf("expected scale for ou")
	}
}

func TestBuildVideoFilters(t *testing.T) {
	cfg := &models.ProcessorConfig{}
	p := NewFFmpegProcessor(cfg)
	probe := &FFProbeResult{}
	stream := &FFProbeStream{Width: 1920, Height: 1080}

	filters := p.buildVideoFilters(probe, stream)
	if len(filters) == 0 {
		t.Errorf("expected video filters")
	}
}

func TestBuildAudioOptions(t *testing.T) {
	cfg := &models.ProcessorConfig{}
	p := NewFFmpegProcessor(cfg)
	stream := &FFProbeStream{CodecName: "aac", Channels: 2}

	opts := p.buildAudioOptions(stream)
	if len(opts) == 0 {
		t.Errorf("expected audio options")
	}
}

func TestBuildSubtitleOptions(t *testing.T) {
	cfg := &models.ProcessorConfig{}
	p := NewFFmpegProcessor(cfg)
	streams := []FFProbeStream{{CodecName: "subrip"}}

	opts := p.buildSubtitleOptions(streams)
	if len(opts) == 0 {
		t.Errorf("expected subtitle options")
	}
}

func TestProcess_EmptyStreams(t *testing.T) {
	mockProbe := `{"streams": [], "format": {"duration": "10.0"}}`
	_, cleanup := setupMockFFmpeg(t, mockProbe, "success", "")
	defer cleanup()

	cfg := &models.ProcessorConfig{}
	p := NewFFmpegProcessor(cfg)
	m := &models.ShrinkMedia{Path: "test.mp4"}

	res := p.Process(context.Background(), m, cfg, nil)
	if res.Error == nil || !strings.Contains(res.Error.Error(), "no media streams") {
		t.Errorf("expected no media streams error, got %v", res.Error)
	}
}

func TestCountFrames(t *testing.T) {
	mockProbe := `{"streams": [{"index": 0, "codec_type": "video", "codec_name": "gif"}], "format": {"duration": "10.0"}}`
	_, cleanup := setupMockFFmpeg(t, mockProbe, "success", "")
	defer cleanup()

	cfg := &models.ProcessorConfig{}
	p := NewFFmpegProcessor(cfg)

	// mock_ffprobe handles -count_frames by returning "10"
	count := p.countFrames("test.gif")
	if count != 10 {
		t.Errorf("expected 10, got %d", count)
	}
}

// =============================================================================
// Subtitle Tests - Edge Cases for buildSubtitleOptions
// =============================================================================

func TestBuildSubtitleOptions_EmptyStreams(t *testing.T) {
	cfg := &models.ProcessorConfig{}
	p := NewFFmpegProcessor(cfg)

	opts := p.buildSubtitleOptions([]FFProbeStream{})
	if opts != nil {
		t.Errorf("expected nil for empty streams, got %v", opts)
	}
}

func TestBuildSubtitleOptions_MKVTextSubtitles_Copy(t *testing.T) {
	cfg := &models.ProcessorConfig{}
	p := NewFFmpegProcessor(cfg)

	testCases := []struct {
		codec  string
		expect []string
	}{
		{"subrip", []string{"-map", "0:0", "-c:s", "copy"}},
		{"srt", []string{"-map", "0:0", "-c:s", "copy"}},
		{"ass", []string{"-map", "0:0", "-c:s", "copy"}},
		{"ssa", []string{"-map", "0:0", "-c:s", "copy"}},
		{"webvtt", []string{"-map", "0:0", "-c:s", "copy"}},
	}

	for _, tc := range testCases {
		t.Run(tc.codec, func(t *testing.T) {
			streams := []FFProbeStream{{CodecName: tc.codec, Index: 0}}
			opts := p.buildSubtitleOptions(streams)
			if len(opts) != len(tc.expect) {
				t.Errorf("expected %d args, got %d: %v", len(tc.expect), len(opts), opts)
			}
			for i, exp := range tc.expect {
				if i < len(opts) && opts[i] != exp {
					t.Errorf("expected %q at position %d, got %q", exp, i, opts[i])
				}
			}
		})
	}
}

func TestBuildSubtitleOptions_MKVImageSubtitles_Copy(t *testing.T) {
	cfg := &models.ProcessorConfig{}
	p := NewFFmpegProcessor(cfg)

	testCases := []struct {
		codec  string
		expect []string
	}{
		{"pgssub", []string{"-map", "0:0", "-c:s", "copy"}},
		{"hdmv_pgs_subtitle", []string{"-map", "0:0", "-c:s", "copy"}},
		{"dvd_subtitle", []string{"-map", "0:0", "-c:s", "copy"}},
		{"vobsub", []string{"-map", "0:0", "-c:s", "copy"}},
	}

	for _, tc := range testCases {
		t.Run(tc.codec, func(t *testing.T) {
			streams := []FFProbeStream{{CodecName: tc.codec, Index: 0}}
			opts := p.buildSubtitleOptions(streams)
			if len(opts) != len(tc.expect) {
				t.Errorf("expected %d args, got %d", len(tc.expect), len(opts))
			}
			for i, exp := range tc.expect {
				if i < len(opts) && opts[i] != exp {
					t.Errorf("expected %q at position %d, got %q", exp, i, opts[i])
				}
			}
		})
	}
}

func TestBuildSubtitleOptions_TextSubtitles_ConvertToSRT(t *testing.T) {
	cfg := &models.ProcessorConfig{}
	p := NewFFmpegProcessor(cfg)

	// Text subtitles that need conversion to SRT
	testCases := []struct {
		codec  string
		expect []string
	}{
		{"mov_text", []string{"-map", "0:0", "-c:s", "srt"}},
		{"text", []string{"-map", "0:0", "-c:s", "srt"}},
		{"utf8", []string{"-map", "0:0", "-c:s", "srt"}},
		{"arib_caption", []string{"-map", "0:0", "-c:s", "srt"}},
		{"libaribcaption", []string{"-map", "0:0", "-c:s", "srt"}},
		{"libaribb24", []string{"-map", "0:0", "-c:s", "srt"}},
		{"libzvbi_teletextdec", []string{"-map", "0:0", "-c:s", "srt"}},
		{"dvb_teletext", []string{"-map", "0:0", "-c:s", "srt"}},
		{"cc_dec", []string{"-map", "0:0", "-c:s", "srt"}},
		{"jacosub", []string{"-map", "0:0", "-c:s", "srt"}},
		{"microdvd", []string{"-map", "0:0", "-c:s", "srt"}},
		{"mpl2", []string{"-map", "0:0", "-c:s", "srt"}},
		{"pjs", []string{"-map", "0:0", "-c:s", "srt"}},
		{"realtext", []string{"-map", "0:0", "-c:s", "srt"}},
		{"sami", []string{"-map", "0:0", "-c:s", "srt"}},
		{"stl", []string{"-map", "0:0", "-c:s", "srt"}},
		{"subviewer", []string{"-map", "0:0", "-c:s", "srt"}},
		{"subviewer1", []string{"-map", "0:0", "-c:s", "srt"}},
		{"vplayer", []string{"-map", "0:0", "-c:s", "srt"}},
	}

	for _, tc := range testCases {
		t.Run(tc.codec, func(t *testing.T) {
			streams := []FFProbeStream{{CodecName: tc.codec, Index: 0}}
			opts := p.buildSubtitleOptions(streams)
			if len(opts) != len(tc.expect) {
				t.Errorf("expected %d args, got %d", len(tc.expect), len(opts))
			}
			for i, exp := range tc.expect {
				if i < len(opts) && opts[i] != exp {
					t.Errorf("expected %q at position %d, got %q", exp, i, opts[i])
				}
			}
		})
	}
}

func TestBuildSubtitleOptions_ImageSubtitles_ConvertToPGS(t *testing.T) {
	cfg := &models.ProcessorConfig{}
	p := NewFFmpegProcessor(cfg)

	// Image subtitles that need conversion to PGS
	testCases := []struct {
		codec  string
		expect []string
	}{
		{"dvdsub", []string{"-map", "0:0", "-c:s", "pgssub"}},
		{"xsub", []string{"-map", "0:0", "-c:s", "pgssub"}},
		{"dvb_subtitle", []string{"-map", "0:0", "-c:s", "pgssub"}},
		{"dvbsub", []string{"-map", "0:0", "-c:s", "pgssub"}},
	}

	for _, tc := range testCases {
		t.Run(tc.codec, func(t *testing.T) {
			streams := []FFProbeStream{{CodecName: tc.codec, Index: 0}}
			opts := p.buildSubtitleOptions(streams)
			if len(opts) != len(tc.expect) {
				t.Errorf("expected %d args, got %d", len(tc.expect), len(opts))
			}
			for i, exp := range tc.expect {
				if i < len(opts) && opts[i] != exp {
					t.Errorf("expected %q at position %d, got %q", exp, i, opts[i])
				}
			}
		})
	}
}

func TestBuildSubtitleOptions_UnknownCodec_Skips(t *testing.T) {
	cfg := &models.ProcessorConfig{}
	p := NewFFmpegProcessor(cfg)

	// Unknown codecs should be skipped (return no args for that stream)
	unknownCodecs := []string{"unknown", "xyz", "custom_sub", ""}

	for _, codec := range unknownCodecs {
		t.Run(codec, func(t *testing.T) {
			streams := []FFProbeStream{{CodecName: codec, Index: 0}}
			opts := p.buildSubtitleOptions(streams)
			if len(opts) != 0 {
				t.Errorf("expected no args for unknown codec %q, got %v", codec, opts)
			}
		})
	}
}

func TestBuildSubtitleOptions_MultipleStreams(t *testing.T) {
	cfg := &models.ProcessorConfig{}
	p := NewFFmpegProcessor(cfg)

	// Mix of different subtitle types
	streams := []FFProbeStream{
		{CodecName: "subrip", Index: 0},   // copy (mkvTextSubs)
		{CodecName: "ass", Index: 1},      // copy (mkvTextSubs)
		{CodecName: "pgssub", Index: 2},   // copy (mkvImageSubs)
		{CodecName: "mov_text", Index: 3}, // convert to srt (textSubs only)
		{CodecName: "dvdsub", Index: 4},   // convert to pgs (imageSubs only)
		{CodecName: "unknown", Index: 5},  // skip
	}

	opts := p.buildSubtitleOptions(streams)

	// Expected: 5 streams * 4 args each = 20 args (unknown is skipped)
	// Each stream produces: -map, 0:X, -c:s, codec
	expectedArgs := 20
	if len(opts) != expectedArgs {
		t.Errorf("expected %d args for 5 streams, got %d", expectedArgs, len(opts))
	}

	// Verify structure: each stream should have -map, index, -c:s, codec
	expectedMaps := []string{"0:0", "0:1", "0:2", "0:3", "0:4"}
	expectedCodecs := []string{"copy", "copy", "copy", "srt", "pgssub"}

	for i := range expectedMaps {
		baseIdx := i * 4
		if baseIdx+3 < len(opts) {
			if opts[baseIdx] != "-map" || opts[baseIdx+1] != expectedMaps[i] {
				t.Errorf("stream %d: expected -map %s, got -map %s", i, expectedMaps[i], opts[baseIdx+1])
			}
			if opts[baseIdx+2] != "-c:s" || opts[baseIdx+3] != expectedCodecs[i] {
				t.Errorf("stream %d: expected -c:s %s, got -c:s %s", i, expectedCodecs[i], opts[baseIdx+3])
			}
		}
	}
}

func TestBuildSubtitleOptions_CaseInsensitive(t *testing.T) {
	cfg := &models.ProcessorConfig{}
	p := NewFFmpegProcessor(cfg)

	// Test case insensitivity - all should produce 4 args: -map, 0:0, -c:s, codec
	testCases := []struct {
		codec       string
		expectedFmt string
	}{
		{"SUBRIP", "copy"},
		{"SubRip", "copy"},
		{"ASS", "copy"},
		{"WebVTT", "copy"},
		{"PGSSUB", "copy"},
		{"MOV_TEXT", "srt"},
		{"DVDSUB", "pgssub"},
	}

	for _, tc := range testCases {
		t.Run(tc.codec, func(t *testing.T) {
			streams := []FFProbeStream{{CodecName: tc.codec, Index: 0}}
			opts := p.buildSubtitleOptions(streams)
			if len(opts) != 4 {
				t.Errorf("expected 4 args, got %d: %v", len(opts), opts)
			}
			if len(opts) >= 4 {
				if opts[0] != "-map" || opts[1] != "0:0" {
					t.Errorf("expected -map 0:0, got %v", opts[:2])
				}
				if opts[2] != "-c:s" {
					t.Errorf("expected -c:s, got %s", opts[2])
				}
			}
		})
	}
}

func TestBuildSubtitleOptions_DifferentIndices(t *testing.T) {
	cfg := &models.ProcessorConfig{}
	p := NewFFmpegProcessor(cfg)

	// Test with different stream indices
	streams := []FFProbeStream{
		{CodecName: "subrip", Index: 5},
		{CodecName: "ass", Index: 10},
		{CodecName: "pgssub", Index: 15},
	}

	opts := p.buildSubtitleOptions(streams)

	// Each stream produces 4 args: -map, 0:X, -c:s, codec
	expectedMaps := []string{"0:5", "0:10", "0:15"}
	for i, expMap := range expectedMaps {
		baseIdx := i * 4
		if baseIdx+1 < len(opts) {
			if opts[baseIdx] != "-map" {
				t.Errorf("stream %d: expected -map, got %s", i, opts[baseIdx])
			}
			if opts[baseIdx+1] != expMap {
				t.Errorf("stream %d: expected map %q, got %q", i, expMap, opts[baseIdx+1])
			}
		}
	}
}
