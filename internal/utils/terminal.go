// Package utils provides common utility functions for file systems and terminal interaction.
package utils

import (
	"os"
	"os/signal"
	"sync"
	"syscall"

	"golang.org/x/term"
)

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
		terminalSize.watchResize()
	})
	terminalSize.mu.RLock()
	defer terminalSize.mu.RUnlock()
	return terminalSize.width
}

// GetTerminalHeight returns the current terminal height
func GetTerminalHeight() int {
	terminalSize.initOnce.Do(func() {
		terminalSize.updateSize()
		terminalSize.watchResize()
	})
	terminalSize.mu.RLock()
	defer terminalSize.mu.RUnlock()
	return terminalSize.height
}

// watchResize listens for terminal resize events
func (t *TerminalSize) watchResize() {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGWINCH)
	go func() {
		for range sigChan {
			t.updateSize()
		}
	}()
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

// TruncateMiddle truncates a string in the middle with ellipsis
func TruncateMiddle(s string, max int) string {
	if s == "" {
		return ""
	}
	if len(s) <= max {
		return s
	}
	half := max / 2
	if half < 2 {
		half = 2
	}
	return s[:half-1] + "…" + s[len(s)-half+1:]
}
