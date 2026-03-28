package db

import (
	"os"
	"path/filepath"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

func TestDatabaseLifecycle(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "test.db")

	// Create database with expected schema
	db, err := Connect(dbPath)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
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
	db.Close()

	// 1. Connect
	db2, err := Connect(dbPath)
	if err != nil {
		t.Fatalf("Connect failed: %v", err)
	}
	defer db2.Close()

	if !DatabaseExists(dbPath) {
		t.Fatalf("DatabaseExists returned false")
	}

	// 2. ConnectWithInit (should validate schema successfully)
	db3, _, err := ConnectWithInit(dbPath)
	if err != nil {
		t.Fatalf("ConnectWithInit failed: %v", err)
	}
	defer db3.Close()
}

func TestEnsureSchemaValidation(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "validation.db")

	db, err := Connect(dbPath)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	// Create table with missing columns
	_, err = db.Exec(`
		CREATE TABLE media (
			id INTEGER PRIMARY KEY,
			path TEXT,
			size INTEGER
		);
	`)
	if err != nil {
		t.Fatalf("Failed to create table: %v", err)
	}

	// ensureSchema should fail due to missing columns
	err = ensureSchema(db)
	if err == nil {
		t.Error("ensureSchema should fail with missing columns")
	}
}

func TestResolveDatabasePath(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "test.db")

	// Try resolving non-existent
	res, _ := ResolveDatabasePath(dbPath)
	if res != dbPath {
		t.Errorf("ResolveDatabasePath should return input if non-existent, got %s", res)
	}

	// Create and test if it returns an absolute path if found
	f, _ := os.Create(dbPath)
	f.Close()

	res, _ = ResolveDatabasePath(dbPath)
	if !filepath.IsAbs(res) {
		t.Errorf("Expected absolute path, got %s", res)
	}
}
