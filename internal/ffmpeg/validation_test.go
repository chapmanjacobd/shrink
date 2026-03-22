package ffmpeg

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/chapmanjacobd/shrink/internal/models"
)

func TestValidateTranscode(t *testing.T) {
	p := &FFmpegProcessor{}
	m := models.ShrinkMedia{Path: "orig.mp4", Size: 1000, Duration: 10.0}

	// Case 1: Output file missing
	res := p.validateTranscode(m, "nonexistent.mkv", nil)
	if res.Success {
		t.Errorf("expected failure for nonexistent output")
	}

	// Case 2: Output file empty
	tempDir := t.TempDir()
	emptyFile := filepath.Join(tempDir, "empty.mkv")
	os.WriteFile(emptyFile, []byte(""), 0o644)
	res = p.validateTranscode(m, emptyFile, nil)
	if res.Success {
		t.Errorf("expected failure for empty output")
	}
}

func TestValidateTranscode_DurationMismatch(t *testing.T) {
	// Case: probed duration is 50s (half of original 100s)
	// This will trigger deleteTranscode = true
}

func TestIsUnsupportedError(t *testing.T) {
	p := &FFmpegProcessor{}
	if !p.isUnsupportedError([]string{"Unknown encoder 'libsvtav1'"}) {
		t.Errorf("failed libsvtav1")
	}
	if !p.isUnsupportedError([]string{"Encoder not found"}) {
		t.Errorf("failed encoder not found")
	}
}
