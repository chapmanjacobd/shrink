package commands

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"sync"
	"testing"

	dbpkg "github.com/chapmanjacobd/shrink/internal/db"
	"github.com/chapmanjacobd/shrink/internal/models"
)

func TestSortByEfficiency(t *testing.T) {
	cmd := &ShrinkCmd{}
	media := []models.ShrinkMedia{
		{Path: "slow", Savings: 1000, ProcessingTime: 100}, // 10 bytes/sec
		{Path: "fast", Savings: 1000, ProcessingTime: 10},  // 100 bytes/sec
	}
	cmd.SortByEfficiency(media)
	if media[0].Path != "fast" {
		t.Errorf("expected fast first, got %s", media[0].Path)
	}
}

func TestGetTimeout(t *testing.T) {
	engCfg := EngineConfig{
		VideoThreads:   2,
		Video4KThreads: 1,
		Timeout: TimeoutFlags{
			VideoTimeoutMult: 2.0,
			VideoTimeout:     "10m",
		},
	}
	engine := NewEngine(nil, nil, engCfg, nil, nil, nil)

	m := models.ShrinkMedia{Category: "Video", Duration: 60}
	timeout := engine.getTimeout(m)
	if timeout.Seconds() != 120 {
		t.Errorf("expected 120s timeout, got %v", timeout.Seconds())
	}

	m.Duration = 0
	timeout = engine.getTimeout(m)
	if timeout.Minutes() != 10 {
		t.Errorf("expected 10m timeout, got %v", timeout.Minutes())
	}
}

func TestFinalizeFileSwapKeepOriginal(t *testing.T) {
	// Create a temp file
	tmpDir := t.TempDir()
	originalPath := filepath.Join(tmpDir, "original.mp4")
	if err := os.WriteFile(originalPath, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}

	e := &Engine{} // mock engine
	m := models.ShrinkMedia{Path: originalPath}
	result := models.ProcessResult{
		Success: true,
		Outputs: []models.ProcessOutputFile{
			{Path: originalPath, Size: 4},
		},
	}

	// This should NOT delete the original file
	e.finalizeFileSwap(m, result, true)

	if _, err := os.Stat(originalPath); os.IsNotExist(err) {
		t.Errorf("original file was deleted but it was in the outputs")
	}
}

