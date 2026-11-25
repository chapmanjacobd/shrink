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

// filterMediaByPath applies --include, --exclude, and --search. Include and
// exclude patterns are globs matched against both the full path and the file
// name. Search terms must all appear in the path (case-insensitive).
func (c *ShrinkCmd) filterMediaByPath(media []models.ShrinkMedia) ([]models.ShrinkMedia, error) {
	if len(c.Include) == 0 && len(c.Exclude) == 0 && len(c.Search) == 0 {
		return media, nil
	}

	// Validate patterns once so a bad pattern fails before processing starts.
	if _, err := matchAnyPattern("", c.Include); err != nil {
		return nil, err
	}
	if _, err := matchAnyPattern("", c.Exclude); err != nil {
		return nil, err
	}

	var err error
	filtered := make([]models.ShrinkMedia, 0, len(media))
	for _, m := range media {
		included := len(c.Include) == 0
		if !included {
			included, err = matchAnyPattern(m.Path, c.Include)
			if err != nil {
				return nil, err
			}
		}
		if !included {
			continue
		}

		excluded := false
		if len(c.Exclude) > 0 {
			excluded, err = matchAnyPattern(m.Path, c.Exclude)
			if err != nil {
				return nil, err
			}
		}
		if excluded {
			continue
		}

		if !matchesAllSearchTerms(m.Path, c.Search) {
			continue
		}
		filtered = append(filtered, m)
	}
	return filtered, nil
}

func matchAnyPattern(path string, patterns []string) (bool, error) {
	if len(patterns) == 0 {
		return false, nil
	}
	base := filepath.Base(path)
	for _, pattern := range patterns {
		for _, target := range []string{path, base} {
			matched, err := filepath.Match(pattern, target)
			if err != nil {
				return false, fmt.Errorf("invalid path pattern %q: %w", pattern, err)
			}
			if matched {
				return true, nil
			}
		}
	}
	return false, nil
}

func matchesAllSearchTerms(path string, terms []string) bool {
	lowerPath := strings.ToLower(path)
	for _, term := range terms {
		if !strings.Contains(lowerPath, strings.ToLower(term)) {
			return false
		}
	}
	return true
}

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
	_ = os.Chtimes(path, atime, mtime)

	// If it's a directory, walk and apply to all contents
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return
	}

	_ = filepath.Walk(path, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		_ = os.Chtimes(p, atime, mtime)
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
