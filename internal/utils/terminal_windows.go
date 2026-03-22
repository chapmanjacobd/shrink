//go:build windows

package utils

import (
	"os"
	"sync"

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
