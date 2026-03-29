// Package db provides database access for archive analysis cache.
package db

import (
	"database/sql"
	"log/slog"
	"time"
)

// ArchiveCacheEntry represents a cached archive analysis result
type ArchiveCacheEntry struct {
	Path              string
	TotalArchiveSize  int64
	FutureSize        int64
	ProcessingTime    int
	HasProcessable    bool
	IsBroken          bool
	PartFiles         string // JSON-encoded list of part file paths
	AnalysisTimestamp int64
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
		part_files TEXT DEFAULT '',
		analysis_timestamp INTEGER DEFAULT 0
	);
	CREATE INDEX IF NOT EXISTS idx_archive_cache_timestamp ON archive_cache(analysis_timestamp);
	`
	_, err := db.Exec(createTableSQL)
	return err
}

// GetArchiveCache retrieves cached analysis results for an archive
func GetArchiveCache(db *sql.DB, path string) (*ArchiveCacheEntry, error) {
	query := `
	SELECT path, total_archive_size, future_size, processing_time, 
           has_processable, is_broken, part_files, analysis_timestamp
	FROM archive_cache
	WHERE path = ?
	`

	row := db.QueryRow(query, path)
	var entry ArchiveCacheEntry
	var hasProc, isBroken int64

	err := row.Scan(
		&entry.Path, &entry.TotalArchiveSize, &entry.FutureSize,
		&entry.ProcessingTime, &hasProc, &isBroken,
		&entry.PartFiles, &entry.AnalysisTimestamp,
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
		has_processable, is_broken, part_files, analysis_timestamp
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(path) DO UPDATE SET
		total_archive_size = excluded.total_archive_size,
		future_size = excluded.future_size,
		processing_time = excluded.processing_time,
		has_processable = excluded.has_processable,
		is_broken = excluded.is_broken,
		part_files = excluded.part_files,
		analysis_timestamp = excluded.analysis_timestamp
	`

	_, err := db.Exec(upsertSQL,
		entry.Path, entry.TotalArchiveSize, entry.FutureSize,
		entry.ProcessingTime, hasProc, isBroken,
		entry.PartFiles, entry.AnalysisTimestamp,
	)

	return err
}

// DeleteArchiveCache removes a cache entry for a specific archive
func DeleteArchiveCache(db *sql.DB, path string) error {
	_, err := db.Exec("DELETE FROM archive_cache WHERE path = ?", path)
	return err
}

// CleanupOldArchiveCache removes cache entries older than the specified duration
func CleanupOldArchiveCache(db *sql.DB, maxAge time.Duration) error {
	cutoff := time.Now().Add(-maxAge).Unix()
	_, err := db.Exec("DELETE FROM archive_cache WHERE analysis_timestamp < ?", cutoff)
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
		has_processable, is_broken, part_files, analysis_timestamp
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(path) DO UPDATE SET
		total_archive_size = excluded.total_archive_size,
		future_size = excluded.future_size,
		processing_time = excluded.processing_time,
		has_processable = excluded.has_processable,
		is_broken = excluded.is_broken,
		part_files = excluded.part_files,
		analysis_timestamp = excluded.analysis_timestamp
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
			entry.PartFiles, entry.AnalysisTimestamp,
		)
		if err != nil {
			slog.Warn("Failed to cache archive analysis", "path", entry.Path, "error", err)
			// Continue with other entries
		}
	}

	return tx.Commit()
}

// GetStaleArchiveCache returns cache entries older than the specified duration
func GetStaleArchiveCache(db *sql.DB, maxAge time.Duration) ([]ArchiveCacheEntry, error) {
	cutoff := time.Now().Add(-maxAge).Unix()
	query := `
	SELECT path, total_archive_size, future_size, processing_time, 
           has_processable, is_broken, part_files, analysis_timestamp
	FROM archive_cache
	WHERE analysis_timestamp < ?
	`

	rows, err := db.Query(query, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []ArchiveCacheEntry
	for rows.Next() {
		var entry ArchiveCacheEntry
		var hasProc, isBroken int64

		err := rows.Scan(
			&entry.Path, &entry.TotalArchiveSize, &entry.FutureSize,
			&entry.ProcessingTime, &hasProc, &isBroken,
			&entry.PartFiles, &entry.AnalysisTimestamp,
		)
		if err != nil {
			slog.Warn("Failed to scan cache entry", "error", err)
			continue
		}

		entry.HasProcessable = hasProc == 1
		entry.IsBroken = isBroken == 1
		entries = append(entries, entry)
	}

	return entries, rows.Err()
}
