//go:build windows

package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chapmanjacobd/shrink/internal/models"
)

func TestCollectLSARPartFilesNormalizesMSYSPaths(t *testing.T) {
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
			toMSYSPath(zipPath),
			toMSYSPath(z01Path),
			toMSYSPath(z02Path),
		},
	)

	if len(partFiles) != 2 {
		t.Fatalf("expected 2 part files, got %d: %v", len(partFiles), partFiles)
	}
	if !partFilesContain(partFiles, z01Path) {
		t.Fatalf("expected z01 part to be detected: %s", z01Path)
	}
	if !partFilesContain(partFiles, z02Path) {
		t.Fatalf("expected z02 part to be detected: %s", z02Path)
	}
}

func toMSYSPath(p string) string {
	vol := filepath.VolumeName(p)
	drive := vol[:1]
	rest := strings.TrimPrefix(p, vol)
	rest = strings.ReplaceAll(rest, `\`, "/")
	return "/" + strings.ToLower(drive) + rest
}
