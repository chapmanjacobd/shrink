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

func createMediaReferenceTables(t *testing.T, db *sql.DB) {
	t.Helper()

	_, err := db.Exec(`
		CREATE TABLE playlists (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			path TEXT UNIQUE,
			title TEXT
		) STRICT;
		CREATE TABLE playlist_items (
			playlist_id INTEGER NOT NULL,
			media_path TEXT NOT NULL,
			track_number INTEGER,
			PRIMARY KEY (playlist_id, media_path),
			FOREIGN KEY (playlist_id) REFERENCES playlists(id) ON DELETE CASCADE,
			FOREIGN KEY (media_path) REFERENCES media(path) ON DELETE CASCADE
		) STRICT;
		CREATE TABLE history (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			media_path TEXT NOT NULL,
			time_played INTEGER DEFAULT 0,
			FOREIGN KEY (media_path) REFERENCES media(path) ON DELETE CASCADE
		) STRICT;
		CREATE TABLE captions (
			media_path TEXT NOT NULL,
			time REAL,
			text TEXT,
			FOREIGN KEY (media_path) REFERENCES media(path) ON DELETE CASCADE
		) STRICT;
	`)
	if err != nil {
		t.Fatalf("Failed to create media reference tables: %v", err)
	}
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

func TestUpdateMediaRenamesBuiltInForeignKeyReferences(t *testing.T) {
	db, _ := setupTestDB(t)
	defer db.Close()
	createMediaReferenceTables(t, db)

	oldPath := "original.mp4"
	newPath := "compressed.mkv"

	AddMediaEntry([]*sql.DB{db}, oldPath, 1000, 10.0, ShrinkStatusNotProcessed)

	result, err := db.Exec("INSERT INTO playlists (path, title) VALUES (?, ?)", "playlist.m3u8", "Test Playlist")
	if err != nil {
		t.Fatalf("Failed to insert playlist: %v", err)
	}
	playlistID, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("Failed to fetch playlist ID: %v", err)
	}

	if _, err := db.Exec("INSERT INTO history (media_path, time_played) VALUES (?, ?)", oldPath, 10); err != nil {
		t.Fatalf("Failed to insert history: %v", err)
	}
	if _, err := db.Exec("INSERT INTO captions (media_path, time, text) VALUES (?, ?, ?)", oldPath, 1.5, "caption"); err != nil {
		t.Fatalf("Failed to insert captions: %v", err)
	}
	if _, err := db.Exec("INSERT INTO playlist_items (playlist_id, media_path, track_number) VALUES (?, ?, ?)", playlistID, oldPath, 1); err != nil {
		t.Fatalf("Failed to insert playlist item: %v", err)
	}

	UpdateMedia([]*sql.DB{db}, oldPath, newPath, 600, 10.0, 1920, 1080)

	records, err := LoadMediaFromDB(db, true, false, false, false, false)
	if err != nil {
		t.Fatalf("LoadMediaFromDB failed: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("Expected 1 record after rename, got %d", len(records))
	}
	if records[0].Path != newPath {
		t.Fatalf("Expected record path %q, got %q", newPath, records[0].Path)
	}

	assertMediaReferenceCount(t, db, "history", newPath, 1)
	assertMediaReferenceCount(t, db, "captions", newPath, 1)
	assertMediaReferenceCount(t, db, "playlist_items", newPath, 1)
	assertMediaReferenceCount(t, db, "history", oldPath, 0)
	assertMediaReferenceCount(t, db, "captions", oldPath, 0)
	assertMediaReferenceCount(t, db, "playlist_items", oldPath, 0)
}

func TestUpdateMediaMergesPlaylistConflictsBeforeRepointingReferences(t *testing.T) {
	db, _ := setupTestDB(t)
	defer db.Close()
	createMediaReferenceTables(t, db)

	oldPath := "original.mp4"
	newPath := "compressed.mkv"

	AddMediaEntry([]*sql.DB{db}, oldPath, 1000, 10.0, ShrinkStatusNotProcessed)
	AddMediaEntry([]*sql.DB{db}, newPath, 500, 10.0, ShrinkStatusSuccess)

	result, err := db.Exec("INSERT INTO playlists (path, title) VALUES (?, ?)", "playlist.m3u8", "Test Playlist")
	if err != nil {
		t.Fatalf("Failed to insert playlist: %v", err)
	}
	playlistID, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("Failed to fetch playlist ID: %v", err)
	}

	if _, err := db.Exec("INSERT INTO history (media_path, time_played) VALUES (?, ?)", oldPath, 10); err != nil {
		t.Fatalf("Failed to insert history: %v", err)
	}
	if _, err := db.Exec("INSERT INTO captions (media_path, time, text) VALUES (?, ?, ?)", oldPath, 1.5, "caption"); err != nil {
		t.Fatalf("Failed to insert captions: %v", err)
	}
	if _, err := db.Exec("INSERT INTO playlist_items (playlist_id, media_path, track_number) VALUES (?, ?, ?)", playlistID, oldPath, 1); err != nil {
		t.Fatalf("Failed to insert old playlist item: %v", err)
	}
	if _, err := db.Exec("INSERT INTO playlist_items (playlist_id, media_path, track_number) VALUES (?, ?, ?)", playlistID, newPath, 2); err != nil {
		t.Fatalf("Failed to insert new playlist item: %v", err)
	}

	UpdateMedia([]*sql.DB{db}, oldPath, newPath, 600, 10.0, 1920, 1080)

	assertMediaReferenceCount(t, db, "history", newPath, 1)
	assertMediaReferenceCount(t, db, "captions", newPath, 1)
	assertMediaReferenceCount(t, db, "playlist_items", newPath, 1)

	var trackNumber int
	err = db.QueryRow("SELECT track_number FROM playlist_items WHERE playlist_id = ? AND media_path = ?", playlistID, newPath).Scan(&trackNumber)
	if err != nil {
		t.Fatalf("Failed to query merged playlist item: %v", err)
	}
	if trackNumber != 2 {
		t.Fatalf("Expected existing newPath playlist item to win, got track_number=%d", trackNumber)
	}
}

