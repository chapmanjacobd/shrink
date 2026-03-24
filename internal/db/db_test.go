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

	db, err := Connect(dbPath)
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

func TestMigrationToInteger(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "migration.db")
	db, err := Connect(dbPath)
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}
	defer db.Close()

	// 1. Create a table with REAL duration (legacy)
	_, err = db.Exec(`
		CREATE TABLE media (
			path TEXT PRIMARY KEY,
			size INTEGER,
			duration REAL
		);
	`)
	if err != nil {
		t.Fatalf("failed to create legacy table: %v", err)
	}

	// Insert some data with fractional duration
	_, err = db.Exec("INSERT INTO media (path, size, duration) VALUES (?, ?, ?)", "test.mp4", 1000, 12.34)
	if err != nil {
		t.Fatalf("failed to insert: %v", err)
	}

	// 2. Run InitDB which should trigger MigrateDB and migrateToIntegerDuration
	err = InitDB(db)
	if err != nil {
		t.Fatalf("InitDB failed: %v", err)
	}

	// 3. Verify column type is now INTEGER
	columns, err := getTableColumns(db, "media")
	if err != nil {
		t.Fatalf("getTableColumns failed: %v", err)
	}
	if columns["DURATION"] != "INTEGER" {
		t.Errorf("Expected DURATION type to be INTEGER, got %s", columns["DURATION"])
	}

	// 4. Verify data was preserved (rounded)
	var duration int64
	err = db.QueryRow("SELECT duration FROM media WHERE path = 'test.mp4'").Scan(&duration)
	if err != nil {
		t.Fatalf("Failed to query duration: %v", err)
	}
	if duration != 12 {
		t.Errorf("Expected duration 12, got %d", duration)
	}

	// 5. Verify STRICT is now there
	if !IsTableStrict(db, "media") {
		t.Errorf("Expected table to be STRICT after migration")
	}
}
