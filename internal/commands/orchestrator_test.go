package commands

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	dbpkg "github.com/chapmanjacobd/shrink/internal/db"
	"github.com/chapmanjacobd/shrink/internal/models"
)

func TestSortByEfficiency(t *testing.T) {
	cmd := &ShrinkCmd{}
	media := []models.ShrinkMedia{
		{Path: "slow", Savings: 1000, ProcessingTime: 100}, // 10 bytes/sec
		{Path: "fast", Savings: 1000, ProcessingTime: 10},  // 100 bytes/sec
	}
	cmd.SortByEfficiency(media)
	if media[0].Path != "fast" {
		t.Errorf("expected fast first, got %s", media[0].Path)
	}
}

func TestGetTimeout(t *testing.T) {
	engCfg := EngineConfig{
		VideoThreads:   2,
		Video4KThreads: 1,
		Timeout: TimeoutFlags{
			VideoTimeoutMult: 2.0,
			VideoTimeout:     "10m",
		},
	}
	engine := NewEngine(nil, nil, engCfg, nil, nil, nil)

	m := models.ShrinkMedia{Category: "Video", Duration: 60}
	timeout := engine.getTimeout(m)
	if timeout.Seconds() != 120 {
		t.Errorf("expected 120s timeout, got %v", timeout.Seconds())
	}

	m.Duration = 0
	timeout = engine.getTimeout(m)
	if timeout.Minutes() != 10 {
		t.Errorf("expected 10m timeout, got %v", timeout.Minutes())
	}
}

func TestFinalizeFileSwapKeepOriginal(t *testing.T) {
	// Create a temp file
	tmpDir := t.TempDir()
	originalPath := filepath.Join(tmpDir, "original.mp4")
	if err := os.WriteFile(originalPath, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}

	e := &Engine{} // mock engine
	m := models.ShrinkMedia{Path: originalPath}
	result := models.ProcessResult{
		Success: true,
		Outputs: []models.ProcessOutputFile{
			{Path: originalPath, Size: 4},
		},
	}

	// This should NOT delete the original file
	e.finalizeFileSwap(m, result, true)

	if _, err := os.Stat(originalPath); os.IsNotExist(err) {
		t.Errorf("original file was deleted but it was in the outputs")
	}
}

func TestFinalizeFileSwapDeleteOriginal(t *testing.T) {
	// Create a temp file
	tmpDir := t.TempDir()
	originalPath := filepath.Join(tmpDir, "original.mp4")
	newPath := filepath.Join(tmpDir, "new.mp4")
	if err := os.WriteFile(originalPath, []byte("original data"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newPath, []byte("new data"), 0o644); err != nil {
		t.Fatal(err)
	}

	e := &Engine{} // mock engine
	m := models.ShrinkMedia{Path: originalPath}
	result := models.ProcessResult{
		Success: true,
		Outputs: []models.ProcessOutputFile{
			{Path: newPath, Size: 8},
		},
	}

	// This SHOULD delete the original file
	e.finalizeFileSwap(m, result, true)

	if _, err := os.Stat(originalPath); err == nil {
		t.Errorf("original file was NOT deleted but it was NOT in the outputs")
	}
	if _, err := os.Stat(newPath); err != nil {
		t.Errorf("new file was deleted but it was in the outputs: %v", err)
	}
}

func TestUpdateMetadataPreservesForeignKeyRows(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "media.db")
	sqlDB, err := dbpkg.Connect(dbPath)
	if err != nil {
		t.Fatalf("Failed to open DB: %v", err)
	}
	defer sqlDB.Close()

	_, err = sqlDB.Exec(`
		CREATE TABLE media (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			path TEXT UNIQUE NOT NULL,
			size INTEGER NOT NULL,
			duration INTEGER DEFAULT 0,
			video_count INTEGER DEFAULT 0,
			audio_count INTEGER DEFAULT 0,
			video_codecs TEXT,
			audio_codecs TEXT,
			subtitle_codecs TEXT,
			media_type TEXT,
			time_deleted INTEGER DEFAULT 0,
			is_shrinked INTEGER DEFAULT 0,
			width INTEGER DEFAULT 0,
			height INTEGER DEFAULT 0
		) STRICT;
		CREATE TABLE history (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			media_path TEXT NOT NULL,
			time_played INTEGER DEFAULT 0,
			FOREIGN KEY (media_path) REFERENCES media(path) ON DELETE CASCADE
		) STRICT;
	`)
	if err != nil {
		t.Fatalf("Failed to create schema: %v", err)
	}

	oldPath := filepath.Join(tempDir, "original.mp4")
	newPath := filepath.Join(tempDir, "compressed.mkv")
	if _, err := sqlDB.Exec(
		"INSERT INTO media (path, size, duration, width, height) VALUES (?, ?, ?, ?, ?)",
		oldPath, 1000, 12, 1920, 1080,
	); err != nil {
		t.Fatalf("Failed to insert media row: %v", err)
	}
	if _, err := sqlDB.Exec("INSERT INTO history (media_path, time_played) VALUES (?, ?)", oldPath, 25); err != nil {
		t.Fatalf("Failed to insert history row: %v", err)
	}

	engine := NewEngine(nil, nil, EngineConfig{}, []*sql.DB{sqlDB}, nil, nil)
	engine.updateMetadata(
		models.ShrinkMedia{Path: oldPath, Duration: 12, Width: 1920, Height: 1080, Category: "Video"},
		models.ProcessResult{Outputs: []models.ProcessOutputFile{{Path: newPath, Size: 600}}},
	)

	var (
		mediaCount   int
		historyCount int
	)
	if err := sqlDB.QueryRow("SELECT COUNT(*) FROM media WHERE path = ?", newPath).Scan(&mediaCount); err != nil {
		t.Fatalf("Failed to query media row: %v", err)
	}
	if err := sqlDB.QueryRow("SELECT COUNT(*) FROM history WHERE media_path = ?", newPath).Scan(&historyCount); err != nil {
		t.Fatalf("Failed to query history row: %v", err)
	}
	if mediaCount != 1 {
		t.Fatalf("Expected renamed media row at %s, got %d", newPath, mediaCount)
	}
	if historyCount != 1 {
		t.Fatalf("Expected history row to move to %s, got %d", newPath, historyCount)
	}
}
