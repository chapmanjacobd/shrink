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
	PartFiles        string // pipe-separated list of part file paths
	SourceSize       int64  // archive size when analyzed
	SourceMtime      int64  // archive modification time when analyzed
}

// InitArchiveCache creates the archive_cache table if it doesn't exist and
// migrates older tables that predate source metadata tracking.
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
		source_size INTEGER DEFAULT 0,
		source_mtime INTEGER DEFAULT 0
	);
	`
	if _, err := db.Exec(createTableSQL); err != nil {
		return err
	}

	columns, err := archiveCacheColumns(db)
	if err != nil {
		return err
	}
	if !columns["source_size"] {
		if _, err := db.Exec("ALTER TABLE archive_cache ADD COLUMN source_size INTEGER DEFAULT 0"); err != nil {
			return err
		}
	}
	if !columns["source_mtime"] {
		if _, err := db.Exec("ALTER TABLE archive_cache ADD COLUMN source_mtime INTEGER DEFAULT 0"); err != nil {
			return err
		}
	}
	return nil
}

func archiveCacheColumns(db *sql.DB) (map[string]bool, error) {
	rows, err := db.Query("PRAGMA table_info(archive_cache)")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

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
	return columns, rows.Err()
}

// GetArchiveCache retrieves cached analysis results for an archive
func GetArchiveCache(db *sql.DB, path string) (*ArchiveCacheEntry, error) {
	query := `
	SELECT path, total_archive_size, future_size, processing_time, 
           has_processable, is_broken, part_files, source_size, source_mtime
	FROM archive_cache
	WHERE path = ?
	`

	row := db.QueryRow(query, path)
	var entry ArchiveCacheEntry
	var hasProc, isBroken int64

	err := row.Scan(
		&entry.Path, &entry.TotalArchiveSize, &entry.FutureSize,
		&entry.ProcessingTime, &hasProc, &isBroken,
		&entry.PartFiles, &entry.SourceSize, &entry.SourceMtime,
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
		has_processable, is_broken, part_files, source_size, source_mtime
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(path) DO UPDATE SET
		total_archive_size = excluded.total_archive_size,
		future_size = excluded.future_size,
		processing_time = excluded.processing_time,
		has_processable = excluded.has_processable,
		is_broken = excluded.is_broken,
		part_files = excluded.part_files,
		source_size = excluded.source_size,
		source_mtime = excluded.source_mtime
	`

	_, err := db.Exec(upsertSQL,
		entry.Path, entry.TotalArchiveSize, entry.FutureSize,
		entry.ProcessingTime, hasProc, isBroken,
		entry.PartFiles, entry.SourceSize, entry.SourceMtime,
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
	defer func() { _ = tx.Rollback() }()

	upsertSQL := `
	INSERT INTO archive_cache (
		path, total_archive_size, future_size, processing_time,
		has_processable, is_broken, part_files, source_size, source_mtime
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(path) DO UPDATE SET
		total_archive_size = excluded.total_archive_size,
		future_size = excluded.future_size,
		processing_time = excluded.processing_time,
		has_processable = excluded.has_processable,
		is_broken = excluded.is_broken,
		part_files = excluded.part_files,
		source_size = excluded.source_size,
		source_mtime = excluded.source_mtime
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
			entry.PartFiles, entry.SourceSize, entry.SourceMtime,
		)
		if err != nil {
			slog.Warn("Failed to cache archive analysis", "path", entry.Path, "error", err)
			// Continue with other entries
		}
	}

	return tx.Commit()
}
