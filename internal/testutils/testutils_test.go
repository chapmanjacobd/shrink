package testutils

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCopyFile(t *testing.T) {
	tempDir := t.TempDir()
	src := filepath.Join(tempDir, "src.txt")
	dst := filepath.Join(tempDir, "dst.txt")
	os.WriteFile(src, []byte("data"), 0o644)

	copyFile(t, src, dst)

	data, _ := os.ReadFile(dst)
	if string(data) != "data" {
		t.Errorf("got %s", string(data))
	}
}
