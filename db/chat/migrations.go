// Package chatdb holds chat's SQL migrations, embedded in the chat binary
// (ADR-0006). Only chat may import it (CLAUDE.md, rule 2).
package chatdb

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
