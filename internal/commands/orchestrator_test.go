package commands

import (
	"testing"

	"github.com/chapmanjacobd/shrink/internal/models"
)

func TestSortByEfficiency(t *testing.T) {
	cmd := &ShrinkCmd{}
	media := []models.ShrinkMedia{
		{Path: "slow", Savings: 1000, ProcessingTime: 100}, // 10 bytes/sec
		{Path: "fast", Savings: 1000, ProcessingTime: 10},  // 100 bytes/sec
	}
	cmd.sortByEfficiency(media)
	if media[0].Path != "fast" {
		t.Errorf("expected fast first, got %s", media[0].Path)
	}
}

func TestGetTimeout(t *testing.T) {
	cmd := &ShrinkCmd{}
	cmd.VideoTimeoutMult = 2.0
	cmd.VideoTimeout = "10m"
	engine := NewEngine(cmd, nil, nil, nil)

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
