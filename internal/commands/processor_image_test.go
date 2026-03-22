package commands

import (
	"testing"
)

func TestImageMagickErrors(t *testing.T) {
	// Case: unsupported
	logs := []string{"no decode delegate for this image format"}
	if !isImageMagickUnsupportedError(logs) {
		t.Errorf("expected unsupported")
	}

	// Case: file error
	logs = []string{"unable to open image"}
	if !isImageMagickFileError(logs) {
		t.Errorf("expected file error")
	}

	// Case: env error
	logs = []string{"killed"}
	if !isImageMagickEnvironmentError(logs) {
		t.Errorf("expected env error")
	}
}
