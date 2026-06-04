package commands

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/chapmanjacobd/shrink/internal/models"
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

	// Since flattenWrapperFolders is now recursive, it should flatten BOTH wrapper and inner
	// tempDir/wrapper/inner/test.txt -> tempDir/inner/test.txt -> tempDir/test.txt
	newPath := filepath.Join(tempDir, "test.txt")
	if _, err := os.Stat(newPath); os.IsNotExist(err) {
		t.Errorf("expected flattened file missing at %s", newPath)
	}
}

func TestIsBrokenArchive(t *testing.T) {
	// Mock lsar to return empty for broken archive
	// But wait, unar/lsar are external.
	// We already tested broken archive in integration test TestShrinkBrokenArchive.
}

func TestCollectLSARPartFilesFallsBackToGlob(t *testing.T) {
	tempDir := t.TempDir()
	zipPath := filepath.Join(tempDir, "test_archive_multi.zip")
	z01Path := filepath.Join(tempDir, "test_archive_multi.z01")
	z02Path := filepath.Join(tempDir, "test_archive_multi.z02")

	for _, p := range []string{zipPath, z01Path, z02Path} {
		if err := os.WriteFile(p, []byte("test"), 0o644); err != nil {
			t.Fatalf("failed to create test file %s: %v", p, err)
		}
	}

	p := &ArchiveProcessor{cfg: &models.ProcessorConfig{}}
	partFiles := p.collectLSARPartFiles(
		zipPath,
		filepath.Dir(zipPath),
		filepath.Base(zipPath),
		map[string]bool{},
		[]string{
			filepath.Join(string(filepath.Separator), "does", "not", "exist", "test_archive_multi.z01"),
			filepath.Join(string(filepath.Separator), "does", "not", "exist", "test_archive_multi.z02"),
		},
	)

	if len(partFiles) != 2 {
		t.Fatalf("expected glob fallback to recover 2 part files, got %d: %v", len(partFiles), partFiles)
	}
	if !partFiles[z01Path] {
		t.Fatalf("expected z01 part to be recovered by glob fallback: %s", z01Path)
	}
	if !partFiles[z02Path] {
		t.Fatalf("expected z02 part to be recovered by glob fallback: %s", z02Path)
	}
}
