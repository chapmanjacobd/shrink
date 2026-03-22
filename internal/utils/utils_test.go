package utils

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		sec  int
		want string
	}{
		{0, "0s"},
		{5, "5s"},
		{65, "1m 5s"},
		{3665, "1h 1m"},
		{90000, "1d 1h"},
	}
	for _, tt := range tests {
		got := FormatDuration(tt.sec)
		if got != tt.want {
			t.Errorf("FormatDuration(%d) = %v, want %v", tt.sec, got, tt.want)
		}
	}
}

func TestFormatSize(t *testing.T) {
	tests := []struct {
		bytes int64
		want  string
	}{
		{100, "100 B"},
		{1024, "1.0 KB"},
		{1048576, "1.0 MB"},
		{1073741824, "1.0 GB"},
	}
	for _, tt := range tests {
		got := FormatSize(tt.bytes)
		if got != tt.want {
			t.Errorf("FormatSize(%d) = %v, want %v", tt.bytes, got, tt.want)
		}
	}
}

func TestParseBitrate(t *testing.T) {
	tests := []struct {
		s    string
		want int64
	}{
		{"1000", 1000},
		{"1k", 1000},
		{"1kbps", 1000},
		{"1M", 1000000},
		{"1mbps", 1000000},
	}
	for _, tt := range tests {
		got := ParseBitrate(tt.s)
		if got != tt.want {
			t.Errorf("ParseBitrate(%s) = %v, want %v", tt.s, got, tt.want)
		}
	}
}

func TestParseSize(t *testing.T) {
	tests := []struct {
		s    string
		want int64
	}{
		{"1024", 1024},
		{"1KB", 1024},
		{"1KiB", 1024},
		{"1MB", 1024 * 1024},
		{"1MiB", 1024 * 1024},
		{"1GB", 1024 * 1024 * 1024},
	}
	for _, tt := range tests {
		got := ParseSize(tt.s)
		if got != tt.want {
			t.Errorf("ParseSize(%s) = %v, want %v", tt.s, got, tt.want)
		}
	}
}

func TestParsePercentOrBytes(t *testing.T) {
	tests := []struct {
		s    string
		want float64
	}{
		{"10%", 0.1},
		{"0.1", 0.1},
		{"1000", 1000.0},
		{"1MB", 1024 * 1024},
	}
	for _, tt := range tests {
		got := ParsePercentOrBytes(tt.s)
		if got != tt.want {
			t.Errorf("ParsePercentOrBytes(%s) = %v, want %v", tt.s, got, tt.want)
		}
	}
}

func TestParseDurationString(t *testing.T) {
	got := ParseDurationString("1h30m")
	if got != 90*time.Minute {
		t.Errorf("ParseDurationString(1h30m) = %v", got)
	}
}

func TestEstimateDurationFromSize(t *testing.T) {
	// 1500kbps video, 1.5MB should be ~8 seconds
	got := EstimateDurationFromSize(1500000, true)
	if got < 7.9 || got > 8.1 {
		t.Errorf("EstimateDurationFromSize = %v", got)
	}
}

func TestCommandExists(t *testing.T) {
	if !CommandExists("ls") && !CommandExists("dir") {
		t.Errorf("expected ls or dir to exist")
	}
	if CommandExists("nonexistentcommand12345") {
		t.Errorf("expected nonexistent command to not exist")
	}
}

func TestFileExists(t *testing.T) {
	tempDir := t.TempDir()
	file := filepath.Join(tempDir, "test.txt")
	os.WriteFile(file, []byte("test"), 0o644)

	if !FileExists(file) {
		t.Errorf("expected file to exist")
	}
	if FileExists(filepath.Join(tempDir, "none.txt")) {
		t.Errorf("expected file to not exist")
	}
}

func TestFolderSize(t *testing.T) {
	tempDir := t.TempDir()
	os.WriteFile(filepath.Join(tempDir, "f1.txt"), make([]byte, 1000), 0o644)
	os.WriteFile(filepath.Join(tempDir, "f2.txt"), make([]byte, 2000), 0o644)

	size := FolderSize(tempDir)
	if size < 3000 {
		t.Errorf("expected size at least 3000, got %d", size)
	}
}
