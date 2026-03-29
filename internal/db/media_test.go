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
			is_shrinked INTEGER DEFAULT 0,
			width INTEGER DEFAULT 0,
			height INTEGER DEFAULT 0
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
	UpdateMedia([]*sql.DB{db}, "test.mp4", "test.av1.mkv", 600, 10.0, 0, 0)

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

// TestUpdateMediaWhenNewPathExists tests the scenario where UpdateMedia is called
// but the newPath already exists in the database. This can happen when:
// 1. A file was previously processed and saved with newPath
// 2. The same file is being processed again with a different oldPath
// The expected behavior is to update the existing newPath row and delete oldPath
func TestUpdateMediaWhenNewPathExists(t *testing.T) {
	db, dbPath := setupTestDB(t)
	defer db.Close()
	_ = dbPath

	oldPath := "original.mp4"
	newPath := "compressed.mkv"

	// Add the oldPath entry (file being processed)
	AddMediaEntry([]*sql.DB{db}, oldPath, 1000, 10.0, ShrinkStatusNotProcessed)

	// Simulate a scenario where newPath already exists (e.g., from a previous run)
	AddMediaEntry([]*sql.DB{db}, newPath, 500, 10.0, ShrinkStatusSuccess)

	// Verify both entries exist
	records, err := LoadMediaFromDB(db, true, false, false, false, false)
	if err != nil {
		t.Fatalf("LoadMediaFromDB failed: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("Expected 2 records before update, got %d", len(records))
	}

	// This should update the existing newPath row and delete oldPath
	// Currently this fails with "FOREIGN KEY constraint failed" or UNIQUE constraint error
	UpdateMedia([]*sql.DB{db}, oldPath, newPath, 600, 10.0, 0, 0)

	// Verify only newPath exists with updated values
	records, err = LoadMediaFromDB(db, true, false, false, false, false)
	if err != nil {
		t.Fatalf("LoadMediaFromDB failed: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("Expected 1 record after update, got %d", len(records))
	}
	if records[0].Path != newPath {
		t.Errorf("Expected path '%s', got '%s'", newPath, records[0].Path)
	}
	if records[0].Size != 600 {
		t.Errorf("Expected size 600, got %d", records[0].Size)
	}
}

// TestUpdateMediaWithForeignKeyConstraint tests UpdateMedia when there's a foreign key
// relationship that prevents deleting the newPath. This simulates the real-world scenario
// where FTS5 triggers or other FK relationships exist.
func TestUpdateMediaWithForeignKeyConstraint(t *testing.T) {
	db, dbPath := setupTestDB(t)
	defer db.Close()
	_ = dbPath

	// Enable foreign keys
	_, err := db.Exec("PRAGMA foreign_keys = ON")
	if err != nil {
		t.Fatalf("Failed to enable foreign keys: %v", err)
	}

	// Create a child table with foreign key to media
	_, err = db.Exec(`
		CREATE TABLE media_metadata (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			media_path TEXT NOT NULL,
			metadata TEXT,
			FOREIGN KEY (media_path) REFERENCES media(path) ON DELETE RESTRICT
		)
	`)
	if err != nil {
		t.Fatalf("Failed to create child table: %v", err)
	}

	oldPath := "original.mp4"
	newPath := "compressed.mkv"

	// Add entries
	AddMediaEntry([]*sql.DB{db}, oldPath, 1000, 10.0, ShrinkStatusNotProcessed)
	AddMediaEntry([]*sql.DB{db}, newPath, 500, 10.0, ShrinkStatusSuccess)

	// Add a metadata entry referencing newPath (simulating FTS5 or other FK relationship)
	_, err = db.Exec("INSERT INTO media_metadata (media_path, metadata) VALUES (?, ?)", newPath, "test metadata")
	if err != nil {
		t.Fatalf("Failed to insert metadata: %v", err)
	}

	// This should handle the FK constraint gracefully
	// The current implementation fails here because it tries to DELETE newPath first,
	// which is blocked by the FK constraint, then the UPDATE fails with UNIQUE constraint
	UpdateMedia([]*sql.DB{db}, oldPath, newPath, 600, 10.0, 0, 0)

	// Verify the update succeeded correctly
	records, err := LoadMediaFromDB(db, true, false, false, false, false)
	if err != nil {
		t.Fatalf("LoadMediaFromDB failed: %v", err)
	}
	// Should have 1 record (newPath updated, oldPath deleted)
	if len(records) != 1 {
		t.Fatalf("Expected 1 record after update, got %d", len(records))
	}
	if records[0].Path != newPath {
		t.Errorf("Expected path '%s', got '%s'", newPath, records[0].Path)
	}
	if records[0].Size != 600 {
		t.Errorf("Expected size 600, got %d", records[0].Size)
	}

	// Verify metadata still exists (FK relationship preserved)
	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM media_metadata WHERE media_path = ?", newPath).Scan(&count)
	if err != nil {
		t.Fatalf("Failed to query metadata: %v", err)
	}
	if count != 1 {
		t.Errorf("Expected 1 metadata record, got %d", count)
	}
}
