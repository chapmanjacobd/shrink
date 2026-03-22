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

		if info.IsDir() || strings.HasPrefix(info.Name(), ".") {
			return nil
		}

		ext := strings.ToLower(filepath.Ext(path))
		if !c.isMediaFile(path, info, ext) {
			return nil
		}

		if !c.shouldProcessByFilter(ext) {
			return nil
		}

		m := c.createMediaEntry(path, info, ext)
		c.enrichMetadata(&m)

		media = append(media, m)
		return nil
	})

	return media, err
}

func (c *ShrinkCmd) isMediaFile(path string, info os.FileInfo, ext string) bool {
	isMedia := utils.MediaExtensionMap[ext] || utils.ArchiveExtensionMap[ext]
	if !isMedia {
		if ext != "" {
			c.unknownExtensions[ext] += info.Size()
		}
		return false
	}
	return true
}

func (c *ShrinkCmd) shouldProcessByFilter(ext string) bool {
	if c.VideoOnly && !utils.VideoExtensionMap[ext] {
		return false
	}
	if c.AudioOnly && !utils.AudioExtensionMap[ext] {
		return false
	}
	if c.ImageOnly && !utils.ImageExtensionMap[ext] {
		return false
	}
	if c.TextOnly && !utils.TextExtensionMap[ext] {
		return false
	}
	return true
}

func (c *ShrinkCmd) createMediaEntry(path string, info os.FileInfo, ext string) models.ShrinkMedia {
	m := models.ShrinkMedia{
		Path:      path,
		Size:      info.Size(),
		Ext:       ext,
		MediaType: detectMediaTypeFromExt(ext),
	}

	if utils.VideoExtensionMap[ext] {
		m.VideoCount = 1
	}
	if utils.AudioExtensionMap[ext] {
		m.AudioCount = 1
	}
	return m
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
