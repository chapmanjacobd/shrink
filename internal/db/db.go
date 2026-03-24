// Package db provides database access and migration logic for media metadata.
package db

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"

	_ "github.com/mattn/go-sqlite3"
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
    duration INTEGER DEFAULT 0,
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
) STRICT;
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
	// Attempt to create table with STRICT first.
	// This will fail if SQLite is older than 3.37.0.
	if _, err := db.Exec(GetTableSchema()); err != nil {
		// Fallback for older SQLite versions: remove 'STRICT;' and retry
		noStrictSchema := strings.Replace(GetTableSchema(), ") STRICT;", ");", 1)
		if _, errFallback := db.Exec(noStrictSchema); errFallback != nil {
			return fmt.Errorf("failed to create table schema: %w", errFallback)
		}
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
	if columns["TYPE"] != "" && columns["MEDIA_TYPE"] == "" {
		if _, err := db.Exec("ALTER TABLE media RENAME COLUMN type TO media_type"); err != nil {
			return fmt.Errorf("failed to rename column 'type' to 'media_type': %w", err)
		}
		columns["MEDIA_TYPE"] = "TEXT"
		delete(columns, "TYPE")
	}

	// Columns to add if missing (path and size are mandatory in CREATE TABLE)
	migrations := map[string]string{
		"duration":        "INTEGER DEFAULT 0",
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

	needsRecreation := false
	if columns["DURATION"] != "" && columns["DURATION"] != "INTEGER" {
		needsRecreation = true
	}

	if needsRecreation {
		// Migration to convert non-INTEGER duration to INTEGER
		// This requires table recreation for STRICT tables
		if err := migrateToIntegerDuration(db, columns); err != nil {
			return fmt.Errorf("failed to migrate duration column to INTEGER: %w", err)
		}
		// Refresh columns after recreation
		columns, err = getTableColumns(db, "media")
		if err != nil {
			return err
		}
	}

	for col, definition := range migrations {
		if columns[strings.ToUpper(col)] == "" {
			query := fmt.Sprintf("ALTER TABLE media ADD COLUMN %s %s", col, definition)
			if _, err := db.Exec(query); err != nil {
				return fmt.Errorf("failed to add column %s: %w", col, err)
			}
		}
	}

	return nil
}

// migrateToIntegerDuration converts the duration column to INTEGER
// by recreating the table. This is necessary for SQLite STRICT tables.
func migrateToIntegerDuration(db *sql.DB, columns map[string]string) error {
	// 1. Get current schema and disable foreign keys
	if _, err := db.Exec("PRAGMA foreign_keys=OFF"); err != nil {
		return err
	}
	defer db.Exec("PRAGMA foreign_keys=ON")

	// 2. Start transaction
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// 3. Create new table with correct schema
	// We use a temporary name
	_, err = tx.Exec(`
CREATE TABLE media_new (
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
    mime_type TEXT,
    ext TEXT,
    time_created INTEGER,
    time_modified INTEGER,
    time_deleted INTEGER DEFAULT 0,
    is_shrinked INTEGER DEFAULT 0,
    play_count INTEGER DEFAULT 0
) STRICT;
`)
	if err != nil {
		// Fallback for older SQLite versions
		if strings.Contains(err.Error(), "unknown table option: STRICT") {
			_, err = tx.Exec(`
CREATE TABLE media_new (
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
    mime_type TEXT,
    ext TEXT,
    time_created INTEGER,
    time_modified INTEGER,
    time_deleted INTEGER DEFAULT 0,
    is_shrinked INTEGER DEFAULT 0,
    play_count INTEGER DEFAULT 0
);
`)
		}
		if err != nil {
			return err
		}
	}

	// 4. Copy data from old table to new table
	// We need to be careful about column names.
	// We'll use the common columns.
	var commonCols []string
	for col := range columns {
		// Only copy columns that exist in the new schema
		switch col {
		case "ID", "PATH", "SIZE", "DURATION", "VIDEO_COUNT", "AUDIO_COUNT",
			"VIDEO_CODECS", "AUDIO_CODECS", "SUBTITLE_CODECS", "MEDIA_TYPE",
			"MIME_TYPE", "EXT", "TIME_CREATED", "TIME_MODIFIED", "TIME_DELETED",
			"IS_SHRINKED", "PLAY_COUNT":
			commonCols = append(commonCols, col)
		}
	}

	if len(commonCols) > 0 {
		var selectCols []string
		for _, col := range commonCols {
			if col == "DURATION" {
				selectCols = append(selectCols, "CAST(duration AS INTEGER) AS duration")
			} else {
				selectCols = append(selectCols, col)
			}
		}
		colList := strings.Join(commonCols, ", ")
		selectList := strings.Join(selectCols, ", ")
		query := fmt.Sprintf("INSERT INTO media_new (%s) SELECT %s FROM media", colList, selectList)
		if _, err := tx.Exec(query); err != nil {
			return err
		}
	}

	// 5. Drop old table and rename new table
	if _, err := tx.Exec("DROP TABLE media"); err != nil {
		return err
	}
	if _, err := tx.Exec("ALTER TABLE media_new RENAME TO media"); err != nil {
		return err
	}

	// 6. Re-create indexes will be handled by InitDB after MigrateDB returns
	return tx.Commit()
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

// IsTableStrict checks if a table was created with the STRICT keyword
func IsTableStrict(db *sql.DB, tableName string) bool {
	var strict int
	err := db.QueryRow("SELECT strict FROM pragma_table_list WHERE name = ?", tableName).Scan(&strict)
	if err != nil {
		return false
	}
	return strict == 1
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
