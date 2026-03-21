package commands

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/alecthomas/kong"
	"github.com/chapmanjacobd/shrink/internal/models"
	"github.com/chapmanjacobd/shrink/internal/testutils"
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
				Name:    "test_archive.zip",
				SrcPath: "../testutils/testdata/test_archive.zip",
			},
		},
		ExpectFiles: []string{
			"test_archive.zip.extracted/tiny.av1.mkv",
			"test_archive.zip.extracted/tiny.avif",
		},
		ExpectMissing: []string{
			"test_archive.zip",
			"test_archive.zip.extracted/tiny.avi",
			"test_archive.zip.extracted/tiny.bmp",
		},
		ExpectDBState: []testutils.ExpectedDBRecord{
			{Path: "test_archive.zip", TimeDeleted: 1},
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
				MediaType: "archive/zip",
			},
			// Parts must exist on disk for unar to find them
			{
				Name:      "test_archive_multi.z01",
				SrcPath:   "../testutils/testdata/test_archive_multi.z01",
				MediaType: "application/octet-stream",
			},
			{
				Name:      "test_archive_multi.z02",
				SrcPath:   "../testutils/testdata/test_archive_multi.z02",
				MediaType: "application/octet-stream",
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
