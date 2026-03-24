//go:build !fts5

package db

// FTS5 is REQUIRED for shrink to function correctly.
//
// You are building WITHOUT FTS5 support, which will cause runtime failures.
// FTS5 is a SQLite extension for full-text search that shrink depends on.
//
// Please rebuild with the fts5 tag:
//   go build -tags fts5 ./...
//   go run -tags fts5 ./cmd/shrink
//   go install -tags fts5 github.com/chapmanjacobd/shrink/cmd/shrink@latest
//
// Or use the Makefile:
//   make build
//   make install
//
// For more information, see README.md

// FTS5 support is required. Build with: go build -tags fts5
// This variable is defined in fts5.go when the fts5 tag is present
var _ bool = _fts5BuildTagRequired
