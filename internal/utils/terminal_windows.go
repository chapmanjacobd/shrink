//go:build windows

package utils

import (
	"os"
	"os/exec"
	"path/filepath"
	"sync"

	"golang.org/x/term"
)

// CommandExists checks if a command is available in PATH or common Windows installation paths
func CommandExists(name string) bool {
	if _, err := exec.LookPath(name); err == nil {
		return true
	}

	// Check common Windows installation paths for specific tools
	var searchPaths []string
	programFiles := os.Getenv("ProgramFiles")
	programFilesX86 := os.Getenv("ProgramFiles(x86)")

	switch name {
	case "ebook-convert":
		searchPaths = append(searchPaths, filepath.Join(programFiles, "Calibre2", "ebook-convert.exe"))
	case "magick", "convert":
		// ImageMagick often has version in folder name, use Glob
		if dirs, err := filepath.Glob(filepath.Join(programFiles, "ImageMagick-*")); err == nil {
			for _, dir := range dirs {
				searchPaths = append(searchPaths, filepath.Join(dir, name+".exe"))
			}
		}
	case "lsar", "unar":
		// Check for Universal Extractor or other common locations
		searchPaths = append(searchPaths, filepath.Join(programFiles, "Universal Extractor 2", "bin", name+".exe"))
		searchPaths = append(searchPaths, filepath.Join(programFilesX86, "Universal Extractor 2", "bin", name+".exe"))
	}

	for _, path := range searchPaths {
		if _, err := os.Stat(path); err == nil {
			return true
		}
	}

	return false
}

// GetCommandPath returns the absolute path to a command, searching in common Windows installation paths if not in PATH
func GetCommandPath(name string) string {
	if path, err := exec.LookPath(name); err == nil {
		return path
	}

	// Check common Windows installation paths
	var searchPaths []string
	programFiles := os.Getenv("ProgramFiles")
	programFilesX86 := os.Getenv("ProgramFiles(x86)")

	switch name {
	case "ebook-convert":
		searchPaths = append(searchPaths, filepath.Join(programFiles, "Calibre2", "ebook-convert.exe"))
	case "magick", "convert":
		if dirs, err := filepath.Glob(filepath.Join(programFiles, "ImageMagick-*")); err == nil {
			for _, dir := range dirs {
				searchPaths = append(searchPaths, filepath.Join(dir, name+".exe"))
			}
		}
	case "lsar", "unar":
		searchPaths = append(searchPaths, filepath.Join(programFiles, "Universal Extractor 2", "bin", name+".exe"))
		searchPaths = append(searchPaths, filepath.Join(programFilesX86, "Universal Extractor 2", "bin", name+".exe"))
	}

	for _, path := range searchPaths {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}

	return ""
}

// TerminalSize tracks the current terminal dimensions
type TerminalSize struct {
	mu       sync.RWMutex
	width    int
	height   int
	initOnce sync.Once
}

var terminalSize TerminalSize

// GetTerminalWidth returns the current terminal width
func GetTerminalWidth() int {
	terminalSize.initOnce.Do(func() {
		terminalSize.updateSize()
	})
	terminalSize.mu.RLock()
	defer terminalSize.mu.RUnlock()
	return terminalSize.width
}

// GetTerminalHeight returns the current terminal height
func GetTerminalHeight() int {
	terminalSize.initOnce.Do(func() {
		terminalSize.updateSize()
	})
	terminalSize.mu.RLock()
	defer terminalSize.mu.RUnlock()
	return terminalSize.height
}

// updateSize gets the current terminal dimensions
func (t *TerminalSize) updateSize() {
	w, h, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil {
		w = 80 // Fallback
		h = 24
	}
	t.mu.Lock()
	t.width = w
	t.height = h
	t.mu.Unlock()
}
