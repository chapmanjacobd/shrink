package db

import (
	"os"
	"path/filepath"
	"testing"
)

func TestArchiveCache(t *testing.T) {
	// Create a temporary database
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	db, err := Connect(dbPath)
	if err != nil {
		t.Fatalf("Failed to connect to database: %v", err)
	}
	defer db.Close()

	// Create media table (required by ConnectWithInit, but we'll create it manually for testing)
	createMediaSQL := `
	CREATE TABLE IF NOT EXISTS media (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		path TEXT UNIQUE,
		size INTEGER DEFAULT 0,
		duration REAL DEFAULT 0,
		video_count INTEGER DEFAULT 0,
		audio_count INTEGER DEFAULT 0,
		video_codecs TEXT DEFAULT '',
		audio_codecs TEXT DEFAULT '',
		subtitle_codecs TEXT DEFAULT '',
		media_type TEXT DEFAULT '',
		time_deleted INTEGER DEFAULT 0,
		is_shrinked INTEGER DEFAULT 0,
		width INTEGER DEFAULT 0,
		height INTEGER DEFAULT 0
	);
	`
	if _, err := db.Exec(createMediaSQL); err != nil {
		t.Fatalf("Failed to create media table: %v", err)
	}

	// Initialize the archive cache table
	if err := InitArchiveCache(db); err != nil {
		t.Fatalf("Failed to initialize archive cache: %v", err)
	}

	t.Run("SetAndGet", func(t *testing.T) {
		entry := &ArchiveCacheEntry{
			Path:             "/test/archive.zip",
			TotalArchiveSize: 1000000,
			FutureSize:       500000,
			ProcessingTime:   120,
			HasProcessable:   true,
			IsBroken:         false,
			PartFiles:        "[]",
		}

		// Set cache
		if err := SetArchiveCache(db, entry); err != nil {
			t.Fatalf("Failed to set cache: %v", err)
		}

		// Get cache
		retrieved, err := GetArchiveCache(db, "/test/archive.zip")
		if err != nil {
			t.Fatalf("Failed to get cache: %v", err)
		}
		if retrieved == nil {
			t.Fatal("Cache entry not found")
		}

		if retrieved.Path != entry.Path {
			t.Errorf("Path mismatch: got %s, want %s", retrieved.Path, entry.Path)
		}
		if retrieved.TotalArchiveSize != entry.TotalArchiveSize {
			t.Errorf("TotalArchiveSize mismatch: got %d, want %d", retrieved.TotalArchiveSize, entry.TotalArchiveSize)
		}
		if retrieved.FutureSize != entry.FutureSize {
			t.Errorf("FutureSize mismatch: got %d, want %d", retrieved.FutureSize, entry.FutureSize)
		}
		if retrieved.ProcessingTime != entry.ProcessingTime {
			t.Errorf("ProcessingTime mismatch: got %d, want %d", retrieved.ProcessingTime, entry.ProcessingTime)
		}
		if retrieved.HasProcessable != entry.HasProcessable {
			t.Errorf("HasProcessable mismatch: got %v, want %v", retrieved.HasProcessable, entry.HasProcessable)
		}
		if retrieved.IsBroken != entry.IsBroken {
			t.Errorf("IsBroken mismatch: got %v, want %v", retrieved.IsBroken, entry.IsBroken)
		}
	})

	t.Run("Update", func(t *testing.T) {
		entry := &ArchiveCacheEntry{
			Path:             "/test/archive2.zip",
			TotalArchiveSize: 2000000,
			FutureSize:       800000,
			ProcessingTime:   200,
			HasProcessable:   true,
			IsBroken:         false,
			PartFiles:        "[]",
		}

		// Set initial cache
		if err := SetArchiveCache(db, entry); err != nil {
			t.Fatalf("Failed to set cache: %v", err)
		}

		// Update cache
		entry.FutureSize = 600000
		entry.ProcessingTime = 150
		if err := SetArchiveCache(db, entry); err != nil {
			t.Fatalf("Failed to update cache: %v", err)
		}

		// Verify update
		retrieved, err := GetArchiveCache(db, "/test/archive2.zip")
		if err != nil {
			t.Fatalf("Failed to get cache: %v", err)
		}
		if retrieved.FutureSize != 600000 {
			t.Errorf("FutureSize not updated: got %d, want 600000", retrieved.FutureSize)
		}
		if retrieved.ProcessingTime != 150 {
			t.Errorf("ProcessingTime not updated: got %d, want 150", retrieved.ProcessingTime)
		}
	})

	t.Run("Delete", func(t *testing.T) {
		entry := &ArchiveCacheEntry{
			Path:             "/test/archive3.zip",
			TotalArchiveSize: 3000000,
			FutureSize:       1000000,
			ProcessingTime:   300,
			HasProcessable:   true,
			IsBroken:         false,
			PartFiles:        "[]",
		}

		// Set cache
		if err := SetArchiveCache(db, entry); err != nil {
			t.Fatalf("Failed to set cache: %v", err)
		}

		// Delete cache
		if err := DeleteArchiveCache(db, "/test/archive3.zip"); err != nil {
			t.Fatalf("Failed to delete cache: %v", err)
		}

		// Verify deletion
		retrieved, err := GetArchiveCache(db, "/test/archive3.zip")
		if err != nil {
			t.Fatalf("Failed to get cache: %v", err)
		}
		if retrieved != nil {
			t.Error("Cache entry should have been deleted")
		}
	})

	t.Run("BulkSet", func(t *testing.T) {
		entries := []ArchiveCacheEntry{
			{
				Path:             "/test/bulk1.zip",
				TotalArchiveSize: 1000000,
				FutureSize:       500000,
				ProcessingTime:   100,
				HasProcessable:   true,
				IsBroken:         false,
				PartFiles:        "[]",
			},
			{
				Path:             "/test/bulk2.zip",
				TotalArchiveSize: 2000000,
				FutureSize:       800000,
				ProcessingTime:   200,
				HasProcessable:   false,
				IsBroken:         true,
				PartFiles:        "[\"/test/bulk2.z01\"]",
			},
		}

		// Bulk set cache
		if err := BulkSetArchiveCache(db, entries); err != nil {
			t.Fatalf("Failed to bulk set cache: %v", err)
		}

		// Verify first entry
		retrieved1, err := GetArchiveCache(db, "/test/bulk1.zip")
		if err != nil {
			t.Fatalf("Failed to get cache: %v", err)
		}
		if retrieved1 == nil || retrieved1.FutureSize != 500000 {
			t.Errorf("First entry not set correctly")
		}

		// Verify second entry
		retrieved2, err := GetArchiveCache(db, "/test/bulk2.zip")
		if err != nil {
			t.Fatalf("Failed to get cache: %v", err)
		}
		if retrieved2 == nil || !retrieved2.IsBroken {
			t.Errorf("Second entry not set correctly")
		}
	})
}

