package commands

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/chapmanjacobd/shrink/internal/utils"
)

// scanDirectory scans a directory recursively for media files
func (c *ShrinkCmd) scanDirectory(dirPath string) ([]ShrinkMedia, error) {
	var media []ShrinkMedia

	err := filepath.Walk(dirPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			slog.Warn("Error accessing path", "path", path, "error", err)
			return nil
		}

		// Skip directories
		if info.IsDir() {
			return nil
		}

		// Skip hidden files
		if strings.HasPrefix(info.Name(), ".") {
			return nil
		}

		ext := strings.ToLower(filepath.Ext(path))

		// Check if this is a media file
		isMedia := utils.MediaExtensionMap[ext] || utils.ArchiveExtensionMap[ext]
		if !isMedia {
			return nil
		}

		// Apply media type filters if specified
		if c.VideoOnly && !utils.VideoExtensionMap[ext] {
			return nil
		}
		if c.AudioOnly && !utils.AudioExtensionMap[ext] {
			return nil
		}
		if c.ImageOnly && !utils.ImageExtensionMap[ext] {
			return nil
		}
		if c.TextOnly && !utils.TextExtensionMap[ext] {
			return nil
		}

		// Create media entry with basic info
		m := ShrinkMedia{
			Path:      path,
			Size:      info.Size(),
			Ext:       ext,
			MediaType: detectMediaTypeFromExt(ext),
		}

		// Set video/audio counts for extension-based detection
		if utils.VideoExtensionMap[ext] {
			m.VideoCount = 1
		}
		if utils.AudioExtensionMap[ext] {
			m.AudioCount = 1
		}

		// Try to get accurate metadata using ffprobe for video/audio files
		if utils.VideoExtensionMap[ext] || utils.AudioExtensionMap[ext] {
			if probed, err := c.probeMedia(path); err == nil {
				m.Duration = probed.Duration
				if probed.VideoCount > 0 {
					m.VideoCount = probed.VideoCount
				}
				if probed.AudioCount > 0 {
					m.AudioCount = probed.AudioCount
				}
				m.SubtitleCount = probed.SubtitleCount
				if probed.VideoCodecs != "" {
					m.VideoCodecs = probed.VideoCodecs
				}
				if probed.AudioCodecs != "" {
					m.AudioCodecs = probed.AudioCodecs
				}
				if probed.SubtitleCodecs != "" {
					m.SubtitleCodecs = probed.SubtitleCodecs
				}
			}
		}

		media = append(media, m)
		return nil
	})
	if err != nil {
		return nil, err
	}

	return media, nil
}

// probeMedia uses ffprobe to get accurate stream counts and metadata
func (c *ShrinkCmd) probeMedia(path string) (*ShrinkMedia, error) {
	if !utils.CommandExists("ffprobe") {
		return nil, fmt.Errorf("ffprobe not available")
	}

	cmd := exec.Command("ffprobe",
		"-v", "quiet",
		"-hide_banner",
		"-show_format",
		"-show_streams",
		"-of", "json",
		path)

	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	var data struct {
		Streams []struct {
			CodecType   string            `json:"codec_type"`
			CodecName   string            `json:"codec_name"`
			Width       int               `json:"width"`
			Height      int               `json:"height"`
			RFrameRate  string            `json:"r_frame_rate"`
			Channels    int               `json:"channels"`
			SampleRate  string            `json:"sample_rate"`
			Tags        map[string]string `json:"tags"`
			Disposition map[string]int    `json:"disposition"`
		} `json:"streams"`
		Format struct {
			Duration string            `json:"duration"`
			Tags     map[string]string `json:"tags"`
		} `json:"format"`
	}

	if err := json.Unmarshal(output, &data); err != nil {
		return nil, err
	}

	m := &ShrinkMedia{
		Path: path,
	}

	var vCodecs, aCodecs, sCodecs []string

	for _, s := range data.Streams {
		switch s.CodecType {
		case "video":
			// Skip attached pics (album art)
			if s.Disposition["attached_pic"] == 1 || s.CodecName == "mjpeg" || s.CodecName == "png" {
				continue
			}
			m.VideoCount++
			codecInfo := s.CodecName
			vCodecs = append(vCodecs, codecInfo)

			if m.Width == 0 {
				m.Width = s.Width
				m.Height = s.Height
			}
		case "audio":
			m.AudioCount++
			codecInfo := s.CodecName
			if s.Channels > 0 {
				codecInfo += fmt.Sprintf(" %dch", s.Channels)
			}
			aCodecs = append(aCodecs, codecInfo)
		case "subtitle":
			m.SubtitleCount++
			label := s.CodecName
			if lang := s.Tags["language"]; lang != "" {
				label = lang
			}
			sCodecs = append(sCodecs, label)
		}
	}

	// Format info
	if d, err := strconv.ParseFloat(data.Format.Duration, 64); err == nil {
		m.Duration = d
	}

	m.VideoCodecs = strings.Join(vCodecs, ", ")
	m.AudioCodecs = strings.Join(aCodecs, ", ")
	m.SubtitleCodecs = strings.Join(sCodecs, ", ")

	return m, nil
}

// applyTimestamps applies timestamps to a file or folder (recursively for folders)
func applyTimestamps(path string, atime, mtime time.Time) {
	// Apply to the path itself
	os.Chtimes(path, atime, mtime)

	// If it's a directory, walk and apply to all contents
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return
	}

	filepath.Walk(path, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		os.Chtimes(p, atime, mtime)
		return nil
	})
}

// InstalledTools tracks which external tools are available
type InstalledTools struct {
	FFmpeg      bool
	ImageMagick bool
	Calibre     bool
	Unar        bool
}

func (c *ShrinkCmd) checkInstalledTools() InstalledTools {
	tools := InstalledTools{
		FFmpeg:      utils.CommandExists("ffmpeg"),
		ImageMagick: utils.CommandExists("magick"),
		Calibre:     utils.CommandExists("ebook-convert"),
		Unar:        utils.CommandExists("lsar"),
	}

	if !tools.FFmpeg {
		slog.Warn("ffmpeg not installed. Video and Audio files will be skipped")
	}
	if !tools.ImageMagick {
		slog.Warn("ImageMagick not installed. Image files will be skipped")
	}
	if !tools.Calibre {
		slog.Warn("Calibre not installed. Text files will be skipped")
	}
	if !tools.Unar {
		slog.Warn("unar not installed. Archives will not be extracted")
	}

	return tools
}
