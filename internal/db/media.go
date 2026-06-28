package db

import (
	"database/sql"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sort"
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

type mediaPathReference struct {
	TableName         string
	ColumnName        string
	UniqueConstraints [][]string
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
	defer func() { _ = rows.Close() }()

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
		if err := updateMediaRecord(sqlDB, oldPath, newPath, newSize, duration, width, height); err != nil {
			slog.Warn("Failed to update database entry", "oldPath", oldPath, "newPath", newPath, "error", err)
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
		_, execErr := sqlDB.Exec(
			`INSERT INTO media (path, size, duration, width, height, time_deleted, is_shrinked)
			 VALUES (?, ?, ?, ?, ?, 0, ?)
			 ON CONFLICT(path) DO UPDATE SET
			 	size = excluded.size,
			 	duration = excluded.duration,
			 	width = excluded.width,
			 	height = excluded.height,
			 	time_deleted = 0,
			 	is_shrinked = excluded.is_shrinked`,
			path, size, int64(math.Round(duration)), width, height, status,
		)
		if execErr != nil {
			slog.Warn("Failed to add database entry", "path", path, "error", execErr)
		}
	}
}

func updateMediaRecord(sqlDB *sql.DB, oldPath, newPath string, newSize int64, duration float64, width, height int) error {
	tx, err := sqlDB.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec("PRAGMA defer_foreign_keys = ON"); err != nil {
		return fmt.Errorf("enable deferred foreign keys: %w", err)
	}

	refs, err := discoverMediaPathReferences(tx)
	if err != nil {
		return err
	}

	if oldPath != newPath {
		if err := reassignMediaPathReferences(tx, refs, oldPath, newPath); err != nil {
			return err
		}
	}

	if oldPath == newPath {
		if err := applyMediaAttributes(tx, oldPath, newSize, duration, width, height); err != nil {
			return err
		}
	} else {
		exists, err := mediaPathExists(tx, newPath)
		if err != nil {
			return err
		}

		if exists {
			if err := applyMediaAttributes(tx, newPath, newSize, duration, width, height); err != nil {
				return err
			}
			if _, err := tx.Exec("DELETE FROM media WHERE path = ?", oldPath); err != nil {
				return err
			}
		} else {
			if err := renameMediaRecord(tx, oldPath, newPath, newSize, duration, width, height); err != nil {
				return err
			}
		}
	}

	return tx.Commit()
}

func mediaPathExists(tx *sql.Tx, path string) (bool, error) {
	var exists int
	if err := tx.QueryRow("SELECT COUNT(*) FROM media WHERE path = ?", path).Scan(&exists); err != nil {
		return false, fmt.Errorf("check existing path %q: %w", path, err)
	}
	return exists > 0, nil
}

func applyMediaAttributes(tx *sql.Tx, path string, newSize int64, duration float64, width, height int) error {
	var (
		result sql.Result
		err    error
	)
	if duration > 0 {
		result, err = tx.Exec(
			"UPDATE media SET size = ?, duration = ?, width = ?, height = ?, time_deleted = 0, is_shrinked = ? WHERE path = ?",
			newSize, int64(math.Round(duration)), width, height, ShrinkStatusSuccess, path,
		)
	} else {
		result, err = tx.Exec(
			"UPDATE media SET size = ?, width = ?, height = ?, time_deleted = 0, is_shrinked = ? WHERE path = ?",
			newSize, width, height, ShrinkStatusSuccess, path,
		)
	}
	if err != nil {
		return err
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rowsAffected == 0 {
		return fmt.Errorf("no media row found for path %q", path)
	}

	return nil
}

func renameMediaRecord(tx *sql.Tx, oldPath, newPath string, newSize int64, duration float64, width, height int) error {
	var (
		result sql.Result
		err    error
	)
	if duration > 0 {
		result, err = tx.Exec(
			"UPDATE media SET path = ?, size = ?, duration = ?, width = ?, height = ?, time_deleted = 0, is_shrinked = ? WHERE path = ?",
			newPath, newSize, int64(math.Round(duration)), width, height, ShrinkStatusSuccess, oldPath,
		)
	} else {
		result, err = tx.Exec(
			"UPDATE media SET path = ?, size = ?, width = ?, height = ?, time_deleted = 0, is_shrinked = ? WHERE path = ?",
			newPath, newSize, width, height, ShrinkStatusSuccess, oldPath,
		)
	}
	if err != nil {
		return err
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rowsAffected == 0 {
		return fmt.Errorf("no media row found for path %q", oldPath)
	}

	return nil
}

func reassignMediaPathReferences(tx *sql.Tx, refs []mediaPathReference, oldPath, newPath string) error {
	for _, ref := range refs {
		if err := deleteConflictingReferenceRows(tx, ref, oldPath, newPath); err != nil {
			return err
		}
		if err := updateReferenceRows(tx, ref, oldPath, newPath); err != nil {
			return err
		}
	}

	return nil
}

func deleteConflictingReferenceRows(tx *sql.Tx, ref mediaPathReference, oldPath, newPath string) error {
	qTable := quoteIdentifier(ref.TableName)
	qColumn := quoteIdentifier(ref.ColumnName)

	for _, constraint := range ref.UniqueConstraints {
		var comparisons []string
		for _, column := range constraint {
			if strings.EqualFold(column, ref.ColumnName) {
				continue
			}
			qConstraintColumn := quoteIdentifier(column)
			comparisons = append(comparisons, fmt.Sprintf("existing.%s IS %s.%s", qConstraintColumn, qTable, qConstraintColumn))
		}

		deleteSQL := fmt.Sprintf(
			"DELETE FROM %s WHERE %s = ? AND EXISTS (SELECT 1 FROM %s AS existing WHERE existing.%s = ?",
			qTable, qColumn, qTable, qColumn,
		)
		if len(comparisons) > 0 {
			deleteSQL += " AND " + strings.Join(comparisons, " AND ")
		}
		deleteSQL += ")"

		if _, err := tx.Exec(deleteSQL, oldPath, newPath); err != nil {
			return fmt.Errorf("delete conflicting rows from %s.%s: %w", ref.TableName, ref.ColumnName, err)
		}
	}

	return nil
}

func updateReferenceRows(tx *sql.Tx, ref mediaPathReference, oldPath, newPath string) error {
	qTable := quoteIdentifier(ref.TableName)
	qColumn := quoteIdentifier(ref.ColumnName)
	updateSQL := fmt.Sprintf("UPDATE %s SET %s = ? WHERE %s = ?", qTable, qColumn, qColumn)
	if _, err := tx.Exec(updateSQL, newPath, oldPath); err != nil {
		return fmt.Errorf("update references in %s.%s: %w", ref.TableName, ref.ColumnName, err)
	}
	return nil
}

func discoverMediaPathReferences(tx *sql.Tx) ([]mediaPathReference, error) {
	rows, err := tx.Query("SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' AND name != 'media'")
	if err != nil {
		return nil, fmt.Errorf("list tables: %w", err)
	}

	var tableNames []string
	for rows.Next() {
		var tableName string
		if err := rows.Scan(&tableName); err != nil {
			_ = rows.Close()
			return nil, err
		}
		tableNames = append(tableNames, tableName)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	_ = rows.Close()

	var refs []mediaPathReference
	for _, tableName := range tableNames {
		tableRefs, err := discoverTableMediaPathReferences(tx, tableName)
		if err != nil {
			return nil, err
		}
		refs = append(refs, tableRefs...)
	}

	return refs, nil
}

func discoverTableMediaPathReferences(tx *sql.Tx, tableName string) ([]mediaPathReference, error) {
	rows, err := tx.Query(fmt.Sprintf("PRAGMA foreign_key_list(%s)", quoteIdentifier(tableName)))
	if err != nil {
		return nil, fmt.Errorf("list foreign keys for %s: %w", tableName, err)
	}
	defer func() { _ = rows.Close() }()

	type fkRow struct {
		id       int
		seq      int
		table    string
		from     string
		to       string
		onUpdate string
		onDelete string
		match    string
	}

	grouped := map[int][]fkRow{}
	for rows.Next() {
		var row fkRow
		if err := rows.Scan(&row.id, &row.seq, &row.table, &row.from, &row.to, &row.onUpdate, &row.onDelete, &row.match); err != nil {
			return nil, err
		}
		grouped[row.id] = append(grouped[row.id], row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var refs []mediaPathReference
	for _, fkRows := range grouped {
		if len(fkRows) == 0 || !strings.EqualFold(fkRows[0].table, "media") {
			continue
		}
		if len(fkRows) != 1 || !strings.EqualFold(fkRows[0].to, "path") {
			return nil, fmt.Errorf("unsupported composite foreign key from %s to media(path)", tableName)
		}

		uniqueConstraints, err := uniqueConstraintsIncludingColumn(tx, tableName, fkRows[0].from)
		if err != nil {
			return nil, fmt.Errorf("inspect unique constraints for %s.%s: %w", tableName, fkRows[0].from, err)
		}

		refs = append(refs, mediaPathReference{
			TableName:         tableName,
			ColumnName:        fkRows[0].from,
			UniqueConstraints: uniqueConstraints,
		})
	}

	return refs, nil
}

func uniqueConstraintsIncludingColumn(tx *sql.Tx, tableName, columnName string) ([][]string, error) {
	var constraints [][]string
	seen := map[string]struct{}{}

	pkColumns, err := primaryKeyColumns(tx, tableName)
	if err != nil {
		return nil, err
	}
	if containsColumn(pkColumns, columnName) {
		addUniqueConstraint(&constraints, seen, pkColumns)
	}

	rows, err := tx.Query(fmt.Sprintf("PRAGMA index_list(%s)", quoteIdentifier(tableName)))
	if err != nil {
		return nil, fmt.Errorf("list indexes for %s: %w", tableName, err)
	}

	type uniqueIndex struct {
		name    string
		partial bool
	}
	var indexes []uniqueIndex
	for rows.Next() {
		var (
			seq     int
			name    string
			unique  int
			origin  string
			partial int
		)
		if err := rows.Scan(&seq, &name, &unique, &origin, &partial); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if unique == 0 {
			continue
		}
		indexes = append(indexes, uniqueIndex{name: name, partial: partial == 1})
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	_ = rows.Close()

	for _, index := range indexes {
		indexColumns, hasExpression, err := uniqueIndexColumns(tx, index.name)
		if err != nil {
			return nil, err
		}
		if !containsColumn(indexColumns, columnName) {
			continue
		}
		if index.partial {
			return nil, fmt.Errorf("partial unique index %s is not supported", index.name)
		}
		if hasExpression {
			return nil, fmt.Errorf("expression unique index %s is not supported", index.name)
		}

		addUniqueConstraint(&constraints, seen, indexColumns)
	}

	return constraints, nil
}

func primaryKeyColumns(tx *sql.Tx, tableName string) ([]string, error) {
	rows, err := tx.Query(fmt.Sprintf("PRAGMA table_info(%s)", quoteIdentifier(tableName)))
	if err != nil {
		return nil, fmt.Errorf("list columns for %s: %w", tableName, err)
	}
	defer func() { _ = rows.Close() }()

	pkColumns := map[int]string{}
	for rows.Next() {
		var (
			cid          int
			name         string
			columnType   string
			notNull      int
			defaultValue sql.NullString
			pk           int
		)
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
			return nil, err
		}
		if pk > 0 {
			pkColumns[pk] = name
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if len(pkColumns) == 0 {
		return nil, nil
	}

	keys := make([]int, 0, len(pkColumns))
	for key := range pkColumns {
		keys = append(keys, key)
	}
	sort.Ints(keys)

	ordered := make([]string, 0, len(keys))
	for _, key := range keys {
		ordered = append(ordered, pkColumns[key])
	}
	return ordered, nil
}

func uniqueIndexColumns(tx *sql.Tx, indexName string) ([]string, bool, error) {
	rows, err := tx.Query(fmt.Sprintf("PRAGMA index_xinfo(%s)", quoteIdentifier(indexName)))
	if err != nil {
		return nil, false, fmt.Errorf("list columns for index %s: %w", indexName, err)
	}
	defer func() { _ = rows.Close() }()

	type indexedColumn struct {
		seq  int
		name string
	}

	var (
		columns       []indexedColumn
		hasExpression bool
	)
	for rows.Next() {
		var (
			seqNo int
			cid   int
			name  sql.NullString
			desc  int
			coll  sql.NullString
			key   int
		)
		if err := rows.Scan(&seqNo, &cid, &name, &desc, &coll, &key); err != nil {
			return nil, false, err
		}
		if key == 0 {
			continue
		}
		if !name.Valid {
			hasExpression = true
			continue
		}
		columns = append(columns, indexedColumn{seq: seqNo, name: name.String})
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}

	sort.Slice(columns, func(i, j int) bool {
		return columns[i].seq < columns[j].seq
	})

	names := make([]string, 0, len(columns))
	for _, column := range columns {
		names = append(names, column.name)
	}

	return names, hasExpression, nil
}

func addUniqueConstraint(constraints *[][]string, seen map[string]struct{}, columns []string) {
	if len(columns) == 0 {
		return
	}
	key := strings.ToLower(strings.Join(columns, "\x00"))
	if _, ok := seen[key]; ok {
		return
	}
	seen[key] = struct{}{}
	copied := append([]string(nil), columns...)
	*constraints = append(*constraints, copied)
}

func containsColumn(columns []string, target string) bool {
	for _, column := range columns {
		if strings.EqualFold(column, target) {
			return true
		}
	}
	return false
}

func quoteIdentifier(identifier string) string {
	return `"` + strings.ReplaceAll(identifier, `"`, `""`) + `"`
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
				_ = tx.Rollback()
				goto NextDB
			}
		}

		if err := tx.Commit(); err != nil {
			slog.Warn("Failed to commit transaction for bulk mark", "error", err)
			_ = tx.Rollback()
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
