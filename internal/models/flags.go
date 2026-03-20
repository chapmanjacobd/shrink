package models

import (
	"context"
	"log/slog"
	"os"
)

// PlainHandler is a simple slog handler that outputs plain text
type PlainHandler struct {
	Level *slog.LevelVar
	Out   *os.File
}

func (h *PlainHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return level >= h.Level.Level()
}

func (h *PlainHandler) Handle(ctx context.Context, record slog.Record) error {
	_, err := h.Out.WriteString(record.Level.String() + " " + record.Message + "\n")
	return err
}

func (h *PlainHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return h
}

func (h *PlainHandler) WithGroup(name string) slog.Handler {
	return h
}

// CoreFlags are essential flags shared across commands
type CoreFlags struct {
	Verbose   bool   `short:"v" help:"Enable verbose logging"`
	Simulate  bool   `help:"Dry run; don't actually do anything"`
	NoConfirm bool   `short:"y" help:"Don't ask for confirmation"`
}

// PathFilterFlags for path-based filtering
type PathFilterFlags struct {
	Include []string `short:"s" help:"Include paths matching pattern" group:"PathFilter"`
	Exclude []string `short:"E" help:"Exclude paths matching pattern" group:"PathFilter"`
}

// FilterFlags for general filtering
type FilterFlags struct {
	Search []string `help:"Search terms" group:"Filter"`
}

// MediaFilterFlags for media type filtering
type MediaFilterFlags struct {
	VideoOnly bool `help:"Only video files" group:"MediaFilter"`
	AudioOnly bool `help:"Only audio files" group:"MediaFilter"`
	ImageOnly bool `help:"Only image files" group:"MediaFilter"`
	TextOnly  bool `help:"Only text/ebook files" group:"MediaFilter"`
}

// TimeFilterFlags for time-based filtering
type TimeFilterFlags struct {
	CreatedAfter   string `help:"Created after date" group:"Time"`
	ModifiedAfter  string `help:"Modified after date" group:"Time"`
	ModifiedBefore string `help:"Modified before date" group:"Time"`
}

// DeletedFlags for deleted file filtering
type DeletedFlags struct {
	HideDeleted bool `default:"true" help:"Exclude deleted files" group:"Deleted"`
	OnlyDeleted bool `help:"Include only deleted files" group:"Deleted"`
}

var LogLevel = &slog.LevelVar{}

// SetupLogging configures logging level
func SetupLogging(verbose bool) {
	if verbose {
		LogLevel.Set(slog.LevelDebug)
	} else {
		LogLevel.Set(slog.LevelInfo)
	}
}
