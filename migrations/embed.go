package migrations

import "embed"

// FS contains all database migrations.
//
//go:embed *.sql
var FS embed.FS
