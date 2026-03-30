// Package db provides database access for archive analysis cache.
package db

import (
	"database/sql"
	"log/slog"
)

// ArchiveCacheEntry represents a cached archive analysis result
type ArchiveCacheEntry struct {
	Path             string
	TotalArchiveSize int64
	FutureSize       int64
	ProcessingTime   int
	HasProcessable   bool
	IsBroken         bool
	PartFiles        string // JSON-encoded list of part file paths
}

// InitArchiveCache creates the archive_cache table if it doesn't exist
func InitArchiveCache(db *sql.DB) error {
	createTableSQL := `
	CREATE TABLE IF NOT EXISTS archive_cache (
		path TEXT PRIMARY KEY,
		total_archive_size INTEGER DEFAULT 0,
		future_size INTEGER DEFAULT 0,
		processing_time INTEGER DEFAULT 0,
		has_processable INTEGER DEFAULT 0,
		is_broken INTEGER DEFAULT 0,
		part_files TEXT DEFAULT ''
	);
	`
	_, err := db.Exec(createTableSQL)
	return err
}

// GetArchiveCache retrieves cached analysis results for an archive
func GetArchiveCache(db *sql.DB, path string) (*ArchiveCacheEntry, error) {
	query := `
	SELECT path, total_archive_size, future_size, processing_time, 
           has_processable, is_broken, part_files
	FROM archive_cache
	WHERE path = ?
	`

	row := db.QueryRow(query, path)
	var entry ArchiveCacheEntry
	var hasProc, isBroken int64

	err := row.Scan(
		&entry.Path, &entry.TotalArchiveSize, &entry.FutureSize,
		&entry.ProcessingTime, &hasProc, &isBroken,
		&entry.PartFiles,
	)

	if err == sql.ErrNoRows {
		return nil, nil // No cache entry found
	}
	if err != nil {
		return nil, err
	}

	entry.HasProcessable = hasProc == 1
	entry.IsBroken = isBroken == 1

	return &entry, nil
}

// SetArchiveCache stores or updates archive analysis results in the cache
func SetArchiveCache(db *sql.DB, entry *ArchiveCacheEntry) error {
	hasProc := 0
	if entry.HasProcessable {
		hasProc = 1
	}
	isBroken := 0
	if entry.IsBroken {
		isBroken = 1
	}

	upsertSQL := `
	INSERT INTO archive_cache (
		path, total_archive_size, future_size, processing_time,
		has_processable, is_broken, part_files
	) VALUES (?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(path) DO UPDATE SET
		total_archive_size = excluded.total_archive_size,
		future_size = excluded.future_size,
		processing_time = excluded.processing_time,
		has_processable = excluded.has_processable,
		is_broken = excluded.is_broken,
		part_files = excluded.part_files
	`

	_, err := db.Exec(upsertSQL,
		entry.Path, entry.TotalArchiveSize, entry.FutureSize,
		entry.ProcessingTime, hasProc, isBroken,
		entry.PartFiles,
	)

	return err
}

// DeleteArchiveCache removes a cache entry for a specific archive
func DeleteArchiveCache(db *sql.DB, path string) error {
	_, err := db.Exec("DELETE FROM archive_cache WHERE path = ?", path)
	return err
}

// BulkSetArchiveCache stores multiple cache entries in a single transaction
func BulkSetArchiveCache(db *sql.DB, entries []ArchiveCacheEntry) error {
	tx, err := BeginImmediate(db)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	upsertSQL := `
	INSERT INTO archive_cache (
		path, total_archive_size, future_size, processing_time,
		has_processable, is_broken, part_files
	) VALUES (?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(path) DO UPDATE SET
		total_archive_size = excluded.total_archive_size,
		future_size = excluded.future_size,
		processing_time = excluded.processing_time,
		has_processable = excluded.has_processable,
		is_broken = excluded.is_broken,
		part_files = excluded.part_files
	`

	for _, entry := range entries {
		hasProc := 0
		if entry.HasProcessable {
			hasProc = 1
		}
		isBroken := 0
		if entry.IsBroken {
			isBroken = 1
		}

		_, err := tx.Exec(upsertSQL,
			entry.Path, entry.TotalArchiveSize, entry.FutureSize,
			entry.ProcessingTime, hasProc, isBroken,
			entry.PartFiles,
		)
		if err != nil {
			slog.Warn("Failed to cache archive analysis", "path", entry.Path, "error", err)
			// Continue with other entries
		}
	}

	return tx.Commit()
}
