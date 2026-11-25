package commands

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/chapmanjacobd/shrink/internal/models"
)

func TestRunOCRStub(t *testing.T) {
	// Stub
}

func TestEbookVersion(t *testing.T) {
	p := NewTextProcessor()
	major, minor, err := p.getCalibreVersion()
	// This might fail if calibre not installed, but let's just ensure it doesn't panic
	_ = major
	_ = minor
	_ = err
}

func TestParseCalibreVersion(t *testing.T) {
	tests := []struct {
		name                string
		output              string
		major, minor, patch int
	}{
		{
			name:   "standard output",
			output: "ebook-convert (calibre 9.19.0)\nCreated by: Kovid Goyal",
			major:  9, minor: 19, patch: 0,
		},
		{
			name:   "older version",
			output: "ebook-convert (calibre 7.19.0)",
			major:  7, minor: 19, patch: 0,
		},
		{
			name:   "unparseable",
			output: "something else",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			major, minor, patch := parseCalibreVersion(tt.output)
			if major != tt.major || minor != tt.minor || patch != tt.patch {
				t.Errorf("parseCalibreVersion() = %d.%d.%d, want %d.%d.%d",
					major, minor, patch, tt.major, tt.minor, tt.patch)
			}
		})
	}
}

func TestUpdateManifestUsesFullHref(t *testing.T) {
	dir := t.TempDir()
	manifest := `<package>
  <manifest>
    <item id="img1" href="images/image1.bmp" media-type="image/bmp"/>
    <item id="img11" href="images/image11.bmp" media-type="image/bmp"/>
  </manifest>
</package>`
	if err := os.WriteFile(filepath.Join(dir, "content.opf"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}

	p := NewTextProcessor()
	defer p.Cleanup()
	p.updateManifest(dir, map[string]string{"image1.bmp": "image1.avif"})

	data, err := os.ReadFile(filepath.Join(dir, "content.opf"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, `href="images/image1.avif"`) {
		t.Errorf("manifest href was not updated: %s", text)
	}
	if !strings.Contains(text, `media-type="image/avif"`) {
		t.Errorf("manifest media type was not updated: %s", text)
	}
	if !strings.Contains(text, `href="images/image11.bmp"`) {
		t.Errorf("unrelated image was changed: %s", text)
	}
}

func TestGetImageFilesFromManifestRejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	manifest := `<package><manifest>
    <item href="../outside.jpg" media-type="image/jpeg"/>
</manifest></package>`
	if err := os.WriteFile(filepath.Join(dir, "content.opf"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}

	p := NewTextProcessor()
	defer p.Cleanup()
	if images := p.getImageFilesFromManifest(dir); len(images) != 0 {
		t.Errorf("expected traversal href to be rejected, got %v", images)
	}
}

func TestReplaceImageReferenceRespectsNameBoundaries(t *testing.T) {
	text := `<img src="images/1.jpg"/><img src="images/11.jpg"/>`
	got := replaceImageReference(text, "1.jpg", "1.avif")
	if !strings.Contains(got, `src="images/1.avif"`) {
		t.Errorf("expected image reference to be replaced: %s", got)
	}
	if !strings.Contains(got, `src="images/11.jpg"`) {
		t.Errorf("expected longer filename to be preserved: %s", got)
	}
}

func TestProcessTextRunsOCROnPDF(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script mocks are not portable to Windows")
	}

	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dir, "ocr-called")

	ocrScript := fmt.Sprintf(`#!/bin/sh
for arg in "$@"; do prev=$last; last=$arg; done
touch %q
cp "$prev" "$last"
`, marker)
	if err := os.WriteFile(filepath.Join(binDir, "ocrmypdf"), []byte(ocrScript), 0o755); err != nil {
		t.Fatal(err)
	}

	convertScript := `#!/bin/sh
case "$1" in
  --version) echo "ebook-convert (calibre 7.19.0)"; exit 0 ;;
  *content.opf) echo "epub" > "$2"; exit 0 ;;
esac
mkdir -p "$2"
printf '<package/>' > "$2/content.opf"
`
	if err := os.WriteFile(filepath.Join(binDir, "ebook-convert"), []byte(convertScript), 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	pdfPath := filepath.Join(dir, "book.pdf")
	if err := os.WriteFile(pdfPath, []byte("pdf"), 0o644); err != nil {
		t.Fatal(err)
	}

	p := NewTextProcessor()
	defer p.Cleanup()
	cfg := &models.ProcessorConfig{Text: models.TextConfig{ForceOCR: true}}
	result := p.Process(context.Background(), &models.ShrinkMedia{Path: pdfPath, Ext: ".pdf", Size: 3}, cfg, nil)

	if result.Error != nil {
		t.Fatalf("process failed: %v", result.Error)
	}
	if !result.Success {
		t.Fatal("expected successful text processing")
	}
	if result.SourcePath != pdfPath {
		t.Errorf("SourcePath = %q, want original path %q", result.SourcePath, pdfPath)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Error("OCR was not invoked for PDF")
	}
	if _, err := os.Stat(pdfPath); err != nil {
		t.Errorf("processor modified the original PDF: %v", err)
	}
	ocrPath := filepath.Join(dir, "book.ocr.pdf")
	if _, err := os.Stat(ocrPath); err == nil {
		t.Errorf("OCR intermediate file was not cleaned up: %s", ocrPath)
	}
}
