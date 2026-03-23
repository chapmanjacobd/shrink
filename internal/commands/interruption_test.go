package commands

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chapmanjacobd/shrink/internal/models"
)

func TestInterruptionPreservesOriginal(t *testing.T) {
	// Setup temporary directory
	tmpDir, err := os.MkdirTemp("", "interruption-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Case 1: TextProcessor with OCR failure
	t.Run("TextProcessor_OCR_Failure", func(t *testing.T) {
		pdfPath := filepath.Join(tmpDir, "test.pdf")
		os.WriteFile(pdfPath, []byte("original pdf content"), 0o644)

		// Mock ocrmypdf to fail
		binDir := filepath.Join(tmpDir, "bin")
		os.MkdirAll(binDir, 0o755)
		os.WriteFile(filepath.Join(binDir, "ocrmypdf"), []byte("#!/bin/sh\nexit 1"), 0o755)

		oldPath := os.Getenv("PATH")
		os.Setenv("PATH", binDir+":"+oldPath)
		defer os.Setenv("PATH", oldPath)

		p := NewTextProcessor()
		cfg := &models.ProcessorConfig{
			Text: models.TextConfig{ForceOCR: true},
		}

		// This will call runOCR which we want to NOT delete the original if it fails
		// But runOCR actually handles its own failure and returns ""
		res := p.runOCR(pdfPath, cfg)

		if res != "" {
			t.Errorf("expected empty result on OCR failure")
		}

		if _, err := os.Stat(pdfPath); os.IsNotExist(err) {
			t.Errorf("original pdf was deleted on OCR failure")
		}
	})

	// Case 2: TextProcessor interrupted during ebook-convert
	t.Run("TextProcessor_Convert_Interrupted", func(t *testing.T) {
		epubPath := filepath.Join(tmpDir, "test.epub")
		os.WriteFile(epubPath, []byte("original epub content"), 0o644)

		// Mock ebook-convert to sleep then we cancel
		binDir := filepath.Join(tmpDir, "bin-convert")
		os.MkdirAll(binDir, 0o755)
		os.WriteFile(filepath.Join(binDir, "ebook-convert"), []byte("#!/bin/sh\nsleep 10"), 0o755)

		oldPath := os.Getenv("PATH")
		os.Setenv("PATH", binDir+":"+oldPath)
		defer os.Setenv("PATH", oldPath)

		p := NewTextProcessor()
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()

		cfg := &models.ProcessorConfig{}
		res := p.Process(ctx, &models.ShrinkMedia{Path: epubPath}, cfg, nil)

		if res.Error == nil {
			t.Errorf("expected error on interruption")
		}

		// Simulate orchestrator handling this error
		eng := &Engine{cfg: cfg}
		eng.finalizeFileSwap(models.ShrinkMedia{Path: epubPath}, res, false)

		if _, err := os.Stat(epubPath); os.IsNotExist(err) {
			t.Errorf("original epub was deleted on interruption")
		}
	})

	// Case 3: ArchiveProcessor interrupted
	t.Run("ArchiveProcessor_Interrupted", func(t *testing.T) {
		zipPath := filepath.Join(tmpDir, "test.zip")
		os.WriteFile(zipPath, []byte("original zip content"), 0o644)
		z01Path := filepath.Join(tmpDir, "test.z01")
		os.WriteFile(z01Path, []byte("zip part content"), 0o644)

		// Mock unar/lsar
		binDir := filepath.Join(tmpDir, "bin-archive")
		os.MkdirAll(binDir, 0o755)
		os.WriteFile(filepath.Join(binDir, "lsar"), []byte("#!/bin/sh\necho '{\"lsarContents\":[]}'"), 0o755)
		os.WriteFile(filepath.Join(binDir, "unar"), []byte("#!/bin/sh\nsleep 10"), 0o755)

		oldPath := os.Getenv("PATH")
		os.Setenv("PATH", binDir+":"+oldPath)
		defer os.Setenv("PATH", oldPath)

		p := NewArchiveProcessor(nil)
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()

		cfg := &models.ProcessorConfig{}
		res := p.Process(ctx, &models.ShrinkMedia{Path: zipPath, Ext: ".zip"}, cfg, nil)

		if res.Error == nil {
			t.Errorf("expected error on interruption")
		}

		// Simulate orchestrator handling this error
		eng := &Engine{cfg: cfg}
		eng.finalizeFileSwap(models.ShrinkMedia{Path: zipPath}, res, false)

		if _, err := os.Stat(zipPath); os.IsNotExist(err) {
			t.Errorf("original zip was deleted on interruption")
		}
		if _, err := os.Stat(z01Path); os.IsNotExist(err) {
			t.Errorf("zip part was deleted on interruption")
		}
	})
}
