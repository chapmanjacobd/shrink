package db

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

func setupTestDB(t *testing.T) (*sql.DB, string) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "media.db")
	db, err := Connect(dbPath)
	if err != nil {
		t.Fatalf("Failed to open DB: %v", err)
	}
	// Create the expected schema for testing
	_, err = db.Exec(`
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
			is_shrinked INTEGER DEFAULT 0
		) STRICT;
		CREATE INDEX idx_media_path ON media(path);
		CREATE INDEX idx_media_type ON media(media_type);
		CREATE INDEX idx_media_deleted ON media(time_deleted);
	`)
	if err != nil {
		t.Fatalf("Failed to create schema: %v", err)
	}
	return db, dbPath
}

func TestMediaLifecycle(t *testing.T) {
	db, dbPath := setupTestDB(t)
	defer db.Close()
	_ = dbPath

	// 1. AddMediaEntry
	AddMediaEntry([]*sql.DB{db}, "test.mp4", 1000, 10.0, ShrinkStatusNotProcessed)

	// 2. LoadMediaFromDB
	records, err := LoadMediaFromDB(db, false, false, false, false, false)
	if err != nil {
		t.Fatalf("LoadMediaFromDB failed: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("Expected 1 record, got %d", len(records))
	}
	if records[0].Path != "test.mp4" {
		t.Errorf("Expected path 'test.mp4', got %s", records[0].Path)
	}

	// 3. MarkShrinked
	MarkShrinked([]*sql.DB{db}, "test.mp4", ShrinkStatusSuccess)

	// 4. UpdateMedia
	UpdateMedia([]*sql.DB{db}, "test.mp4", "test.av1.mkv", 600, 10.0)

	// 5. MarkDeleted
	MarkDeleted([]*sql.DB{db}, "test.av1.mkv")

	records, err = LoadMediaFromDB(db, false, false, false, false, false)
	if err != nil {
		t.Fatalf("LoadMediaFromDB failed: %v", err)
	}
	if len(records) != 0 {
		t.Errorf("Expected 0 records after MarkDeleted, got %d", len(records))
	}
}

func TestIsDatabaseFile(t *testing.T) {
	if !IsDatabaseFile("test.db") {
		t.Errorf("Expected true for .db")
	}
	if !IsDatabaseFile("test.sqlite") {
		t.Errorf("Expected true for .sqlite")
	}
	if IsDatabaseFile("test.mp4") {
		t.Errorf("Expected false for .mp4")
	}
}

func TestIsDatabaseDirectory(t *testing.T) {
	tempDir := t.TempDir()

	if !IsDatabaseDirectory(tempDir) {
		t.Errorf("Expected true for directory")
	}

	tempFile := filepath.Join(tempDir, "data.db")
	f, err := os.Create(tempFile)
	if err != nil {
		t.Fatalf("Failed to create file: %v", err)
	}
	f.Close()
	if IsDatabaseDirectory(tempFile) {
		t.Errorf("Expected false for file")
	}
}
