package commands

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEdgeCases(t *testing.T) {
	tempDir := t.TempDir()

	// Create test files
	corruptVid := filepath.Join(tempDir, "corrupt.avi")
	os.WriteFile(corruptVid, []byte("not a video"), 0o644)

	validVid := filepath.Join(tempDir, "valid.avi")
	copyFile(t, "../testutils/testdata/tiny.avi", validVid)

	// Set timestamps to test preservation
	modTime := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	os.Chtimes(validVid, modTime, modTime)

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
	brokenPath := filepath.Join(moveBrokenDir, filepath.Base(tempDir), "corrupt.avi")
	if _, err := os.Stat(brokenPath); os.IsNotExist(err) {
		t.Errorf("expected corrupt file to be moved to %s", brokenPath)
	}

	// Check if valid file was processed and original deleted
	movedPath := filepath.Join(moveDir, "valid.av1.mkv")
	info, err := os.Stat(movedPath)
	if os.IsNotExist(err) {
		t.Errorf("expected valid file to be transcoded and moved to %s", movedPath)
	} else if err == nil {
		// Timestamp preservation
		if !info.ModTime().Equal(modTime) {
			t.Errorf("expected timestamp %v, got %v", modTime, info.ModTime())
		}
	}
}

func TestContinueFrom(t *testing.T) {
	tempDir := t.TempDir()

	file1 := filepath.Join(tempDir, "a.avi")
	file2 := filepath.Join(tempDir, "b.avi")
	file3 := filepath.Join(tempDir, "c.avi")

	os.WriteFile(file1, []byte("dummy1"), 0o644)
	os.WriteFile(file2, []byte("dummy2"), 0o644)
	os.WriteFile(file3, []byte("dummy3"), 0o644)

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
	os.Setenv("PATH", "")
	defer os.Setenv("PATH", oldPath)

	tempDir := t.TempDir()
	img := filepath.Join(tempDir, "test.bmp")
	os.WriteFile(img, []byte("dummy"), 0o644)

	args := []string{"--no-confirm"}
	err := runShrinkCmdDir(tempDir, tempDir, args)
	if err != nil {
		t.Fatalf("command failed: %v", err)
	}
}

func TestFilterScenarios(t *testing.T) {
	tempDir := t.TempDir()
	os.WriteFile(filepath.Join(tempDir, "vid.mp4"), []byte("dummy"), 0o644)
	os.WriteFile(filepath.Join(tempDir, "aud.mp3"), []byte("dummy"), 0o644)

	// Test --video-only
	args := []string{"--no-confirm", "--video-only", "--simulate"}
	runShrinkCmdDir(tempDir, tempDir, args)

	// Test --audio-only
	args = []string{"--no-confirm", "--audio-only", "--simulate"}
	runShrinkCmdDir(tempDir, tempDir, args)

	// Test --search
	args = []string{"--no-confirm", "--search", "vid", "--simulate"}
	runShrinkCmdDir(tempDir, tempDir, args)
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
	os.Mkdir(filepath.Join(tempDir, ".git"), 0o755)
	os.WriteFile(filepath.Join(tempDir, ".git", "config"), []byte("data"), 0o644)
	os.WriteFile(filepath.Join(tempDir, "vid.mp4"), []byte("data"), 0o644)

	// Should skip .git
	cmd := &ShrinkCmd{
		unknownExtensions: make(map[string]int64),
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
	db, _ := sql.Open("sqlite3", dbPath)
	db.Exec(`CREATE TABLE media (path TEXT PRIMARY KEY, size INTEGER, media_type TEXT, time_deleted INTEGER DEFAULT 0)`)
	db.Exec(`INSERT INTO media (path, size, media_type) VALUES (?, ?, ?)`,
		filepath.Join(tempDir, "ghost.avi"), 1000, "video")
	db.Close()

	args := []string{"--no-confirm"}
	runShrinkCmd(dbPath, tempDir, args)
}
