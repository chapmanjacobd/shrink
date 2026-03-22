package commands

import (
	"os"
	"testing"
)

func TestConfirm(t *testing.T) {
	cmd := &ShrinkCmd{}

	// Mock stdin for "y"
	r, w, _ := os.Pipe()
	oldStdin := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = oldStdin }()

	go func() {
		w.Write([]byte("y\n"))
		w.Close()
	}()

	if !cmd.Confirm() {
		t.Errorf("expected true for 'y'")
	}
}
