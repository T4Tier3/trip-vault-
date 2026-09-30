// Package migrations embeds the SQL schema migrations into the binary so a
// deployment is a single self-contained artefact.
//
// Keeping the .sql files at the repository root of the backend (rather than
// inside internal/) matches the documented project layout. Go's embed
// directive cannot traverse parent directories, so this thin package exists to
// own the embed on their behalf.
package migrations

import "embed"

// FS holds every migration file. Both halves of each migration
// (NNN_name.up.sql and NNN_name.down.sql) are embedded together.
//
//go:embed *.sql
var FS embed.FS
