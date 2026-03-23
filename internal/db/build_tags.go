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

// FtsEnabled is false when fts5 tag is not used (but this build is not supported)
const FtsEnabled = false

// _fts5BuildTagRequired triggers a compile error when fts5 tag is missing.
// The undefined identifier below causes: "undefined: _fts5BuildTagRequired"
// See: https://github.com/chapmanjacobd/shrink#installation
var _ = _fts5BuildTagRequired
