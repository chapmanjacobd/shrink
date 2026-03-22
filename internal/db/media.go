package db

import (
	"database/sql"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/chapmanjacobd/shrink/internal/utils"
)

// MediaRecord represents a row in the media table
type MediaRecord struct {
	Path           string
	Size           int64
	Duration       float64
	VideoCount     int
	AudioCount     int
	VideoCodecs    string
	AudioCodecs    string
	SubtitleCodecs string
	MediaType      string
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
            COALESCE(media_type, '')
		FROM media
		WHERE COALESCE(time_deleted, 0) = 0
            AND size > 0
	`

	if !forceShrink {
		query += " AND COALESCE(is_shrinked, 0) = 0"
	}

	// Filter by media type (prefilter in database)
	var typeConditions []string
	if videoOnly {
		typeConditions = append(typeConditions, "media_type = 'video'")
	}
	if audioOnly {
		typeConditions = append(typeConditions, "media_type = 'audio'", "media_type = 'audiobook'")
	}
	if imageOnly {
		typeConditions = append(typeConditions, "media_type = 'image'")
	}
	if textOnly {
		typeConditions = append(typeConditions, "media_type = 'text'")
	}
	if len(typeConditions) > 0 {
		query += " AND (" + strings.Join(typeConditions, " OR ") + ")"
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
			&m.VideoCodecs, &m.AudioCodecs, &m.SubtitleCodecs, &m.MediaType)
		if err != nil {
			slog.Error("Scan error", "error", err)
			continue
		}
		media = append(media, m)
	}

	return media, rows.Err()
}

// MarkDeleted marks a file as deleted in all provided databases
func MarkDeleted(databases []string, path string) {
	for _, dbPath := range databases {
		if IsDatabaseDirectory(dbPath) {
			continue
		}
		sqlDB, _, err := ConnectWithInit(dbPath)
		if err == nil {
			_, err := sqlDB.Exec("UPDATE media SET time_deleted = ? WHERE path = ?", time.Now().Unix(), path)
			if err != nil {
				slog.Warn("Failed to mark file deleted in database", "path", path, "error", err)
			}
			sqlDB.Close()
		}
	}
}

// UpdateMedia replaces an old path with a new one and updates its size/duration
func UpdateMedia(databases []string, oldPath, newPath string, newSize int64, duration float64) {
	for _, dbPath := range databases {
		if IsDatabaseDirectory(dbPath) {
			continue
		}
		sqlDB, _, err := ConnectWithInit(dbPath)
		if err == nil {
			_, _ = sqlDB.Exec("DELETE FROM media WHERE path = ?", newPath)
			var execErr error
			if duration > 0 {
				_, execErr = sqlDB.Exec(
					"UPDATE media SET path = ?, size = ?, duration = ?, time_deleted = 0, is_shrinked = 1 WHERE path = ?",
					newPath, newSize, duration, oldPath)
			} else {
				_, execErr = sqlDB.Exec(
					"UPDATE media SET path = ?, size = ?, time_deleted = 0, is_shrinked = 1 WHERE path = ?",
					newPath, newSize, oldPath)
			}
			if execErr != nil {
				slog.Warn("Failed to update database entry", "oldPath", oldPath, "newPath", newPath, "error", execErr)
			}
			sqlDB.Close()
		}
	}
}

// AddMediaEntry adds a new media entry to the database
func AddMediaEntry(databases []string, path string, size int64, duration float64) {
	for _, dbPath := range databases {
		if IsDatabaseDirectory(dbPath) {
			continue
		}
		sqlDB, _, err := ConnectWithInit(dbPath)
		if err == nil {
			_, err := sqlDB.Exec("DELETE FROM media WHERE path = ?", path)
			if err != nil {
				slog.Warn("Failed to delete existing media entry", "path", path, "error", err)
			}
			var execErr error
			if duration > 0 {
				_, execErr = sqlDB.Exec(
					"INSERT INTO media (path, size, duration, time_deleted, is_shrinked) VALUES (?, ?, ?, 0, 0)",
					path, size, duration)
			} else {
				_, execErr = sqlDB.Exec(
					"INSERT INTO media (path, size, time_deleted, is_shrinked) VALUES (?, ?, 0, 0)",
					path, size)
			}
			if execErr != nil {
				slog.Warn("Failed to add database entry", "path", path, "error", execErr)
			}
			sqlDB.Close()
		}
	}
}

// MarkShrinked marks a file as shrinked in the database
func MarkShrinked(databases []string, path string) {
	for _, dbPath := range databases {
		if IsDatabaseDirectory(dbPath) {
			continue
		}
		sqlDB, _, err := ConnectWithInit(dbPath)
		if err == nil {
			_, err := sqlDB.Exec("UPDATE media SET is_shrinked = 1 WHERE path = ?", path)
			if err != nil {
				slog.Warn("Failed to mark file as shrinked in database", "path", path, "error", err)
			}
			sqlDB.Close()
		}
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
