package ffmpeg

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// FFProbeResult represents ffprobe JSON output
type FFProbeResult struct {
	Streams  []FFProbeStream `json:"streams"`
	Format   FFProbeFormat   `json:"format"`
	Path     string          `json:"-"`
	Duration float64         `json:"-"`

	// Categorized streams
	VideoStreams    []FFProbeStream `json:"-"`
	AudioStreams    []FFProbeStream `json:"-"`
	SubtitleStreams []FFProbeStream `json:"-"`
	AlbumArtStreams []FFProbeStream `json:"-"`
}

// FFProbeStream represents a stream in ffprobe output
type FFProbeStream struct {
	Index       int               `json:"index"`
	CodecType   string            `json:"codec_type"`
	CodecName   string            `json:"codec_name"`
	Duration    string            `json:"duration"`
	NbFrames    string            `json:"nb_frames"`
	Width       int               `json:"width"`
	Height      int               `json:"height"`
	RFrameRate  string            `json:"r_frame_rate"`
	BitRate     string            `json:"bit_rate"`
	SampleRate  string            `json:"sample_rate"`
	Channels    int               `json:"channels"`
	Tags        map[string]string `json:"tags"`
	Disposition map[string]int    `json:"disposition"`
}

// FFProbeFormat represents the format section of ffprobe output
type FFProbeFormat struct {
	Duration  string            `json:"duration"`
	BitRate   string            `json:"bit_rate"`
	NbStreams int               `json:"nb_streams"`
	Tags      map[string]string `json:"tags"`
}

// ProbeMedia probes a media file and returns metadata
func ProbeMedia(path string) (*FFProbeResult, error) {
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
		if d, err := strconv.ParseFloat(probe.Format.Duration, 64); err == nil {
			probe.Duration = d
		}
	}

	// Categorize streams
	for _, s := range probe.Streams {
		switch s.CodecType {
		case "video":
			//attached pics (album art) often have mjpeg or png codec
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

	for _, stream := range probe.VideoStreams {
		if stream.Width > 0 && stream.Height > 0 {
			return stream.Width, stream.Height, nil
		}
	}

	return 0, 0, fmt.Errorf("no video stream found")
}

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
