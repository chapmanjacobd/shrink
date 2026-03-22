package commands

import (
	"context"
	"log/slog"
	"testing"
)

func TestMetricsProgress(t *testing.T) {
	metrics := NewShrinkMetrics()
	metrics.RecordStarted("Video", "test.mp4")
	metrics.RecordStarted("Audio", "test.mp3")
	metrics.RecordSuccess("Video", 1000, 500, 10, 60)
	metrics.RecordFailure("Audio")
	metrics.RecordSkipped("Image")

	// Test PrintProgress (doesn't crash)
	metrics.PrintProgress()

	// Test LogSummary (doesn't crash)
	metrics.LogSummary()
}

func TestProgressLogHandler(t *testing.T) {
	metrics := NewShrinkMetrics()
	handler := NewProgressLogHandler(slog.Default().Handler(), metrics)

	if !handler.Enabled(context.TODO(), slog.LevelInfo) {
		t.Errorf("expected enabled")
	}

	// Should not crash
	handler.Handle(context.TODO(), slog.Record{Level: slog.LevelInfo, Message: "test"})
}

func TestMetricsHelpers(t *testing.T) {
	s := &MediaTypeStats{
		Success:       1,
		TotalSize:     1000,
		FutureSize:    500,
		TotalTime:     10,
		TotalDuration: 60,
	}

	if s.SpaceSaved() != 500 {
		t.Errorf("got %d", s.SpaceSaved())
	}
	if s.SpeedRatio() < 5.9 || s.SpeedRatio() > 6.1 {
		t.Errorf("got %v", s.SpeedRatio())
	}
}
