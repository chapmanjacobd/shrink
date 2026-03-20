package utils

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// CommandExists checks if a command is available in PATH
func CommandExists(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// FolderSize calculates the total size of a folder
func FolderSize(path string) int64 {
	var size int64
	filepath.Walk(path, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		size += info.Size()
		return nil
	})
	return size
}

// FileExists checks if a file exists
func FileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// FormatDuration formats seconds into human readable duration
func FormatDuration(seconds int) string {
	if seconds == 0 {
		return "-"
	}
	d := seconds / 86400
	h := (seconds % 86400) / 3600
	m := (seconds % 3600) / 60
	s := seconds % 60

	if d > 0 {
		return fmt.Sprintf("%dd %02d:%02d", d, h, m)
	}
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%d:%02d", m, s)
}

// FormatSize formats bytes into human readable size
func FormatSize(bytes int64) string {
	if bytes == 0 {
		return "-"
	}
	const unit = 1000
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}

// ParseDurationString parses duration strings like "10s", "20m", "1h"
func ParseDurationString(s string) time.Duration {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	if d, err := time.ParseDuration(s); err == nil {
		return d
	}
	n, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return time.Duration(n * float64(time.Minute))
}

// ParseBitrate parses bitrate strings like "128kbps", "1Mbps"
func ParseBitrate(s string) int64 {
	s = strings.ToLower(strings.TrimSpace(s))
	multiplier := int64(1)
	if strings.HasSuffix(s, "kbps") {
		multiplier = 1000
		s = strings.TrimSuffix(s, "kbps")
	} else if strings.HasSuffix(s, "mbps") {
		multiplier = 1000000
		s = strings.TrimSuffix(s, "mbps")
	} else if strings.HasSuffix(s, "bps") {
		s = strings.TrimSuffix(s, "bps")
	}
	n, _ := strconv.ParseInt(s, 10, 64)
	return n * multiplier
}

// ParseSize parses size strings like "30KiB", "1MB"
func ParseSize(s string) int64 {
	s = strings.TrimSpace(s)
	multiplier := int64(1)
	if strings.HasSuffix(s, "KiB") || strings.HasSuffix(s, "KB") {
		multiplier = 1024
		s = strings.TrimSuffix(strings.TrimSuffix(s, "KiB"), "KB")
	} else if strings.HasSuffix(s, "MiB") || strings.HasSuffix(s, "MB") {
		multiplier = 1024 * 1024
		s = strings.TrimSuffix(strings.TrimSuffix(s, "MiB"), "MB")
	} else if strings.HasSuffix(s, "GiB") || strings.HasSuffix(s, "GB") {
		multiplier = 1024 * 1024 * 1024
		s = strings.TrimSuffix(strings.TrimSuffix(s, "GiB"), "GB")
	} else if strings.HasSuffix(s, "B") {
		s = strings.TrimSuffix(s, "B")
	}
	n, _ := strconv.ParseInt(s, 10, 64)
	return n * multiplier
}

// ParsePercentOrBytes parses percentage or byte values
func ParsePercentOrBytes(s string) float64 {
	s = strings.TrimSpace(s)
	if strings.HasSuffix(s, "%") {
		pct, _ := strconv.ParseFloat(strings.TrimSuffix(s, "%"), 64)
		return pct / 100.0
	}
	return float64(ParseSize(s))
}

// GetDurationForTimeout estimates duration from file size for timeout calculation
func GetDurationForTimeout(duration float64, size int64, ext string) float64 {
	if duration > 0 {
		return duration
	}
	// Estimate from size if duration unknown
	if VideoExtensionMap[ext] {
		return float64(size) / 1500000 * 8 // Assume 1500kbps
	}
	if AudioExtensionMap[ext] {
		return float64(size) / 256000 * 8 // Assume 256kbps
	}
	return 0
}

// Extension maps for file type detection
var (
	VideoExtensionMap = map[string]bool{
		".mp4": true, ".mkv": true, ".avi": true, ".mov": true, ".wmv": true,
		".flv": true, ".webm": true, ".m4v": true, ".mpeg": true, ".mpg": true,
		".3gp": true, ".ogv": true, ".vob": true, ".mts": true, ".m2ts": true,
	}
	AudioExtensionMap = map[string]bool{
		".mp3": true, ".flac": true, ".m4a": true, ".aac": true, ".ogg": true,
		".wav": true, ".wma": true, ".opus": true, ".aiff": true, ".ape": true,
		".alac": true, ".ac3": true, ".dts": true, ".mka": true,
	}
	ImageExtensionMap = map[string]bool{
		".jpg": true, ".jpeg": true, ".png": true, ".gif": true, ".bmp": true,
		".tiff": true, ".tif": true, ".webp": true, ".heic": true, ".heif": true,
		".avif": true, ".svg": true, ".ico": true, ".psd": true, ".raw": true,
	}
	TextExtensionMap = map[string]bool{
		".epub": true, ".mobi": true, ".azw3": true, ".azw": true, ".kf8": true,
		".pdf": true, ".txt": true, ".rtf": true, ".fb2": true, ".lit": true,
		".lrf": true, ".snb": true, ".tcr": true, ".pdb": true, ".oeb": true,
	}
	ArchiveExtensionMap = map[string]bool{
		".zip": true, ".rar": true, ".7z": true, ".tar": true, ".gz": true,
		".bz2": true, ".xz": true, ".cab": true, ".iso": true, ".dmg": true,
		".pkg": true, ".cpio": true, ".deb": true, ".rpm": true,
	}
)
