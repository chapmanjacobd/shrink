package models

import (
	"log/slog"
	"testing"
)

func TestPlainHandler(t *testing.T) {
	h := &PlainHandler{Level: LogLevel}

	// These should not panic
	h.WithAttrs(nil)
	h.WithGroup("test")
}

func TestSetupLogging(t *testing.T) {
	SetupLogging(0) // Warn
	if LogLevel.Level() != slog.LevelWarn {
		t.Errorf("expected warn")
	}

	SetupLogging(1) // Info
	if LogLevel.Level() != slog.LevelInfo {
		t.Errorf("expected info")
	}

	SetupLogging(2) // Debug
	if LogLevel.Level() != slog.LevelDebug {
		t.Errorf("expected debug")
	}
}
