package commands

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/chapmanjacobd/shrink/internal/models"
)

func TestSortByEfficiency(t *testing.T) {
	cmd := &ShrinkCmd{}
	media := []models.ShrinkMedia{
		{Path: "slow", Savings: 1000, ProcessingTime: 100}, // 10 bytes/sec
		{Path: "fast", Savings: 1000, ProcessingTime: 10},  // 100 bytes/sec
	}
	cmd.SortByEfficiency(media)
	if media[0].Path != "fast" {
		t.Errorf("expected fast first, got %s", media[0].Path)
	}
}

func TestGetTimeout(t *testing.T) {
	engCfg := EngineConfig{
		Timeout: TimeoutFlags{
			VideoTimeoutMult: 2.0,
			VideoTimeout:     "10m",
		},
	}
	engine := NewEngine(nil, nil, engCfg, nil, nil, nil)

	m := models.ShrinkMedia{Category: "Video", Duration: 60}
	timeout := engine.getTimeout(m)
	if timeout.Seconds() != 120 {
		t.Errorf("expected 120s timeout, got %v", timeout.Seconds())
	}

	m.Duration = 0
	timeout = engine.getTimeout(m)
	if timeout.Minutes() != 10 {
		t.Errorf("expected 10m timeout, got %v", timeout.Minutes())
	}
}

func TestFinalizeFileSwapKeepOriginal(t *testing.T) {
	// Create a temp file
	tmpDir := t.TempDir()
	originalPath := filepath.Join(tmpDir, "original.mp4")
	if err := os.WriteFile(originalPath, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}

	e := &Engine{} // mock engine
	m := models.ShrinkMedia{Path: originalPath}
	result := models.ProcessResult{
		Success: true,
		Outputs: []models.ProcessOutputFile{
			{Path: originalPath, Size: 4},
		},
	}

	// This should NOT delete the original file
	e.finalizeFileSwap(m, result, true)

	if _, err := os.Stat(originalPath); os.IsNotExist(err) {
		t.Errorf("original file was deleted but it was in the outputs")
	}
}

func TestFinalizeFileSwapDeleteOriginal(t *testing.T) {
	// Create a temp file
	tmpDir := t.TempDir()
	originalPath := filepath.Join(tmpDir, "original.mp4")
	newPath := filepath.Join(tmpDir, "new.mp4")
	if err := os.WriteFile(originalPath, []byte("original data"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newPath, []byte("new data"), 0o644); err != nil {
		t.Fatal(err)
	}

	e := &Engine{} // mock engine
	m := models.ShrinkMedia{Path: originalPath}
	result := models.ProcessResult{
		Success: true,
		Outputs: []models.ProcessOutputFile{
			{Path: newPath, Size: 8},
		},
	}

	// This SHOULD delete the original file
	e.finalizeFileSwap(m, result, true)

	if _, err := os.Stat(originalPath); err == nil {
		t.Errorf("original file was NOT deleted but it was NOT in the outputs")
	}
	if _, err := os.Stat(newPath); err != nil {
		t.Errorf("new file was deleted but it was in the outputs: %v", err)
	}
}
