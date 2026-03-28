// Package db provides database access for media metadata.
package db

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"

	_ "github.com/mattn/go-sqlite3"
)

// SQLite configuration constants
const (
	sqliteBusyTimeout = 30000      // 30 seconds busy timeout for concurrent writes
	sqliteMmapSize    = 2147483648 // 2GB memory-mapped I/O size
	sqliteCacheSize   = -256000    // 250MB cache size (negative = KB)
)

// expectedColumns defines the required columns in the media table that are actually used by the application
var expectedColumns = []string{
	"id", "path", "size", "duration", "video_count", "audio_count",
	"video_codecs", "audio_codecs", "subtitle_codecs", "media_type",
	"time_deleted", "is_shrinked", "width", "height",
}

// Connect connects to a SQLite database
func Connect(dbPath string) (*sql.DB, error) {
	// Add busy timeout to connection string to handle concurrent writes
	dsn := dbPath
	if dbPath != ":memory:" {
		// Use URI format if not already or append if it has query parameters
		if !strings.Contains(dbPath, "?") {
			dsn = dbPath + fmt.Sprintf("?_busy_timeout=%d", sqliteBusyTimeout)
		} else {
			dsn = dbPath + fmt.Sprintf("&_busy_timeout=%d", sqliteBusyTimeout)
		}
	} else {
		// For in-memory, we use shared cache and busy timeout
		dsn = fmt.Sprintf("file::memory:?cache=shared&_busy_timeout=%d", sqliteBusyTimeout)
	}

	db, err := sql.Open("sqlite3", dsn)
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
		fmt.Sprintf("PRAGMA cache_size=%d", sqliteCacheSize),
		"PRAGMA temp_store=MEMORY",
		"PRAGMA foreign_keys=ON",
		fmt.Sprintf("PRAGMA mmap_size=%d", sqliteMmapSize),
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

// ConnectWithInit connects to a SQLite database and validates the schema
func ConnectWithInit(dbPath string) (*sql.DB, string, error) {
	db, err := Connect(dbPath)
	if err != nil {
		return nil, "", err
	}

	// Validate database schema
	if err := ensureSchema(db); err != nil {
		db.Close()
		return nil, "", fmt.Errorf("database schema validation failed: %w", err)
	}

	return db, dbPath, nil
}

// ensureSchema validates that the database schema matches our expectations
func ensureSchema(db *sql.DB) error {
	columns, err := getTableColumns(db, "media")
	if err != nil {
		return fmt.Errorf("failed to get media table columns: %w", err)
	}

	var missingColumns []string
	for _, expected := range expectedColumns {
		if columns[strings.ToUpper(expected)] == "" {
			missingColumns = append(missingColumns, expected)
		}
	}

	if len(missingColumns) > 0 {
		return fmt.Errorf("media table is missing columns: %v", missingColumns)
	}

	return nil
}

func getTableColumns(db *sql.DB, tableName string) (map[string]string, error) {
	rows, err := db.Query(fmt.Sprintf("PRAGMA table_info(%s)", tableName))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	columns := make(map[string]string)
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
		columns[strings.ToUpper(name)] = strings.ToUpper(ctype)
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
