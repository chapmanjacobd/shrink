package db

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

func TestDatabaseLifecycle(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "test.db")

	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	// 1. InitDB
	err = InitDB(db)
	if err != nil {
		t.Fatalf("InitDB failed: %v", err)
	}
	if !DatabaseExists(dbPath) {
		t.Fatalf("DatabaseExists returned false after InitDB")
	}

	// 2. Connect
	db2, err := Connect(dbPath)
	if err != nil {
		t.Fatalf("Connect failed: %v", err)
	}
	defer db2.Close()

	// 3. MigrateDB
	err = MigrateDB(db)
	if err != nil {
		t.Fatalf("MigrateDB failed: %v", err)
	}

	// 4. ConnectWithInit (should not error on existing DB)
	db3, _, err := ConnectWithInit(dbPath)
	if err != nil {
		t.Fatalf("ConnectWithInit failed: %v", err)
	}
	db3.Close()
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

func TestPopulateMediaType(t *testing.T) {
	db, _ := sql.Open("sqlite3", ":memory:")
	defer db.Close()
	InitDB(db)

	// Insert file without media_type
	db.Exec("INSERT INTO media (path, size) VALUES ('test.mp4', 1000)")

	// Run migrate/populate
	populateMediaType(db)

	var mediaType string
	db.QueryRow("SELECT media_type FROM media WHERE path = 'test.mp4'").Scan(&mediaType)
	if mediaType != "video" {
		t.Errorf("expected video, got %s", mediaType)
	}
}

func TestBuildExtensionList(t *testing.T) {
	list := buildExtensionList(map[string]bool{".mp4": true})
	if list != "'mp4'" {
		t.Errorf("got %s", list)
	}
}