func TestAddMediaEntryWithDimensionsUpsertsWithoutDeletingChildren(t *testing.T) {
	db, _ := setupTestDB(t)
	defer db.Close()
	createMediaReferenceTables(t, db)

	path := "track.flac"
	AddMediaEntry([]*sql.DB{db}, path, 1000, 10.0, ShrinkStatusNotProcessed)
	if _, err := db.Exec("INSERT INTO history (media_path, time_played) VALUES (?, ?)", path, 10); err != nil {
		t.Fatalf("Failed to insert history: %v", err)
	}

	AddMediaEntryWithDimensions([]*sql.DB{db}, path, 600, 0, 0, 0, ShrinkStatusSuccess)

	assertMediaReferenceCount(t, db, "history", path, 1)

	var (
		size       int64
		duration   int64
		isShrinked int
	)
	err := db.QueryRow("SELECT size, duration, is_shrinked FROM media WHERE path = ?", path).Scan(&size, &duration, &isShrinked)
	if err != nil {
		t.Fatalf("Failed to query upserted media row: %v", err)
	}
	if size != 600 {
		t.Fatalf("Expected size 600, got %d", size)
	}
	if duration != 0 {
		t.Fatalf("Expected duration 0 after upsert, got %d", duration)
	}
	if isShrinked != ShrinkStatusSuccess {
		t.Fatalf("Expected status %d, got %d", ShrinkStatusSuccess, isShrinked)
	}
}

func TestUpdateMediaDoesNotFailOnUnrelatedExistingForeignKeyViolations(t *testing.T) {
	db, _ := setupTestDB(t)
	defer db.Close()
	createMediaReferenceTables(t, db)

	if _, err := db.Exec("PRAGMA foreign_keys = OFF"); err != nil {
		t.Fatalf("Failed to disable foreign keys: %v", err)
	}
	if _, err := db.Exec("INSERT INTO captions (media_path, time, text) VALUES (?, ?, ?)", "missing.mp4", 1.5, "orphan"); err != nil {
		t.Fatalf("Failed to insert orphan caption: %v", err)
	}
	if _, err := db.Exec("PRAGMA foreign_keys = ON"); err != nil {
		t.Fatalf("Failed to re-enable foreign keys: %v", err)
	}

	oldPath := "original.mp4"
	newPath := "compressed.mkv"
	AddMediaEntry([]*sql.DB{db}, oldPath, 1000, 10.0, ShrinkStatusNotProcessed)
	if _, err := db.Exec("INSERT INTO captions (media_path, time, text) VALUES (?, ?, ?)", oldPath, 2.5, "valid"); err != nil {
		t.Fatalf("Failed to insert valid caption: %v", err)
	}

	UpdateMedia([]*sql.DB{db}, oldPath, newPath, 600, 10.0, 1920, 1080)

	assertMediaReferenceCount(t, db, "captions", newPath, 1)

	var mediaCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM media WHERE path = ?", newPath).Scan(&mediaCount); err != nil {
		t.Fatalf("Failed to query renamed media row: %v", err)
	}
	if mediaCount != 1 {
		t.Fatalf("Expected renamed media row at %s, got %d", newPath, mediaCount)
	}

	var orphanCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM captions WHERE media_path = ?", "missing.mp4").Scan(&orphanCount); err != nil {
		t.Fatalf("Failed to query orphan caption: %v", err)
	}
	if orphanCount != 1 {
		t.Fatalf("Expected unrelated orphan caption to remain, got %d", orphanCount)
	}
}

func assertMediaReferenceCount(t *testing.T, db *sql.DB, tableName, mediaPath string, want int) {
	t.Helper()

	var count int
	query := "SELECT COUNT(*) FROM " + tableName + " WHERE media_path = ?"
	if err := db.QueryRow(query, mediaPath).Scan(&count); err != nil {
		t.Fatalf("Failed to query %s references for %s: %v", tableName, mediaPath, err)
	}
	if count != want {
		t.Fatalf("Expected %d %s references for %s, got %d", want, tableName, mediaPath, count)
	}
}