func TestArchiveCacheNoCacheEntry(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	db, err := Connect(dbPath)
	if err != nil {
		t.Fatalf("Failed to connect to database: %v", err)
	}
	defer db.Close()

	// Create media table
	createMediaSQL := `
	CREATE TABLE IF NOT EXISTS media (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		path TEXT UNIQUE,
		size INTEGER DEFAULT 0,
		duration REAL DEFAULT 0,
		video_count INTEGER DEFAULT 0,
		audio_count INTEGER DEFAULT 0,
		video_codecs TEXT DEFAULT '',
		audio_codecs TEXT DEFAULT '',
		subtitle_codecs TEXT DEFAULT '',
		media_type TEXT DEFAULT '',
		time_deleted INTEGER DEFAULT 0,
		is_shrinked INTEGER DEFAULT 0,
		width INTEGER DEFAULT 0,
		height INTEGER DEFAULT 0
	);
	`
	if _, err := db.Exec(createMediaSQL); err != nil {
		t.Fatalf("Failed to create media table: %v", err)
	}

	if err := InitArchiveCache(db); err != nil {
		t.Fatalf("Failed to initialize archive cache: %v", err)
	}

	// Get non-existent entry
	entry, err := GetArchiveCache(db, "/nonexistent/archive.zip")
	if err != nil {
		t.Fatalf("GetArchiveCache should not return error for non-existent entry: %v", err)
	}
	if entry != nil {
		t.Error("GetArchiveCache should return nil for non-existent entry")
	}
}

func TestMain(m *testing.M) {
	// Suppress logs during tests
	os.Exit(m.Run())
}
