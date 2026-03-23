// Package db provides database access and migration logic for media metadata.
package db

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/chapmanjacobd/shrink/internal/utils"
	_ "modernc.org/sqlite"
)

// Connect connects to a SQLite database without initializing it
func Connect(dbPath string) (*sql.DB, error) {
	// Add busy timeout to connection string to handle concurrent writes
	dsn := dbPath
	if dbPath != ":memory:" {
		// Use URI format if not already or append if it has query parameters
		if !strings.Contains(dbPath, "?") {
			dsn = dbPath + "?_busy_timeout=30000"
		} else {
			dsn = dbPath + "&_busy_timeout=30000"
		}
	} else {
		// For in-memory, we use shared cache and busy timeout
		dsn = "file::memory:?cache=shared&_busy_timeout=30000"
	}

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}

	// Limit to one connection to avoid SQLITE_BUSY during concurrent writes.
	// Since we use WAL mode, multiple readers can still coexist with one writer,
	// but sql.DB's connection pool can still cause issues if it tries to open
	// multiple write connections.
	db.SetMaxOpenConns(1)

	// Apply tuning PRAGMAs
	tuning := []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=NORMAL",
		"PRAGMA cache_size=-256000",
		"PRAGMA temp_store=MEMORY",
		"PRAGMA foreign_keys=ON",
		"PRAGMA mmap_size=2147483648",
	}

	for _, pragma := range tuning {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("failed to apply %s: %w", pragma, err)
		}
	}

	// Test connection
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}

	return db, nil
}

// ConnectWithInit connects to a SQLite database and initializes it if needed
func ConnectWithInit(dbPath string) (*sql.DB, string, error) {
	db, err := Connect(dbPath)
	if err != nil {
		return nil, "", err
	}

	// Initialize database schema and run migrations
	if err := InitDB(db); err != nil {
		db.Close()
		return nil, "", fmt.Errorf("failed to initialize database: %w", err)
	}

	return db, dbPath, nil
}

// GetTableSchema returns the database table schema SQL
func GetTableSchema() string {
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
    media_type TEXT,
    mime_type TEXT,
    ext TEXT,
    time_created INTEGER,
    time_modified INTEGER,
    time_deleted INTEGER DEFAULT 0,
    is_shrinked INTEGER DEFAULT 0,
    play_count INTEGER DEFAULT 0
);
`
}

// GetIndexSchema returns the database index schema SQL
func GetIndexSchema() string {
	return `
