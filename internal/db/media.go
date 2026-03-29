package db

import (
	"database/sql"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/chapmanjacobd/shrink/internal/utils"
)

// ShrinkStatus codes for tracking processing outcomes
// 0 = not processed, 1 = success, >1 = various failure/skip states
const (
	ShrinkStatusNotProcessed = 0 // File has not been processed yet
	ShrinkStatusSuccess      = 1 // Successfully processed and saved space
	ShrinkStatusTooLarge     = 2 // Transcoded file was larger than original (kept original)
	ShrinkStatusUnplayable   = 3 // File was unplayable/corrupt
	ShrinkStatusError        = 5 // Processing error (file-specific, not environment)
	ShrinkStatusSkipped      = 6 // Skipped (no savings, already optimized, etc.)
	ShrinkStatusBroken       = 7 // File is broken (moved to broken directory)
)

// MediaRecord represents a row in the media table
type MediaRecord struct {
	Path           string
	VideoCodecs    string
	AudioCodecs    string
	SubtitleCodecs string
	MediaType      string
	Size           int64
	Duration       float64
	VideoCount     int
	AudioCount     int
	Width          int
	Height         int
	IsShrinked     int // Status code: 0=not processed, 1=success, >1=various states
}

// LoadMediaFromDB loads all processable media from a database
func LoadMediaFromDB(db *sql.DB, forceShrink bool, videoOnly, audioOnly, imageOnly, textOnly bool) ([]MediaRecord, error) {
	query := `
		SELECT path,
            size,
            COALESCE(duration, 0),
            COALESCE(video_count, 0),
            COALESCE(audio_count, 0),
            COALESCE(video_codecs, ''),
            COALESCE(audio_codecs, ''),
            COALESCE(subtitle_codecs, ''),
            COALESCE(media_type, ''),
            COALESCE(width, 0),
            COALESCE(height, 0)
		FROM media
		WHERE COALESCE(time_deleted, 0) = 0
            AND size > 0
	`

	if !forceShrink {
		query += " AND COALESCE(is_shrinked, 0) = 0"
	}

	rows, err := db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var media []MediaRecord
	for rows.Next() {
		var m MediaRecord
		err := rows.Scan(&m.Path, &m.Size, &m.Duration, &m.VideoCount, &m.AudioCount,
			&m.VideoCodecs, &m.AudioCodecs, &m.SubtitleCodecs, &m.MediaType,
			&m.Width, &m.Height)
		if err != nil {
			slog.Error("Scan error", "error", err)
			continue
		}
		media = append(media, m)
	}

	return media, rows.Err()
}

// MarkDeleted marks a file as deleted in all provided databases
func MarkDeleted(databases []*sql.DB, path string) {
	for _, sqlDB := range databases {
		_, err := sqlDB.Exec("UPDATE media SET time_deleted = ? WHERE path = ?", time.Now().Unix(), path)
		if err != nil {
			slog.Warn("Failed to mark file deleted in database", "path", path, "error", err)
		}
	}
}

// UpdateMedia replaces an old path with a new one and updates its size/duration/dimensions
func UpdateMedia(databases []*sql.DB, oldPath, newPath string, newSize int64, duration float64, width, height int) {
	for _, sqlDB := range databases {
		var execErr error
		// Check if newPath already exists
		var exists int
		err := sqlDB.QueryRow("SELECT COUNT(*) FROM media WHERE path = ?", newPath).Scan(&exists)
		if err != nil {
			slog.Warn("Failed to check existing path", "newPath", newPath, "error", err)
			continue
		}

		if exists > 0 {
			// newPath exists: update it with new values, then delete oldPath
			if duration > 0 {
				_, execErr = sqlDB.Exec(
					"UPDATE media SET size = ?, duration = ?, width = ?, height = ?, time_deleted = 0, is_shrinked = ? WHERE path = ?",
					newSize, int64(math.Round(duration)), width, height, ShrinkStatusSuccess, newPath)
			} else {
				_, execErr = sqlDB.Exec(
					"UPDATE media SET size = ?, width = ?, height = ?, time_deleted = 0, is_shrinked = ? WHERE path = ?",
					newSize, width, height, ShrinkStatusSuccess, newPath)
			}
			if execErr != nil {
				slog.Warn("Failed to update database entry", "oldPath", oldPath, "newPath", newPath, "error", execErr)
				continue
			}
			// Delete the old row
			_, _ = sqlDB.Exec("DELETE FROM media WHERE path = ?", oldPath)
		} else {
			// newPath doesn't exist: update oldPath to newPath
			_, _ = sqlDB.Exec("DELETE FROM media WHERE path = ?", newPath)
			if duration > 0 {
				_, execErr = sqlDB.Exec(
					"UPDATE media SET path = ?, size = ?, duration = ?, width = ?, height = ?, time_deleted = 0, is_shrinked = ? WHERE path = ?",
					newPath, newSize, int64(math.Round(duration)), width, height, ShrinkStatusSuccess, oldPath)
			} else {
				_, execErr = sqlDB.Exec(
					"UPDATE media SET path = ?, size = ?, width = ?, height = ?, time_deleted = 0, is_shrinked = ? WHERE path = ?",
					newPath, newSize, width, height, ShrinkStatusSuccess, oldPath)
			}
			if execErr != nil {
				slog.Warn("Failed to update database entry", "oldPath", oldPath, "newPath", newPath, "error", execErr)
			}
		}
	}
}

