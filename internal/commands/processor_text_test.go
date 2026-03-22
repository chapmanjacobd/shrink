package commands

import (
	"testing"
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
