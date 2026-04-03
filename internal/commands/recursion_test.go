package commands

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/chapmanjacobd/shrink/internal/ffmpeg"
	"github.com/chapmanjacobd/shrink/internal/models"
)

type mockRegistry struct {
	processors []models.MediaProcessor
}

func (r *mockRegistry) GetProcessor(m *models.ShrinkMedia) models.MediaProcessor {
	if m.Category != "" {
		for _, p := range r.processors {
			if p.Category() == m.Category {
				return p
			}
		}
	}
	for _, p := range r.processors {
		if p.CanProcess(m) {
			return p
		}
	}
	return nil
}

func TestGIFMutualRecursion(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gif-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	gifPath := filepath.Join(tmpDir, "test.gif")
	if err := os.WriteFile(gifPath, []byte("fake gif content"), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := &models.ProcessorConfig{}
	ff := ffmpeg.NewFFmpegProcessor(cfg)
	videoProc := NewVideoProcessor(ff)
	imageProc := NewImageProcessor()

	registry := &mockRegistry{
		processors: []models.MediaProcessor{videoProc, imageProc},
	}

	m := &models.ShrinkMedia{
		Path:     gifPath,
		Ext:      ".gif",
		Category: "Video",
	}

	_ = registry.GetProcessor(m)
}