// AddMediaEntry adds a new media entry to the database with a specific status
func AddMediaEntry(databases []*sql.DB, path string, size int64, duration float64, status int) {
	AddMediaEntryWithDimensions(databases, path, size, duration, 0, 0, status)
}

// AddMediaEntryWithDimensions adds a new media entry to the database with dimensions and status
func AddMediaEntryWithDimensions(databases []*sql.DB, path string, size int64, duration float64, width, height int, status int) {
	for _, sqlDB := range databases {
		_, err := sqlDB.Exec("DELETE FROM media WHERE path = ?", path)
		if err != nil {
			slog.Warn("Failed to delete existing media entry", "path", path, "error", err)
		}
		var execErr error
		if duration > 0 {
			_, execErr = sqlDB.Exec(
				"INSERT INTO media (path, size, duration, width, height, time_deleted, is_shrinked) VALUES (?, ?, ?, ?, ?, 0, ?)",
				path, size, int64(math.Round(duration)), width, height, status)
		} else {
			_, execErr = sqlDB.Exec(
				"INSERT INTO media (path, size, width, height, time_deleted, is_shrinked) VALUES (?, ?, ?, ?, 0, ?)",
				path, size, width, height, status)
		}
		if execErr != nil {
			slog.Warn("Failed to add database entry", "path", path, "error", execErr)
		}
	}
}

// MarkShrinked marks a file as shrinked in the database with the given status code
func MarkShrinked(databases []*sql.DB, path string, status int) {
	if status <= 0 {
		status = ShrinkStatusSuccess
	}
	for _, sqlDB := range databases {
		_, err := sqlDB.Exec("UPDATE media SET is_shrinked = ? WHERE path = ?", status, path)
		if err != nil {
			slog.Warn("Failed to mark file status in database", "path", path, "status", status, "error", err)
		}
	}
}

// MarkSuccess marks a file as successfully processed
func MarkSuccess(databases []*sql.DB, path string) {
	MarkShrinked(databases, path, ShrinkStatusSuccess)
}

// MarkTooLarge marks a file as processed but result was larger than original
func MarkTooLarge(databases []*sql.DB, path string) {
	MarkShrinked(databases, path, ShrinkStatusTooLarge)
}

// MarkUnplayable marks a file as unplayable/corrupt
func MarkUnplayable(databases []*sql.DB, path string) {
	MarkShrinked(databases, path, ShrinkStatusUnplayable)
}

// MarkProcessingError marks a file as having a processing error
func MarkProcessingError(databases []*sql.DB, path string) {
	MarkShrinked(databases, path, ShrinkStatusError)
}

// MarkSkipped marks a file as skipped (no savings, already optimized, etc.)
func MarkSkipped(databases []*sql.DB, path string) {
	MarkShrinked(databases, path, ShrinkStatusSkipped)
}

// MarkBroken marks a file as broken (moved to broken directory)
func MarkBroken(databases []*sql.DB, path string) {
	MarkShrinked(databases, path, ShrinkStatusBroken)
}

// BulkMarkOptimizedExtensions marks files with already-optimized extensions as shrinked
func BulkMarkOptimizedExtensions(databases []*sql.DB) {
	for _, sqlDB := range databases {
		// Use IMMEDIATE transaction to acquire write lock upfront
		tx, err := BeginImmediate(sqlDB)
		if err != nil {
			slog.Warn("Failed to start transaction for bulk mark", "error", err)
			continue
		}

		for _, ext := range utils.OptimizedExtensions {
			// Use LIKE with LOWER to handle case-insensitive matching
			_, err := tx.Exec(
				"UPDATE media SET is_shrinked = ? WHERE LOWER(path) LIKE ? AND COALESCE(time_deleted, 0) = 0",
				ShrinkStatusSkipped, "%"+ext,
			)
			if err != nil {
				slog.Warn("Failed to bulk mark optimized extensions", "extension", ext, "error", err)
				tx.Rollback()
				goto NextDB
			}
		}

		if err := tx.Commit(); err != nil {
			slog.Warn("Failed to commit transaction for bulk mark", "error", err)
			tx.Rollback()
		}

	NextDB:
	}
}

// IsDatabaseFile checks if a path is a SQLite database file
func IsDatabaseFile(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	if slices.Contains(utils.SQLiteExtensions, ext) {
		return true
	}
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	if !info.IsDir() {
		for _, dbExt := range utils.SQLiteExtensions {
			if strings.HasSuffix(strings.ToLower(path), dbExt) {
				return true
			}
		}
	}
	return false
}

// IsDatabaseDirectory checks if a path is a directory (not a database file)
func IsDatabaseDirectory(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.IsDir()
}
