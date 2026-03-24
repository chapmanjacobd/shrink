//go:build fts5

package db

// _fts5BuildTagRequired is defined when fts5 tag is present.
// When building without the fts5 tag, build_tags.go references this
// undefined identifier, causing a compile error.
//
//lint:ignore U1000 This variable is intentionally used via build tag magic
var _fts5BuildTagRequired bool
