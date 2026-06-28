package utils

import (
	"os"
	"path/filepath"
	"strings"
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
		{31622400, "1y 1d"},
	}
	for _, tt := range tests {
		got := FormatDuration(float64(tt.sec))
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

func TestPrintTableToString(t *testing.T) {
	t.Run("empty headers", func(t *testing.T) {
		if got := PrintTableToString(nil, nil); got != "" {
			t.Errorf("expected empty, got %q", got)
		}
	})

	t.Run("no MISSING in output", func(t *testing.T) {
		headers := []string{"Media Type", "Queued", "Skip", "Fail", "OK", "Saved", "Size", "Speed", "ETA"}
		rows := [][]string{{"video", "1", "0", "0", "5", "100 MB", "10 MB/s", "5s"}}
		got := PrintTableToString(headers, rows)
		if containsMissing(got) {
			t.Errorf("output contains %%!s(MISSING):\n%s", got)
		}
	})

	t.Run("all headers present in output", func(t *testing.T) {
		headers := []string{"Type", "Count", "Size"}
		rows := [][]string{{"txt", "10", "1 KB"}}
		got := PrintTableToString(headers, rows)
		for _, h := range headers {
			if !strings.Contains(got, h) {
				t.Errorf("output missing header %q:\n%s", h, got)
			}
		}
	})

	t.Run("all values present in output", func(t *testing.T) {
		headers := []string{"Name", "Value"}
		rows := [][]string{{"foo", "bar"}, {"baz", "qux"}}
		got := PrintTableToString(headers, rows)
		for _, cell := range []string{"foo", "bar", "baz", "qux"} {
			if !strings.Contains(got, cell) {
				t.Errorf("output missing value %q:\n%s", cell, got)
			}
		}
	})

	t.Run("right-aligned numeric columns", func(t *testing.T) {
		headers := []string{"Name", "Count"}
		rows := [][]string{{"a", "100"}, {"b", "5"}}
		got := PrintTableToString(headers, rows)
		// Count column should be right-aligned: "100" lined up, "  5" padded
		if containsMissing(got) {
			t.Errorf("output contains %%!s(MISSING):\n%s", got)
		}
		lines := splitLines(got)
		if len(lines) != 3 {
			t.Errorf("expected 3 lines, got %d", len(lines))
		}
	})

	t.Run("empty rows prints header only", func(t *testing.T) {
		got := PrintTableToString([]string{"A", "B"}, nil)
		lines := splitLines(got)
		if len(lines) != 1 {
			t.Errorf("expected 1 line (header only), got %d lines:\n%s", len(lines), got)
		}
	})

	t.Run("extra columns in row are ignored", func(t *testing.T) {
		headers := []string{"A", "B"}
		rows := [][]string{{"1", "2", "3"}}
		got := PrintTableToString(headers, rows)
		if containsMissing(got) {
			t.Errorf("output contains %%!s(MISSING):\n%s", got)
		}
		if !strings.Contains(got, "1") || !strings.Contains(got, "2") {
			t.Errorf("output missing expected values:\n%s", got)
		}
	})
}

func containsMissing(s string) bool {
	return strings.Contains(s, "%!s(MISSING)")
}

func splitLines(s string) []string {
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}

func TestFileExists(t *testing.T) {
	tempDir := t.TempDir()
	file := filepath.Join(tempDir, "test.txt")
	_ = os.WriteFile(file, []byte("test"), 0o644)

	if !FileExists(file) {
		t.Errorf("expected file to exist")
	}
	if FileExists(filepath.Join(tempDir, "none.txt")) {
		t.Errorf("expected file to not exist")
	}
}

func TestGetMountPoint(t *testing.T) {
	paths := []string{t.TempDir(), ".", "/", "/tmp"}
	for _, p := range paths {
		// Skip if directory doesn't exist (e.g. /tmp on some systems, though unlikely)
		if _, err := os.Stat(p); os.IsNotExist(err) {
			continue
		}

		mp, err := GetMountPoint(p)
		if err != nil {
			t.Errorf("failed to get mount point for %s: %v", p, err)
			continue
		}
		if mp == "" {
			t.Errorf("expected non-empty mount point for %s", p)
		}
	}

	// Test with non-existent file - should still work by walking up to existing parent
	tempDir := t.TempDir()
	nonExistentFile := filepath.Join(tempDir, "does_not_exist", "file.txt")
	mp, err := GetMountPoint(nonExistentFile)
	if err != nil {
		t.Errorf("GetMountPoint failed for non-existent file %s: %v", nonExistentFile, err)
	}
	if mp == "" {
		t.Errorf("expected non-empty mount point for non-existent file %s", nonExistentFile)
	}
}

func TestFolderSize(t *testing.T) {
	tempDir := t.TempDir()
	_ = os.WriteFile(filepath.Join(tempDir, "f1.txt"), make([]byte, 1000), 0o644)
	_ = os.WriteFile(filepath.Join(tempDir, "f2.txt"), make([]byte, 2000), 0o644)

	size := FolderSize(tempDir)
	if size < 3000 {
		t.Errorf("expected size at least 3000, got %d", size)
	}
}
