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
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatalf("Failed to open DB: %v", err)
	}
	err = InitDB(db)
	if err != nil {
		t.Fatalf("Failed to init DB: %v", err)
	}
	return db, dbPath
}

func TestMediaLifecycle(t *testing.T) {
	db, dbPath := setupTestDB(t)
	defer db.Close()
	_ = dbPath

	// 1. AddMediaEntry
	AddMediaEntry([]*sql.DB{db}, "test.mp4", 1000, 10.0)

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
	MarkShrinked([]*sql.DB{db}, "test.mp4")

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
