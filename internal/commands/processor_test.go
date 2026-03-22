package commands

import (
	"testing"

	"github.com/chapmanjacobd/shrink/internal/models"
)

func TestShouldShrink(t *testing.T) {
	cfg := &models.ProcessorConfig{
		Video: models.VideoConfig{MinSavingsVideo: 0.1},
	}
	m := &models.ShrinkMedia{Category: "Video", Size: 1000}

	// Savings 20% (1000 -> 800)
	if !m.ShouldShrink(800, cfg) {
		t.Errorf("expected true for 20%% savings")
	}

	// Savings 5% (1000 -> 950)
	if m.ShouldShrink(950, cfg) {
		t.Errorf("expected false for 5%% savings")
	}

	// Byte threshold: 100 bytes
	cfg.Video.MinSavingsVideo = 100.0
	if !m.ShouldShrink(800, cfg) {
		t.Errorf("expected true for 200 bytes savings")
	}
}

func TestProcessorRegistry(t *testing.T) {
	registry := NewProcessorRegistry(nil)

	m := &models.ShrinkMedia{Category: "Video", MediaType: "video/mp4", Ext: ".mp4", VideoCount: 1}
	p := registry.GetProcessor(m)
	if _, ok := p.(*VideoProcessor); !ok {
		t.Errorf("expected VideoProcessor")
	}

	m = &models.ShrinkMedia{Category: "Audio", MediaType: "audio/mpeg", Ext: ".mp3", AudioCount: 1}
	p = registry.GetProcessor(m)
	if _, ok := p.(*AudioProcessor); !ok {
		t.Errorf("expected AudioProcessor")
	}

	m = &models.ShrinkMedia{Category: "Image", MediaType: "image/jpeg", Ext: ".jpg"}
	p = registry.GetProcessor(m)
	if _, ok := p.(*ImageProcessor); !ok {
		t.Errorf("expected ImageProcessor")
	}

	m = &models.ShrinkMedia{Category: "Text", Ext: ".epub"}
	p = registry.GetProcessor(m)
	if _, ok := p.(*TextProcessor); !ok {
		t.Errorf("expected TextProcessor")
	}

	m = &models.ShrinkMedia{Category: "Archived", Ext: ".zip"}
	p = registry.GetProcessor(m)
	if _, ok := p.(*ArchiveProcessor); !ok {
		t.Errorf("expected ArchiveProcessor")
	}
}
