package commands

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chapmanjacobd/shrink/internal/ffmpeg"
	"github.com/chapmanjacobd/shrink/internal/models"
	"github.com/chapmanjacobd/shrink/internal/utils"
)

// scanDirectory scans a directory recursively for media files
func (c *ShrinkCmd) scanDirectory(dirPath string) ([]models.ShrinkMedia, error) {
	var media []models.ShrinkMedia

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
		m := models.ShrinkMedia{
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
			if probed, err := ffmpeg.ProbeMedia(path); err == nil {
				m.Duration = probed.Duration
				m.VideoCount = len(probed.VideoStreams)
				m.AudioCount = len(probed.AudioStreams)
				m.SubtitleCount = len(probed.SubtitleStreams)

				var vCodecs, aCodecs, sCodecs []string
				for _, s := range probed.VideoStreams {
					vCodecs = append(vCodecs, s.CodecName)
					if m.Width == 0 {
						m.Width = s.Width
						m.Height = s.Height
					}
				}
				for _, s := range probed.AudioStreams {
					codecInfo := s.CodecName
					if s.Channels > 0 {
						codecInfo += fmt.Sprintf(" %dch", s.Channels)
					}
					aCodecs = append(aCodecs, codecInfo)
				}
				for _, s := range probed.SubtitleStreams {
					label := s.CodecName
					if lang := s.Tags["language"]; lang != "" {
						label = lang
					}
					sCodecs = append(sCodecs, label)
				}

				m.VideoCodecs = strings.Join(vCodecs, ", ")
				m.AudioCodecs = strings.Join(aCodecs, ", ")
				m.SubtitleCodecs = strings.Join(sCodecs, ", ")
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
