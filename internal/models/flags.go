package models

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
)

// PlainHandler is a simple slog handler that outputs plain text
type PlainHandler struct {
	Level *slog.LevelVar
	Out   *os.File
	Attrs []slog.Attr
}

func (h *PlainHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return level >= h.Level.Level()
}

func (h *PlainHandler) Handle(ctx context.Context, record slog.Record) error {
	var msg strings.Builder
	msg.WriteString(record.Level.String())
	msg.WriteString(" ")
	msg.WriteString(record.Message)
	for _, a := range h.Attrs {
		msg.WriteString(fmt.Sprintf("\n    %s=%v", a.Key, a.Value.Any()))
	}
	record.Attrs(func(a slog.Attr) bool {
		msg.WriteString(fmt.Sprintf("\n    %s=%v", a.Key, a.Value.Any()))
		return true
	})
	msg.WriteString("\n")
	_, err := h.Out.WriteString(msg.String())
	return err
}

func (h *PlainHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	newAttrs := make([]slog.Attr, len(h.Attrs)+len(attrs))
	copy(newAttrs, h.Attrs)
	copy(newAttrs[len(h.Attrs):], attrs)
	return &PlainHandler{Level: h.Level, Out: h.Out, Attrs: newAttrs}
}

func (h *PlainHandler) WithGroup(name string) slog.Handler {
	return h
}

var LogLevel = &slog.LevelVar{}

// SetupLogging configures logging level
func SetupLogging(verbose bool) {
	if verbose {
		LogLevel.Set(slog.LevelDebug)
	} else {
		LogLevel.Set(slog.LevelWarn)
	}
}
