package db

import (
	"database/sql"
	"fmt"
	"path/filepath"

	_ "github.com/mattn/go-sqlite3"
)

// ConnectWithInit connects to a SQLite database and initializes it if needed
func ConnectWithInit(dbPath string) (*sql.DB, string, error) {
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, "", err
	}

	// Test connection
	if err := db.Ping(); err != nil {
		return nil, "", err
	}

	return db, dbPath, nil
}

// GetSchema returns the database schema SQL
func GetSchema() string {
	return `
CREATE TABLE IF NOT EXISTS media (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    path TEXT UNIQUE NOT NULL,
    size INTEGER NOT NULL,
    duration REAL DEFAULT 0,
    video_count INTEGER DEFAULT 0,
    audio_count INTEGER DEFAULT 0,
    video_codecs TEXT,
    audio_codecs TEXT,
    subtitle_codecs TEXT,
    type TEXT,
    mime_type TEXT,
    ext TEXT,
    time_created INTEGER,
    time_modified INTEGER,
    time_deleted INTEGER DEFAULT 0,
    is_shrinked INTEGER DEFAULT 0,
    play_count INTEGER DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_media_path ON media(path);
CREATE INDEX IF NOT EXISTS idx_media_type ON media(type);
CREATE INDEX IF NOT EXISTS idx_media_deleted ON media(time_deleted);
`
}

// InitDB initializes the database with the required schema
func InitDB(db *sql.DB) error {
	schema := GetSchema()
	_, err := db.Exec(schema)
	return err
}

// DatabaseExists checks if a database file exists and is valid
func DatabaseExists(path string) bool {
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		return false
	}
	defer db.Close()
	return db.Ping() == nil
}

// ResolveDatabasePath resolves a database path to an absolute path
func ResolveDatabasePath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("failed to resolve database path: %w", err)
	}
	return abs, nil
}
