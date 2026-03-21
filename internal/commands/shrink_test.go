package commands

import (
	"database/sql"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/alecthomas/kong"
	"github.com/chapmanjacobd/shrink/internal/models"
	"github.com/chapmanjacobd/shrink/internal/testutils"
	_ "github.com/mattn/go-sqlite3"
)

func runShrinkCmd(dbPath, tempDir string, args []string) error {
	// Prepend dbPath to args since it's a positional argument
	fullArgs := append([]string{dbPath}, args...)

	// Configure default logger for tests
	logger := slog.New(&models.PlainHandler{
		Level: models.LogLevel,
		Out:   os.Stderr,
	})
	slog.SetDefault(logger)

	cmd := &ShrinkCmd{}
	parser, err := kong.New(cmd)
	if err != nil {
		return err
	}

	ctx, err := parser.Parse(fullArgs)
	if err != nil {
		return err
	}

	cmd.DeleteLarger = true

	// We run it synchronously
	return cmd.Run(ctx)
}

// runShrinkCmdDir runs the shrink command with a directory path instead of database
func runShrinkCmdDir(dirPath, tempDir string, args []string) error {
	// Prepend dirPath to args since it's a positional argument
	fullArgs := append([]string{dirPath}, args...)

	// Configure default logger for tests
	logger := slog.New(&models.PlainHandler{
		Level: models.LogLevel,
		Out:   os.Stderr,
	})
	slog.SetDefault(logger)

	cmd := &ShrinkCmd{}
	parser, err := kong.New(cmd)
	if err != nil {
		return err
	}

	ctx, err := parser.Parse(fullArgs)
	if err != nil {
		return err
	}

	cmd.DeleteLarger = true

	// We run it synchronously
	return cmd.Run(ctx)
}

func TestShrinkVideo(t *testing.T) {
	scenario := testutils.Scenario{
		Description: "Shrinking a small video file replaces it with a smaller AV1 version",
		CLIArgs:     []string{"--no-confirm", "--preset=7", "--crf=40"},
		InputFiles: []testutils.TestFile{
			{
				Name:      "test_vid.avi",
				SrcPath:   "../testutils/testdata/tiny.avi",
				MediaType: "video/x-msvideo",
			},
		},
		ExpectFiles: []string{
			"test_vid.av1.mkv",
		},
		ExpectMissing: []string{
			"test_vid.avi",
		},
		ExpectDBState: []testutils.ExpectedDBRecord{
			{Path: "test_vid.av1.mkv", TimeDeleted: 0, IsShrinked: 1},
		},
	}

	testutils.RunScenario(t, scenario, runShrinkCmd)
}

func TestShrinkArchive(t *testing.T) {
	scenario := testutils.Scenario{
		Description: "Extracting an archive with a video shrinks the video and deletes the archive",
		CLIArgs:     []string{"--no-confirm", "--preset=7", "--crf=40"},
		Archives: []testutils.TestArchive{
			{
				Name:    "test_archive_simple.zip",
				SrcPath: "../testutils/testdata/test_archive_simple.zip",
			},
		},
		ExpectFiles: []string{
			"test_archive_simple.zip.extracted/tiny.av1.mkv",
			"test_archive_simple.zip.extracted/tiny.avif",
		},
		ExpectMissing: []string{
			"test_archive_simple.zip",
			"test_archive_simple.zip.extracted/tiny.avi",
			"test_archive_simple.zip.extracted/tiny.bmp",
		},
		ExpectDBState: []testutils.ExpectedDBRecord{
			{Path: "test_archive_simple.zip", TimeDeleted: 1},
		},
	}

	testutils.RunScenario(t, scenario, runShrinkCmd)
}

func TestShrinkText(t *testing.T) {
	scenario := testutils.Scenario{
		Description: "Converting an EPUB with BMP images converts images to AVIF and optimizes CSS",
		CLIArgs:     []string{"--no-confirm", "--min-savings-image=0", "--target-image-size=1KB"},
		InputFiles: []testutils.TestFile{
			{
				Name:      "test.epub",
				SrcPath:   "../testutils/testdata/test.epub",
				MediaType: "application/epub+zip",
			},
		},
		ExpectFiles: []string{
			"test.epub.OEB",
		},
		ExpectMissing: []string{
			"test.epub",
		},
		ExpectDBState: []testutils.ExpectedDBRecord{
			{Path: "test.epub.OEB", TimeDeleted: 0, IsShrinked: 1},
		},
	}

	testutils.RunScenario(t, scenario, runShrinkCmd)
}

