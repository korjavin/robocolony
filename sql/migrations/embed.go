// Package migrations carries the goose SQL migrations into the binary, so a
// deployed container needs nothing on disk but the database file.
//
// The embed lives here rather than in internal/db because //go:embed patterns
// cannot reach outside the directory holding the .go file.
package migrations

import "embed"

// FS holds every migration, at the root of the filesystem.
//
//go:embed *.sql
var FS embed.FS
