// Package migrations holds the schema that ships with the backend binary.
package migrations

import "embed"

// Files contains the SQL migrations in the release.
//
//go:embed *.sql
var Files embed.FS