func TestShrinkArchiveNested(t *testing.T) {
	scenario := testutils.Scenario{
		Description: "Extracting a nested archive flattens wrapper folders and processes media",
		CLIArgs:     []string{"--no-confirm", "--preset=7", "--crf=40"},
		Archives: []testutils.TestArchive{
			{
				Name:    "test_archive_nested.zip",
				SrcPath: "../testutils/testdata/test_archive_nested.zip",
			},
		},
		ExpectFiles: []string{
			"test_archive_nested.zip.extracted/1/tiny.av1.mkv",
			"test_archive_nested.zip.extracted/tiny.mka",
		},
		ExpectMissing: []string{
			"test_archive_nested.zip",
			"test_archive_nested.zip.extracted/1/tiny.avi",
			"test_archive_nested.zip.extracted/tiny.wav",
		},
		ExpectDBState: []testutils.ExpectedDBRecord{
			{Path: "test_archive_nested.zip", TimeDeleted: 1},
		},
	}

	testutils.RunScenario(t, scenario, runShrinkCmd)
}

func TestShrinkDirectory(t *testing.T) {
	// Create a scenario that tests directory scanning instead of database
	tempDir := t.TempDir()

	// Copy test files to temp directory
	videoPath := filepath.Join(tempDir, "test_vid.avi")
	copyFile(t, "../testutils/testdata/tiny.avi", videoPath)

	// Run shrink command with directory path
	args := []string{"--no-confirm", "--preset=7", "--crf=40"}
	err := runShrinkCmdDir(tempDir, tempDir, args)
	if err != nil {
		t.Fatalf("command failed: %v", err)
	}

	// Check that the video was processed
	expectedOutput := filepath.Join(tempDir, "test_vid.av1.mkv")
	if _, err := os.Stat(expectedOutput); os.IsNotExist(err) {
		t.Errorf("expected output file missing: %s", expectedOutput)
	}

	// Check that the original was deleted
	if _, err := os.Stat(videoPath); err == nil {
		t.Errorf("expected original file to be deleted: %s", videoPath)
	}
}

func TestShrinkMultiPartArchive(t *testing.T) {
	// Multi-part archives are detected via lsar XADVolumes
	// Only the main .zip file needs to be in the database
	scenario := testutils.Scenario{
		Description: "Multi-part archive extracts, processes media, and deletes all parts",
		CLIArgs:     []string{"--no-confirm", "--preset=7", "--crf=40"},
		// Only insert the main .zip file - parts are detected automatically
		InputFiles: []testutils.TestFile{
			{
				Name:      "test_archive_multi.zip",
				SrcPath:   "../testutils/testdata/test_archive_multi.zip",
				MediaType: "archive",
			},
			// Parts must exist on disk for unar to find them
			{
				Name:      "test_archive_multi.z01",
				SrcPath:   "../testutils/testdata/test_archive_multi.z01",
				MediaType: "archive",
			},
			{
				Name:      "test_archive_multi.z02",
				SrcPath:   "../testutils/testdata/test_archive_multi.z02",
				MediaType: "archive",
			},
		},
		ExpectFiles: []string{
			"test_archive_multi.zip.extracted/tiny.av1.mkv",
			"test_archive_multi.zip.extracted/tiny.avif",
		},
		ExpectMissing: []string{
			"test_archive_multi.z01",
			"test_archive_multi.z02",
			"test_archive_multi.zip",
			"test_archive_multi.zip.extracted/tiny.avi",
			"test_archive_multi.zip.extracted/tiny.bmp",
			"test_archive_multi.zip.extracted/tiny.wav",
		},
		ExpectDBState: []testutils.ExpectedDBRecord{
			{Path: "test_archive_multi.zip", TimeDeleted: 1},
		},
	}

	testutils.RunScenario(t, scenario, runShrinkCmd)
}

