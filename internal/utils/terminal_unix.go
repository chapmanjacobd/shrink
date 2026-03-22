//go:build !windows

package utils

import (
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"syscall"

	"golang.org/x/term"
)

// CommandExists checks if a command is available in PATH
func CommandExists(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// GetCommandPath returns the absolute path to a command
func GetCommandPath(name string) string {
	path, err := exec.LookPath(name)
	if err != nil {
		return ""
	}
	return path
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
