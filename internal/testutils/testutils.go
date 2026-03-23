// Package testutils provides helper functions and types for testing the shrink application.
package testutils

import (
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// TestFile represents a file used in natural language testing scenarios
type TestFile struct {
	Name      string
	SrcPath   string // Path to tiny media in testdata
	MediaType string
}

type TestArchive struct {
	Name    string
	SrcPath string
}

type ExpectedDBRecord struct {
	Path        string
	TimeDeleted int64
	IsShrinked  int
}

type Scenario struct {
	Description   string
	CLIArgs       []string
	InputFiles    []TestFile
	Archives      []TestArchive
	ExpectFiles   []string
	ExpectMissing []string
	ExpectDBState []ExpectedDBRecord
}

func RunScenario(t *testing.T, s Scenario, runCmd func(dbPath, tempDir string, args []string) error) {
	// 1. Setup temp dir
	tempDir := t.TempDir()

	// 2. Setup SQLite db
	dbPath := filepath.Join(tempDir, "test.db")
	db, err := sql.Open("sqlite", dbPath)
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

	// 3. Populate db and files
	for _, f := range s.InputFiles {
		dest := filepath.Join(tempDir, f.Name)
		copyFile(t, f.SrcPath, dest)
		info, _ := os.Stat(dest)
		insertMedia(t, db, dest, info.Size(), f.MediaType)
	}

	for _, a := range s.Archives {
		dest := filepath.Join(tempDir, a.Name)
		copyFile(t, a.SrcPath, dest)
		info, _ := os.Stat(dest)
		insertMedia(t, db, dest, info.Size(), "archive/zip")
	}

	// Close DB so the command under test can open it without locking issues
	db.Close()

	// 4. Run the command
	err = runCmd(dbPath, tempDir, s.CLIArgs)
	if err != nil {
		t.Fatalf("command failed: %v", err)
	}

	// 5. Re-open DB for Assertions
	db, err = sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("failed to re-open db: %v", err)
	}
	defer db.Close()

	// Files that MUST exist
	for _, expectedFile := range s.ExpectFiles {
		path := filepath.Join(tempDir, filepath.FromSlash(expectedFile))
		if _, err := os.Stat(path); os.IsNotExist(err) {
			// Log folder contents for debugging
			dir := filepath.Dir(path)
			entries, _ := os.ReadDir(dir)
			var names []string
			for _, e := range entries {
				names = append(names, e.Name())
			}
			t.Errorf("expected file missing: %s. Folder %s contains: %v", expectedFile, dir, names)
		}
	}

	// Files that MUST be deleted
	for _, missingFile := range s.ExpectMissing {
		path := filepath.Join(tempDir, filepath.FromSlash(missingFile))
		if _, err := os.Stat(path); err == nil {
			// Log folder contents for debugging
			dir := filepath.Dir(path)
			entries, _ := os.ReadDir(dir)
			var names []string
			for _, e := range entries {
				names = append(names, e.Name())
			}
			t.Errorf("expected file to be missing, but it exists: %s. Folder %s contains: %v", missingFile, dir, names)
		}
	}

	// Database state
	for _, expectedDB := range s.ExpectDBState {
		path := filepath.Join(tempDir, filepath.FromSlash(expectedDB.Path))
		var td int64
		var isShrinked int
		err := db.QueryRow("SELECT time_deleted, is_shrinked FROM media WHERE path = ?", path).Scan(&td, &isShrinked)
		if err == sql.ErrNoRows {
			t.Errorf("expected db record missing for: %s", path)
			continue
		} else if err != nil {
			t.Errorf("db query error for %s: %v", path, err)
			continue
		}

		if expectedDB.TimeDeleted > 0 && td == 0 {
			t.Errorf("db record %s time_deleted mismatch: expected deleted, got not deleted", path)
		} else if expectedDB.TimeDeleted == 0 && td > 0 {
			t.Errorf("db record %s time_deleted mismatch: expected not deleted, got deleted", path)
		}

		if expectedDB.IsShrinked != isShrinked {
			t.Errorf("db record %s is_shrinked mismatch: expected %d, got %d", path, expectedDB.IsShrinked, isShrinked)
		}
	}
}

func copyFile(t *testing.T, src, dst string) {
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

func insertMedia(t *testing.T, db *sql.DB, path string, size int64, mediaType string) {
	duration := 10.0 // Give it a decent duration so bitrates calculate a small future size
	videoCount := 0
	audioCount := 0
	lowerType := strings.ToLower(mediaType)
	lowerPath := strings.ToLower(path)
	if strings.Contains(lowerType, "video") || strings.HasSuffix(lowerPath, ".mp4") || strings.HasSuffix(lowerPath, ".avi") {
		videoCount = 1
	}
	if strings.Contains(lowerType, "audio") || strings.HasSuffix(lowerPath, ".mp3") || strings.HasSuffix(lowerPath, ".wav") {
		audioCount = 1
	}

	_, err := db.Exec(`INSERT INTO media (path, size, duration, video_count, audio_count, media_type) VALUES (?, ?, ?, ?, ?, ?)`, path, size, duration, videoCount, audioCount, mediaType)
	if err != nil {
		t.Fatal(err)
	}
}
