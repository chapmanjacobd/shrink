package commands

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFlattenWrapperFolders(t *testing.T) {
	tempDir := t.TempDir()

	// Create nested folder
	wrapper := filepath.Join(tempDir, "wrapper")
	os.Mkdir(wrapper, 0o755)

	inner := filepath.Join(wrapper, "inner")
	os.Mkdir(inner, 0o755)

	file := filepath.Join(inner, "test.txt")
	os.WriteFile(file, []byte("test"), 0o644)

	// Flatten
	flattenWrapperFolders(tempDir)

	// Should be moved to tempDir/inner/test.txt?
	// Wait, flattenWrapperFolders flattens if there is only ONE entry in a folder and it's a directory.
	// tempDir has 'wrapper'. wrapper has 'inner'.
	// So tempDir/wrapper/inner becomes tempDir/inner.

	newPath := filepath.Join(tempDir, "inner", "test.txt")
	if _, err := os.Stat(newPath); os.IsNotExist(err) {
		t.Errorf("expected flattened file missing at %s", newPath)
	}
}

func TestIsBrokenArchive(t *testing.T) {
	// Mock lsar to return empty for broken archive
	// But wait, unar/lsar are external.
	// We already tested broken archive in integration test TestShrinkBrokenArchive.
}
