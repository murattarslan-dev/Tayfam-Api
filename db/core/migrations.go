// Package coredb holds core's SQL migrations, embedded in the core binary
// (ADR-0006). Only core may import it (CLAUDE.md, rule 2).
package coredb

import (
	"embed"
	"io/fs"
)

//go:embed migrations/*.sql
var files embed.FS

// Migrations returns the migration files rooted at the migrations directory.
func Migrations() fs.FS {
	sub, err := fs.Sub(files, "migrations")
	if err != nil {
		panic(err) // the embedded path is fixed at compile time
	}
	return sub
}