func TestFinalizeFileSwapDeleteOriginal(t *testing.T) {
	// Create a temp file
	tmpDir := t.TempDir()
	originalPath := filepath.Join(tmpDir, "original.mp4")
	newPath := filepath.Join(tmpDir, "new.mp4")
	if err := os.WriteFile(originalPath, []byte("original data"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newPath, []byte("new data"), 0o644); err != nil {
		t.Fatal(err)
	}

	e := &Engine{} // mock engine
	m := models.ShrinkMedia{Path: originalPath}
	result := models.ProcessResult{
		Success: true,
		Outputs: []models.ProcessOutputFile{
			{Path: newPath, Size: 8},
		},
	}

	// This SHOULD delete the original file
	e.finalizeFileSwap(m, result, true)

	if _, err := os.Stat(originalPath); err == nil {
		t.Errorf("original file was NOT deleted but it was NOT in the outputs")
	}
	if _, err := os.Stat(newPath); err != nil {
		t.Errorf("new file was deleted but it was in the outputs: %v", err)
	}
}

func TestAnalyzeCategoryProcessesAllJobs(t *testing.T) {
	var running, maxSeen int32
	var wg sync.WaitGroup
	processor := &MockProcessor{category: "Video", running: &running, maxSeen: &maxSeen, wg: &wg}
	registry := &MediaRegistry{processors: []models.MediaProcessor{processor}}
	metrics := NewShrinkMetrics()
	metrics.isTTY = false
	engine := NewEngine(nil, &models.ProcessorConfig{
		Video: models.VideoConfig{MinSavingsVideo: 0},
	}, EngineConfig{AnalysisThreads: 4}, nil, registry, metrics)

	const n = 500
	tempDir := t.TempDir()
	media := make([]models.ShrinkMedia, n)
	indices := make([]int, n)
	for i := range media {
		media[i] = models.ShrinkMedia{
			Path:     filepath.Join(tempDir, "video.mp4"),
			Category: "Video",
			Ext:      ".mp4",
			Size:     100,
		}
		indices[i] = i
	}

	results := engine.analyzeCategory(media, indices, "Video")
	if len(results) != n {
		t.Fatalf("analyzed %d jobs, want %d", len(results), n)
	}
	for i := range n {
		if _, ok := results[i]; !ok {
			t.Fatalf("missing result for job %d", i)
		}
	}
}

func TestAnalyzeSecondaryArchivePartDoesNotMove(t *testing.T) {
	tempDir := t.TempDir()
	zipPath := filepath.Join(tempDir, "archive.zip")
	partPath := filepath.Join(tempDir, "archive.z01")
	for _, path := range []string{zipPath, partPath} {
		if err := os.WriteFile(path, []byte("data"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	processor := &MockProcessor{category: "Archived", running: new(int32), maxSeen: new(int32), wg: &sync.WaitGroup{}}
	registry := &MediaRegistry{processors: []models.MediaProcessor{processor}}
	metrics := NewShrinkMetrics()
	metrics.isTTY = false
	engine := NewEngine(nil, &models.ProcessorConfig{}, EngineConfig{Move: filepath.Join(tempDir, "moved")}, nil, registry, metrics)

	media := []models.ShrinkMedia{{Path: partPath, Ext: ".z01", Category: "Archived"}}
	results := engine.analyzeCategory(media, []int{0}, "Archived")
	result, ok := results[0]
	if !ok {
		t.Fatal("missing analysis result")
	}
	if !result.skip {
		t.Error("expected secondary archive part to be skipped")
	}
	if result.skipMove {
		t.Error("secondary archive part must not be moved independently")
	}
}

type timeoutProcessor struct {
	BaseProcessor
}

func (p *timeoutProcessor) CanProcess(m *models.ShrinkMedia) bool { return true }
func (p *timeoutProcessor) EstimateSize(m *models.ShrinkMedia, cfg *models.ProcessorConfig) models.ProcessableInfo {
	return models.ProcessableInfo{IsTimeout: true}
}

func (p *timeoutProcessor) Process(ctx context.Context, m *models.ShrinkMedia, cfg *models.ProcessorConfig, registry models.ProcessorRegistry) models.ProcessResult {
	return models.ProcessResult{}
}

func TestAnalyzeTimeoutIsNotTreatedAsSkippable(t *testing.T) {
	processor := &timeoutProcessor{BaseProcessor{category: "Archived", requiredTool: "mock"}}
	registry := &MediaRegistry{processors: []models.MediaProcessor{processor}}
	metrics := NewShrinkMetrics()
	metrics.isTTY = false
	engine := NewEngine(nil, &models.ProcessorConfig{}, EngineConfig{Move: t.TempDir()}, nil, registry, metrics)

	media := []models.ShrinkMedia{{Path: "archive.zip", Ext: ".zip", Size: 1000}}
	results := engine.analyzeCategory(media, []int{0}, "Archived")
	result, ok := results[0]
	if !ok {
		t.Fatal("missing analysis result")
	}
	if !result.failed {
		t.Error("expected timed-out analysis to be marked failed")
	}
	if result.skip {
		t.Error("timed-out analysis must not be treated as a skip")
	}
	if result.skipMove {
		t.Error("timed-out analysis must not be moved")
	}
}

func TestUpdateMetadataPreservesForeignKeyRows(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "media.db")
	sqlDB, err := dbpkg.Connect(dbPath)
	if err != nil {
		t.Fatalf("Failed to open DB: %v", err)
	}
	defer func() { _ = sqlDB.Close() }()

	_, err = sqlDB.Exec(`
		CREATE TABLE media (
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
			time_deleted INTEGER DEFAULT 0,
			is_shrinked INTEGER DEFAULT 0,
			width INTEGER DEFAULT 0,
			height INTEGER DEFAULT 0
		) STRICT;
		CREATE TABLE history (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			media_path TEXT NOT NULL,
			time_played INTEGER DEFAULT 0,
			FOREIGN KEY (media_path) REFERENCES media(path) ON DELETE CASCADE
		) STRICT;
	`)
	if err != nil {
		t.Fatalf("Failed to create schema: %v", err)
	}

	oldPath := filepath.Join(tempDir, "original.mp4")
	newPath := filepath.Join(tempDir, "compressed.mkv")
	if _, err := sqlDB.Exec(
		"INSERT INTO media (path, size, duration, width, height) VALUES (?, ?, ?, ?, ?)",
		oldPath, 1000, 12, 1920, 1080,
	); err != nil {
		t.Fatalf("Failed to insert media row: %v", err)
	}
	if _, err := sqlDB.Exec("INSERT INTO history (media_path, time_played) VALUES (?, ?)", oldPath, 25); err != nil {
		t.Fatalf("Failed to insert history row: %v", err)
	}

	engine := NewEngine(nil, nil, EngineConfig{}, []*sql.DB{sqlDB}, nil, nil)
	engine.updateMetadata(
		models.ShrinkMedia{Path: oldPath, Duration: 12, Width: 1920, Height: 1080, Category: "Video"},
		models.ProcessResult{Outputs: []models.ProcessOutputFile{{Path: newPath, Size: 600}}},
	)

	var (
		mediaCount   int
		historyCount int
	)
	if err := sqlDB.QueryRow("SELECT COUNT(*) FROM media WHERE path = ?", newPath).Scan(&mediaCount); err != nil {
		t.Fatalf("Failed to query media row: %v", err)
	}
	if err := sqlDB.QueryRow("SELECT COUNT(*) FROM history WHERE media_path = ?", newPath).Scan(&historyCount); err != nil {
		t.Fatalf("Failed to query history row: %v", err)
	}
	if mediaCount != 1 {
		t.Fatalf("Expected renamed media row at %s, got %d", newPath, mediaCount)
	}
	if historyCount != 1 {
		t.Fatalf("Expected history row to move to %s, got %d", newPath, historyCount)
	}
}
