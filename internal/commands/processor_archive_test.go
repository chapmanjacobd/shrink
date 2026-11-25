package commands

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/chapmanjacobd/shrink/internal/models"
)

func TestFlattenWrapperFolders(t *testing.T) {
	tempDir := t.TempDir()

	// Create nested folder
	wrapper := filepath.Join(tempDir, "wrapper")
	_ = os.Mkdir(wrapper, 0o755)

	inner := filepath.Join(wrapper, "inner")
	_ = os.Mkdir(inner, 0o755)

	file := filepath.Join(inner, "test.txt")
	_ = os.WriteFile(file, []byte("test"), 0o644)

	// Flatten
	flattenWrapperFolders(tempDir)

	// Since flattenWrapperFolders is now recursive, it should flatten BOTH wrapper and inner
	// tempDir/wrapper/inner/test.txt -> tempDir/inner/test.txt -> tempDir/test.txt
	newPath := filepath.Join(tempDir, "test.txt")
	if _, err := os.Stat(newPath); os.IsNotExist(err) {
		t.Errorf("expected flattened file missing at %s", newPath)
	}
}

func TestExtractAndProcessDepthLimitReportsFailure(t *testing.T) {
	p := &ArchiveProcessor{cfg: &models.ProcessorConfig{}}
	result := p.ExtractAndProcess(
		context.Background(),
		&models.ShrinkMedia{Path: "/tmp/nested.zip", Ext: ".zip"},
		&models.ProcessorConfig{},
		nil, nil, nil, 11,
	)
	if result.Error == nil {
		t.Fatal("expected recursion depth error")
	}
	if result.Success {
		t.Fatal("depth limit must not report success, otherwise the archive is deleted")
	}
}

func TestExtractAndProcessSecondaryPartReportsFailure(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "archive.zip")
	partPath := filepath.Join(dir, "archive.z01")
	for _, path := range []string{zipPath, partPath} {
		if err := os.WriteFile(path, []byte("data"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	p := &ArchiveProcessor{cfg: &models.ProcessorConfig{}}
	result := p.ExtractAndProcess(
		context.Background(),
		&models.ShrinkMedia{Path: partPath, Ext: ".z01"},
		&models.ProcessorConfig{},
		nil, nil, nil, 0,
	)
	if result.Error == nil {
		t.Fatal("expected secondary part error")
	}
	if result.Success {
		t.Fatal("secondary part must not report success")
	}
}

func TestEstimateContentsCountsAllExtractedFiles(t *testing.T) {
	cfg := &models.ProcessorConfig{
		Video: models.VideoConfig{
			TargetVideoBitrate:   800000,
			TranscodingVideoRate: 10,
			MaxVideoWidth:        1440,
			MaxVideoHeight:       960,
		},
		Audio: models.AudioConfig{
			TargetAudioBitrate:   128000,
			TranscodingAudioRate: 100,
		},
		Image: models.ImageConfig{
			TargetImageSize:      30 * 1024,
			TranscodingImageTime: 1,
		},
		Common: models.CommonConfig{
			SourceVideoBitrate: 1500000,
			SourceAudioBitrate: 256000,
		},
	}
	p := &ArchiveProcessor{cfg: cfg}

	t.Run("preserved files are included", func(t *testing.T) {
		contents := []models.ShrinkMedia{
			{Path: "video.mp4", Ext: ".mp4", Size: 10 * 1024 * 1024},
			{Path: "notes.txt", Ext: ".txt", Size: 20 * 1024 * 1024},
		}

		future, _, processable := p.estimateContents(contents, cfg)
		if !processable {
			t.Fatal("expected archive to have processable content")
		}
		if future < 20*1024*1024 {
			t.Errorf("future size %d does not include preserved file", future)
		}
		if future >= 30*1024*1024 {
			t.Errorf("future size %d should be less than original contents", future)
		}
	})

	t.Run("unprocessable-only archive is not processable", func(t *testing.T) {
		contents := []models.ShrinkMedia{
			{Path: "notes.txt", Ext: ".txt", Size: 20 * 1024 * 1024},
			{Path: "song.opus", Ext: ".opus", Size: 5 * 1024 * 1024},
		}

		future, _, processable := p.estimateContents(contents, cfg)
		if processable {
			t.Fatal("expected archive without processable content to be skipped")
		}
		if want := int64(25 * 1024 * 1024); future != want {
			t.Errorf("future size = %d, want %d", future, want)
		}
	})
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
	if !partFilesContain(partFiles, z01Path) {
		t.Fatalf("expected z01 part to be recovered by glob fallback: %s", z01Path)
	}
	if !partFilesContain(partFiles, z02Path) {
		t.Fatalf("expected z02 part to be recovered by glob fallback: %s", z02Path)
	}
}

func TestCollectLSARPartFilesFallsBackWhenLSAROnlyReportsMainArchive(t *testing.T) {
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
		[]string{zipPath},
	)

	if len(partFiles) != 2 {
		t.Fatalf("expected glob fallback to recover 2 part files when lsar only reports the main archive, got %d: %v", len(partFiles), partFiles)
	}
	if !partFilesContain(partFiles, z01Path) {
		t.Fatalf("expected z01 part to be recovered when lsar only reports the main archive: %s", z01Path)
	}
	if !partFilesContain(partFiles, z02Path) {
		t.Fatalf("expected z02 part to be recovered when lsar only reports the main archive: %s", z02Path)
	}
}

func partFilesContain(partFiles map[string]bool, want string) bool {
	for partFile := range partFiles {
		if pathsEqual(partFile, want) {
			return true
		}
	}
	return false
}