CREATE INDEX IF NOT EXISTS idx_media_path ON media(path);
CREATE INDEX IF NOT EXISTS idx_media_type ON media(media_type);
CREATE INDEX IF NOT EXISTS idx_media_deleted ON media(time_deleted);
`
}

// InitDB initializes the database with the required schema and runs migrations
func InitDB(db *sql.DB) error {
	if _, err := db.Exec(GetTableSchema()); err != nil {
		return fmt.Errorf("failed to create table schema: %w", err)
	}

	if err := MigrateDB(db); err != nil {
		return fmt.Errorf("failed to migrate database: %w", err)
	}

	if _, err := db.Exec(GetIndexSchema()); err != nil {
		return fmt.Errorf("failed to create index schema: %w", err)
	}

	return nil
}

// MigrateDB adds any missing columns to the media table
func MigrateDB(db *sql.DB) error {
	columns, err := getTableColumns(db, "media")
	if err != nil {
		return err
	}

	// Rename 'type' to 'media_type' if needed
	if columns["type"] && !columns["media_type"] {
		if _, err := db.Exec("ALTER TABLE media RENAME COLUMN type TO media_type"); err != nil {
			return fmt.Errorf("failed to rename column 'type' to 'media_type': %w", err)
		}
		columns["media_type"] = true
		delete(columns, "type")
	}

	// Columns to add if missing (path and size are mandatory in CREATE TABLE)
	migrations := map[string]string{
		"duration":        "REAL DEFAULT 0",
		"video_count":     "INTEGER DEFAULT 0",
		"audio_count":     "INTEGER DEFAULT 0",
		"video_codecs":    "TEXT",
		"audio_codecs":    "TEXT",
		"subtitle_codecs": "TEXT",
		"media_type":      "TEXT",
		"mime_type":       "TEXT",
		"ext":             "TEXT",
		"time_created":    "INTEGER",
		"time_modified":   "INTEGER",
		"time_deleted":    "INTEGER DEFAULT 0",
		"is_shrinked":     "INTEGER DEFAULT 0",
		"play_count":      "INTEGER DEFAULT 0",
	}

	for col, definition := range migrations {
		if !columns[col] {
			query := fmt.Sprintf("ALTER TABLE media ADD COLUMN %s %s", col, definition)
			if _, err := db.Exec(query); err != nil {
				return fmt.Errorf("failed to add column %s: %w", col, err)
			}
		}
	}

	// Populate media_type based on file extension if not already set
	if err := populateMediaType(db); err != nil {
		return fmt.Errorf("failed to populate media_type: %w", err)
	}

	return nil
}

// populateMediaType fills in NULL media_type values based on file extension
func populateMediaType(db *sql.DB) error {
	// Check if there are any rows with NULL or empty media_type
	var count int
	err := db.QueryRow(`SELECT COUNT(*) FROM media WHERE media_type IS NULL OR media_type = ''`).Scan(&count)
	if err != nil {
		return err
	}
	if count == 0 {
		return nil
	}

	// Use IMMEDIATE transaction to acquire write lock upfront
	tx, err := BeginImmediate(db)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback() // Rollback if commit fails or on error

	// Build extension lists from utils
	videoExts := buildExtensionList(utils.VideoExtensionMap)
	audioExts := buildExtensionList(utils.AudioExtensionMap)
	imageExts := buildExtensionList(utils.ImageExtensionMap)
	textExts := buildExtensionList(utils.TextExtensionMap)
	archiveExts := buildExtensionList(utils.ArchiveExtensionMap)

	// Update media_type based on file extension
	if videoExts != "" {
		_, err = tx.Exec(`
			UPDATE media SET media_type = 'video'
			WHERE (media_type IS NULL OR media_type = '')
			AND LOWER(SUBSTR(path, INSTR(path, '.') + 1)) IN (` + videoExts + `)
		`)
		if err != nil {
			return err
		}
	}

	if audioExts != "" {
		_, err = tx.Exec(`
			UPDATE media SET media_type = 'audio'
			WHERE (media_type IS NULL OR media_type = '')
			AND LOWER(SUBSTR(path, INSTR(path, '.') + 1)) IN (` + audioExts + `)
		`)
		if err != nil {
			return err
		}
	}

	// Audiobook extensions (m4b, aa, aax) - subset of audio
	_, err = tx.Exec(`
		UPDATE media SET media_type = 'audiobook'
		WHERE (media_type IS NULL OR media_type = '')
		AND LOWER(SUBSTR(path, INSTR(path, '.') + 1)) IN ('m4b', 'aa', 'aax')
	`)
	if err != nil {
		return err
	}

	if imageExts != "" {
		_, err = tx.Exec(`
			UPDATE media SET media_type = 'image'
			WHERE (media_type IS NULL OR media_type = '')
			AND LOWER(SUBSTR(path, INSTR(path, '.') + 1)) IN (` + imageExts + `)
		`)
		if err != nil {
			return err
		}
	}

	if textExts != "" {
		_, err = tx.Exec(`
			UPDATE media SET media_type = 'text'
			WHERE (media_type IS NULL OR media_type = '')
			AND LOWER(SUBSTR(path, INSTR(path, '.') + 1)) IN (` + textExts + `)
		`)
		if err != nil {
			return err
		}
	}

	if archiveExts != "" {
		_, err = tx.Exec(`
			UPDATE media SET media_type = 'archive'
			WHERE (media_type IS NULL OR media_type = '')
			AND LOWER(SUBSTR(path, INSTR(path, '.') + 1)) IN (` + archiveExts + `)
		`)
		if err != nil {
			return err
		}
	}

	return tx.Commit()
}

// buildExtensionList converts a map of extensions to a SQL-ready comma-separated string
func buildExtensionList(extMap map[string]bool) string {
	var exts []string
	for ext := range extMap {
		// Remove leading dot for SQL comparison
		cleanExt := strings.TrimPrefix(ext, ".")
		exts = append(exts, "'"+cleanExt+"'")
	}
	return strings.Join(exts, ",")
}

func getTableColumns(db *sql.DB, tableName string) (map[string]bool, error) {
	rows, err := db.Query(fmt.Sprintf("PRAGMA table_info(%s)", tableName))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	columns := make(map[string]bool)
	for rows.Next() {
		var (
			cid       int
			name      string
			ctype     string
			notnull   int
			dfltValue sql.NullString
			pk        int
		)
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dfltValue, &pk); err != nil {
			return nil, err
		}
		columns[name] = true
	}
	return columns, nil
}

// DatabaseExists checks if a database file exists and is valid
func DatabaseExists(path string) bool {
	db, err := Connect(path)
	if err != nil {
		return false
	}
	defer db.Close()
	return true
}

// ResolveDatabasePath resolves a database path to an absolute path
func ResolveDatabasePath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("failed to resolve database path: %w", err)
	}
	return abs, nil
}

// BeginImmediate starts a new transaction with IMMEDIATE mode.
// This acquires a write lock immediately, respecting the busy_timeout.
// Use this instead of db.Begin() to avoid SQLITE_BUSY errors from
// deferred lock upgrades.
func BeginImmediate(db *sql.DB) (*sql.Tx, error) {
	return db.BeginTx(context.Background(), &sql.TxOptions{
		Isolation: sql.LevelWriteCommitted,
		ReadOnly:  false,
	})
}
