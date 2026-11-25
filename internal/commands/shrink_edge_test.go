package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chapmanjacobd/shrink/internal/db"
	"github.com/chapmanjacobd/shrink/internal/models"
	"github.com/chapmanjacobd/shrink/internal/utils"
)

func TestEdgeCases(t *testing.T) {
	tempDir := t.TempDir()

	// Create test files
	corruptVid := filepath.Join(tempDir, "corrupt.avi")
	_ = os.WriteFile(corruptVid, []byte("not a video"), 0o644)

	validVid := filepath.Join(tempDir, "valid.avi")
	copyFile(t, "../testutils/testdata/tiny.avi", validVid)

	// Set timestamps to test preservation
	modTime := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	_ = os.Chtimes(validVid, modTime, modTime)

	moveDir := filepath.Join(tempDir, "moved")
	moveBrokenDir := filepath.Join(tempDir, "broken")

	args := []string{
		"--no-confirm",
		"--preset=7",
		"--crf=40",
		"--move=" + moveDir,
		"--move-broken=" + moveBrokenDir,
		"--delete-larger=true",
		"--verbose",
	}

	err := runShrinkCmdDir(tempDir, tempDir, args)
	if err != nil {
		t.Fatalf("command failed: %v", err)
	}

	// Check if corrupt file was moved to broken
	// GetMountPoint now works even after files are moved
	mountPoint, _ := utils.GetMountPoint(corruptVid)
	var expectedBrokenPath string
	if mountPoint == "" {
		expectedBrokenPath = filepath.Join(moveBrokenDir, filepath.Base(filepath.Dir(corruptVid)), filepath.Base(corruptVid))
	} else {
		relPath, _ := filepath.Rel(mountPoint, corruptVid)
		expectedBrokenPath = filepath.Join(moveBrokenDir, relPath)
	}
	if _, err := os.Stat(expectedBrokenPath); os.IsNotExist(err) {
		t.Errorf("expected corrupt file to be moved to %s", expectedBrokenPath)
	}

	// Check if valid file was processed and original deleted
	mountPointValid, _ := utils.GetMountPoint(validVid)
	var expectedMovedPath string
	if mountPointValid == "" {
		expectedMovedPath = filepath.Join(moveDir, filepath.Base(filepath.Dir(validVid)), "valid.av1.mkv")
	} else {
		relPathValid, _ := filepath.Rel(mountPointValid, validVid)
		expectedMovedPath = filepath.Join(moveDir, strings.TrimSuffix(relPathValid, filepath.Ext(validVid))+".av1.mkv")
	}
	info, err := os.Stat(expectedMovedPath)
	if os.IsNotExist(err) {
		t.Errorf("expected valid file to be transcoded and moved to %s", expectedMovedPath)
	} else if err == nil {
		// Timestamp preservation
		if !info.ModTime().Equal(modTime) {
			t.Errorf("expected timestamp %v, got %v", modTime, info.ModTime())
		}
	}
}

func TestSimulateDoesNotMoveSkippedFiles(t *testing.T) {
	tempDir := t.TempDir()
	moveDir := filepath.Join(tempDir, "moved")
	srcDir := filepath.Join(tempDir, "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	original := filepath.Join(srcDir, "already.opus")
	if err := os.WriteFile(original, []byte("already optimized"), 0o644); err != nil {
		t.Fatal(err)
	}

	args := []string{"--simulate", "--move", moveDir}
	if err := runShrinkCmdDir(srcDir, tempDir, args); err != nil {
		t.Fatalf("command failed: %v", err)
	}

	if _, err := os.Stat(original); os.IsNotExist(err) {
		t.Errorf("simulation moved the original file")
	}
	if _, err := os.Stat(moveDir); err == nil {
		entries, _ := os.ReadDir(moveDir)
		if len(entries) > 0 {
			t.Errorf("simulation created destination files: %v", entries)
		}
	}
}

func TestFilterByToolsDoesNotMoveFiles(t *testing.T) {
	tempDir := t.TempDir()
	original := filepath.Join(tempDir, "already.opus")
	if err := os.WriteFile(original, []byte("already optimized"), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := &ShrinkCmd{
		unknownExtensions: make(map[string]UnknownExt),
		skippedByTool:     make(map[string]int64),
	}
	cmd.Move = filepath.Join(tempDir, "moved")

	media := []models.ShrinkMedia{{Path: original, Ext: ".opus"}}
	filtered, skipped := cmd.filterByTools(media, nil, InstalledTools{})
	if len(filtered) != 0 {
		t.Fatalf("expected optimized file to be filtered, got %d", len(filtered))
	}
	if len(skipped) != 1 || skipped[0].Path != original {
		t.Fatalf("expected optimized file to be queued for moving, got %+v", skipped)
	}
	if _, err := os.Stat(original); err != nil {
		t.Errorf("filterByTools moved the file before confirmation: %v", err)
	}
}

func TestInvalidConfigValuesReturnErrors(t *testing.T) {
	tempDir := t.TempDir()
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{name: "zero video threads", args: []string{"--video-threads=0"}, wantErr: "video-threads"},
		{name: "zero audio threads", args: []string{"--audio-threads=0"}, wantErr: "audio-threads"},
		{name: "invalid memory limit", args: []string{"--memory-limit=banana"}, wantErr: "memory-limit"},
		{name: "invalid savings", args: []string{"--min-savings-video=banana"}, wantErr: "min-savings-video"},
		{name: "invalid bitrate", args: []string{"--target-audio-bitrate=fast"}, wantErr: "target-audio-bitrate"},
		{name: "invalid preset", args: []string{"--preset=99"}, wantErr: "preset"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := runShrinkCmdDir(tempDir, tempDir, tt.args)
			if err == nil {
				t.Fatalf("expected error for %v", tt.args)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error %q does not mention %q", err, tt.wantErr)
			}
		})
	}
}

