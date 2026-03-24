package commands

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chapmanjacobd/shrink/internal/ffmpeg"
	"github.com/chapmanjacobd/shrink/internal/models"
	"github.com/chapmanjacobd/shrink/internal/utils"
)

// scanDirectory scans a directory recursively for files
func (c *ShrinkCmd) scanDirectory(dirPath string) ([]models.ShrinkMedia, error) {
	var media []models.ShrinkMedia

	err := filepath.Walk(dirPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			slog.Warn("Error accessing path", "path", path, "error", err)
			return nil
		}

		if info.IsDir() {
			if path != dirPath && strings.HasPrefix(info.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}

		ext := strings.ToLower(filepath.Ext(path))
		m := c.createMediaEntry(path, info, ext)
		c.enrichMetadata(&m)

		media = append(media, m)
		return nil
	})

	return media, err
}

func (c *ShrinkCmd) createMediaEntry(path string, info os.FileInfo, ext string) models.ShrinkMedia {
	return models.ShrinkMedia{
		Path: path,
		Size: info.Size(),
		Ext:  ext,
	}
}

func (c *ShrinkCmd) enrichMetadata(m *models.ShrinkMedia) {
	if !utils.VideoExtensionMap[m.Ext] && !utils.AudioExtensionMap[m.Ext] {
		return
	}

	probed, err := ffmpeg.ProbeMedia(m.Path)
	if err != nil {
		return
	}

	m.Duration = probed.Duration
	m.VideoCount = len(probed.VideoStreams)
	m.AudioCount = len(probed.AudioStreams)

	for _, s := range probed.VideoStreams {
		if m.Width == 0 {
			m.Width = s.Width
			m.Height = s.Height
		}
	}
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

// IsAvailable returns true if the named tool is installed
func (it InstalledTools) IsAvailable(toolName string) bool {
	switch strings.ToLower(toolName) {
	case "ffmpeg":
		return it.FFmpeg
	case "magick", "imagemagick":
		return it.ImageMagick
	case "calibre":
		return it.Calibre
	case "unar":
		return it.Unar
	default:
		return false
	}
}

func (c *ShrinkCmd) checkInstalledTools() InstalledTools {
	tools := InstalledTools{
		FFmpeg:      utils.GetCommandPath("ffmpeg") != "",
		ImageMagick: utils.GetCommandPath("magick") != "" || utils.GetCommandPath("convert") != "",
		Calibre:     utils.GetCommandPath("ebook-convert") != "",
		Unar:        utils.GetCommandPath("lsar") != "",
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
