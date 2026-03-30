package ffmpeg

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"github.com/chapmanjacobd/shrink/internal/utils"
)

// FFProbeResult represents ffprobe JSON output
type FFProbeResult struct {
	VideoStreams    []FFProbeStream `json:"-"`
	AudioStreams    []FFProbeStream `json:"-"`
	SubtitleStreams []FFProbeStream `json:"-"`
	AlbumArtStreams []FFProbeStream `json:"-"`
	Streams         []FFProbeStream `json:"streams"`
	Path            string          `json:"-"`
	Format          FFProbeFormat   `json:"format"`
	Duration        float64         `json:"-"`
}

// FFProbeStream represents a stream in ffprobe output
type FFProbeStream struct {
	Tags        map[string]string `json:"tags"`
	Disposition map[string]int    `json:"disposition"`
	CodecType   string            `json:"codec_type"`
	CodecName   string            `json:"codec_name"`
	Duration    string            `json:"duration"`
	NbFrames    string            `json:"nb_frames"`
	RFrameRate  string            `json:"r_frame_rate"`
	BitRate     string            `json:"bit_rate"`
	SampleRate  string            `json:"sample_rate"`
	Index       int               `json:"index"`
	Width       int               `json:"width"`
	Height      int               `json:"height"`
	Channels    int               `json:"channels"`
}

// FFProbeFormat represents the format section of ffprobe output
type FFProbeFormat struct {
	Tags      map[string]string `json:"tags"`
	Duration  string            `json:"duration"`
	BitRate   string            `json:"bit_rate"`
	NbStreams int               `json:"nb_streams"`
}

// ProbeMedia probes a media file and returns metadata
func ProbeMedia(path string) (*FFProbeResult, error) {
	ffprobe := utils.GetCommandPath("ffprobe")
	if ffprobe == "" {
		return nil, fmt.Errorf("ffprobe not installed")
	}
	cmd := exec.Command(ffprobe,
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
		if d, err := strconv.ParseFloat(probe.Format.Duration, 64); err == nil {
			probe.Duration = d
		}
	}

	// Categorize streams
	for _, s := range probe.Streams {
		switch s.CodecType {
		case "video":
			// attached pics (album art) often have mjpeg or png codec
			if s.Disposition["attached_pic"] == 1 || s.CodecName == "mjpeg" || s.CodecName == "png" {
				probe.AlbumArtStreams = append(probe.AlbumArtStreams, s)
			} else if s.Width > 0 || s.Height > 0 {
				probe.VideoStreams = append(probe.VideoStreams, s)
			} else {
				// Fallback for cases where width/height might be 0 but it's not album art
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

// GetImageDimensions uses ffprobe to get the actual width and height of an image
func GetImageDimensions(path string) (int, int, error) {
	probe, err := ProbeMedia(path)
	if err != nil {
		return 0, 0, err
	}

	// First, try video streams (standard for most images including AVIF)
	for _, stream := range probe.VideoStreams {
		if stream.Width > 0 && stream.Height > 0 {
			return stream.Width, stream.Height, nil
		}
	}

	// Fallback: check all streams for any with valid dimensions
	// This handles edge cases where ffprobe reports codec_type differently
	for _, stream := range probe.Streams {
		if stream.Width > 0 && stream.Height > 0 {
			return stream.Width, stream.Height, nil
		}
	}

	return 0, 0, fmt.Errorf("no video stream found")
}

func (p *FFmpegProcessor) isAnimationFromProbe(probe *FFProbeResult) bool {
	if probe == nil {
		return false
	}
	if len(probe.AudioStreams) > 0 {
		return true
	}

	ffprobe := utils.GetCommandPath("ffprobe")
	for _, s := range probe.VideoStreams {
		frames := parseInt(s.NbFrames)
		if frames == 0 && ffprobe != "" {
			cmd := exec.Command(ffprobe,
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
			return true
		}
	}

	return false
}

func (p *FFmpegProcessor) countFrames(path string) int {
	ffprobe := utils.GetCommandPath("ffprobe")
	if ffprobe == "" {
		return 0
	}
	cmd := exec.Command(ffprobe,
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

func parseFPS(probe *FFProbeResult) float64 {
	if len(probe.VideoStreams) == 0 {
		return 0
	}
	stream := probe.VideoStreams[0]
	parts := strings.Split(stream.RFrameRate, "/")
	if len(parts) != 2 {
		return 0
	}
	num, err := strconv.ParseFloat(parts[0], 64)
	if err != nil {
		return 0
	}
	den, err := strconv.ParseFloat(parts[1], 64)
	if err != nil || den == 0 {
		return 0
	}
	return num / den
}