func TestContinueFrom(t *testing.T) {
	tempDir := t.TempDir()

	file1 := filepath.Join(tempDir, "a.avi")
	file2 := filepath.Join(tempDir, "b.avi")
	file3 := filepath.Join(tempDir, "c.avi")

	_ = os.WriteFile(file1, []byte("dummy1"), 0o644)
	_ = os.WriteFile(file2, []byte("dummy2"), 0o644)
	_ = os.WriteFile(file3, []byte("dummy3"), 0o644)

	args := []string{
		"--no-confirm",
		"--continue-from=" + file2,
		"--simulate",
	}

	err := runShrinkCmdDir(tempDir, tempDir, args)
	if err != nil {
		t.Fatalf("command failed: %v", err)
	}
}

func TestShrinkImage(t *testing.T) {
	tempDir := t.TempDir()
	img := filepath.Join(tempDir, "test.bmp")
	copyFile(t, "../testutils/testdata/tiny.bmp", img)

	args := []string{"--no-confirm", "--image-only"}
	err := runShrinkCmdDir(tempDir, tempDir, args)
	if err != nil {
		t.Fatalf("command failed: %v", err)
	}

	output := filepath.Join(tempDir, "test.avif")
	if _, err := os.Stat(output); os.IsNotExist(err) {
		t.Errorf("expected output image missing")
	}
}

func TestMissingTools(t *testing.T) {
	// Set PATH to empty to simulate missing tools
	oldPath := os.Getenv("PATH")
	_ = os.Setenv("PATH", "")
	defer func() { _ = os.Setenv("PATH", oldPath) }()

	tempDir := t.TempDir()
	img := filepath.Join(tempDir, "test.bmp")
	_ = os.WriteFile(img, []byte("dummy"), 0o644)

	args := []string{"--no-confirm"}
	err := runShrinkCmdDir(tempDir, tempDir, args)
	if err != nil {
		t.Fatalf("command failed: %v", err)
	}
}

func TestFilterScenarios(t *testing.T) {
	tempDir := t.TempDir()
	_ = os.WriteFile(filepath.Join(tempDir, "vid.mp4"), []byte("dummy"), 0o644)
	_ = os.WriteFile(filepath.Join(tempDir, "aud.mp3"), []byte("dummy"), 0o644)

	// Test --video-only
	args := []string{"--no-confirm", "--video-only", "--simulate"}
	_ = runShrinkCmdDir(tempDir, tempDir, args)

	// Test --audio-only
	args = []string{"--no-confirm", "--audio-only", "--simulate"}
	_ = runShrinkCmdDir(tempDir, tempDir, args)

	// Test --search
	args = []string{"--no-confirm", "--search", "vid", "--simulate"}
	_ = runShrinkCmdDir(tempDir, tempDir, args)
}

func TestConfigProfiles(t *testing.T) {
	// Test that profiles correctly set preset/crf
	tests := []struct {
		profile string
		wantP   string
		wantC   string
	}{
		{"quality", "4", "32"},
		{"speed", "10", "45"},
		{"balance", "7", "40"},
	}

	for _, tt := range tests {
		cfg := &Config{
			CoreFlags: CoreFlags{Profile: tt.profile},
		}
		cfg.ApplyProfile()
		if cfg.Preset != tt.wantP || cfg.CRF != tt.wantC {
			t.Errorf("Profile %s: got %s/%s, want %s/%s", tt.profile, cfg.Preset, cfg.CRF, tt.wantP, tt.wantC)
		}
	}
}

func TestScanDirectory(t *testing.T) {
	tempDir := t.TempDir()
	_ = os.Mkdir(filepath.Join(tempDir, ".git"), 0o755)
	_ = os.WriteFile(filepath.Join(tempDir, ".git", "config"), []byte("data"), 0o644)
	_ = os.WriteFile(filepath.Join(tempDir, "vid.mp4"), []byte("data"), 0o644)

	// Should skip .git
	cmd := &ShrinkCmd{
		unknownExtensions: make(map[string]UnknownExt),
	}
	media, _ := cmd.scanDirectory(tempDir)
	for _, m := range media {
		if strings.Contains(m.Path, ".git") {
			t.Errorf("scanned .git directory")
		}
	}
}

func TestFileDisappears(t *testing.T) {
	tempDir := t.TempDir()

	// Create a file and then delete it right before running?
	// No, just provide a non-existent file in the DB.
	dbPath := filepath.Join(tempDir, "test.db")
	db, _ := db.Connect(dbPath)
	_, _ = db.Exec(`CREATE TABLE media (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		path TEXT UNIQUE NOT NULL,
		size INTEGER,
		media_type TEXT,
		time_deleted INTEGER DEFAULT 0,
		is_shrinked INTEGER DEFAULT 0,
		width INTEGER DEFAULT 0,
		height INTEGER DEFAULT 0
	)`)
	_, _ = db.Exec(`INSERT INTO media (path, size, media_type) VALUES (?, ?, ?)`,
		filepath.Join(tempDir, "ghost.avi"), 1000, "video")
	_ = db.Close()

	args := []string{"--no-confirm"}
	_ = runShrinkCmd(dbPath, tempDir, args)
}
