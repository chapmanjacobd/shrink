package main

import (
	"log/slog"
	"os"

	"github.com/alecthomas/kong"
	"github.com/chapmanjacobd/shrink/internal/commands"
	"github.com/chapmanjacobd/shrink/internal/models"
)

// CLI defines the command-line interface for shrink
type CLI struct {
	Shrink commands.ShrinkCmd `cmd:"" name:"shrink" help:"Shrink media to efficient formats (AV1/Opus/AVIF)"`
}

func main() {
	cli := &CLI{}
	parser, err := kong.New(cli,
		kong.Name("shrink"),
		kong.Description("Media shrinking tool - transcode media to efficient formats"),
		kong.UsageOnError(),
	)
	if err != nil {
		panic(err)
	}

	ctx, err := parser.Parse(os.Args[1:])
	if err != nil {
		parser.FatalIfErrorf(err)
	}

	// Configure default logger
	logger := slog.New(&models.PlainHandler{
		Level: models.LogLevel,
		Out:   os.Stderr,
	})
	slog.SetDefault(logger)

	err = ctx.Run()
	if err != nil {
		slog.Error("Command failed", "error", err)
		os.Exit(1)
	}
}