func TestShrinkBrokenArchive(t *testing.T) {
	// Test that broken/incomplete archives (lsar returns empty) are moved to --move-broken
	tempDir := t.TempDir()
	moveBrokenDir := filepath.Join(tempDir, "broken")

	// Copy multi-part archive files but remove one part to make it broken
	copyFile(t, "../testutils/testdata/test_archive_multi.zip", filepath.Join(tempDir, "test_archive_multi.zip"))
	copyFile(t, "../testutils/testdata/test_archive_multi.z02", filepath.Join(tempDir, "test_archive_multi.z02"))
	// Intentionally NOT copying .z01 to make it broken

	// Create database
	dbPath := filepath.Join(tempDir, "test.db")
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}

	_, err = db.Exec(`CREATE TABLE media (
		path TEXT PRIMARY KEY,
		size INTEGER,
		duration REAL,
		video_count INTEGER,
		audio_count INTEGER,
		video_codecs TEXT,
		audio_codecs TEXT,
		subtitle_codecs TEXT,
		media_type TEXT,
		time_deleted INTEGER DEFAULT 0,
		is_shrinked INTEGER DEFAULT 0
	)`)
	if err != nil {
		t.Fatalf("failed to create table: %v", err)
	}

	archivePath := filepath.Join(tempDir, "test_archive_multi.zip")
	info, _ := os.Stat(archivePath)
	_, err = db.Exec(`INSERT INTO media (path, size, media_type) VALUES (?, ?, ?)`,
		archivePath, info.Size(), "archive")
	if err != nil {
		t.Fatalf("failed to insert media: %v", err)
	}
	db.Close()

	// Run shrink - extraction should fail due to missing part
	args := []string{"--no-confirm", "--move-broken", moveBrokenDir}
	_ = runShrinkCmd(dbPath, tempDir, args)

	// Check that the archive was moved to broken directory with tidy structure
	parentFolder := filepath.Base(tempDir)
	brokenSubdir := filepath.Join(moveBrokenDir, parentFolder)

	// The .zip and .z02 should be moved to broken
	parts := []string{"test_archive_multi.zip", "test_archive_multi.z02"}
	for _, part := range parts {
		movedPath := filepath.Join(brokenSubdir, part)
		if _, err := os.Stat(movedPath); os.IsNotExist(err) {
			t.Errorf("expected broken archive part in broken dir: %s", movedPath)
		}
	}
}

func TestShrinkArchiveKeep(t *testing.T) {
	// Test that archives with no processable content are skipped (not moved to broken)
	tempDir := t.TempDir()
	moveBrokenDir := filepath.Join(tempDir, "broken")

	// Copy archive with non-media content
	copyFile(t, "../testutils/testdata/test_archive_keep.zip", filepath.Join(tempDir, "test_archive_keep.zip"))

	// Create database
	dbPath := filepath.Join(tempDir, "test.db")
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}

	_, err = db.Exec(`CREATE TABLE media (
		path TEXT PRIMARY KEY,
		size INTEGER,
		duration REAL,
		video_count INTEGER,
		audio_count INTEGER,
		video_codecs TEXT,
		audio_codecs TEXT,
		subtitle_codecs TEXT,
		media_type TEXT,
		time_deleted INTEGER DEFAULT 0,
		is_shrinked INTEGER DEFAULT 0
	)`)
	if err != nil {
		t.Fatalf("failed to create table: %v", err)
	}

	archivePath := filepath.Join(tempDir, "test_archive_keep.zip")
	info, _ := os.Stat(archivePath)
	_, err = db.Exec(`INSERT INTO media (path, size, media_type) VALUES (?, ?, ?)`,
		archivePath, info.Size(), "archive")
	if err != nil {
		t.Fatalf("failed to insert media: %v", err)
	}
	db.Close()

	// Run shrink with --move-broken - archive should NOT be moved because it has content (just not processable)
	args := []string{"--no-confirm", "--move-broken", moveBrokenDir}
	_ = runShrinkCmd(dbPath, tempDir, args)

	// Archive should still exist in original location (not moved to broken)
	if _, err := os.Stat(archivePath); os.IsNotExist(err) {
		t.Errorf("archive should not be moved to broken: %s", archivePath)
	}

	// Broken directory should be empty or not exist
	if _, err := os.Stat(moveBrokenDir); err == nil {
		entries, _ := os.ReadDir(moveBrokenDir)
		if len(entries) > 0 {
			t.Errorf("move-broken dir should be empty but has: %v", entries)
		}
	}
}

func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	err := os.MkdirAll(filepath.Dir(dst), 0o755)
	if err != nil {
		t.Fatal(err)
	}
	in, err := os.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	if err != nil {
		t.Fatal(err)
	}
}
