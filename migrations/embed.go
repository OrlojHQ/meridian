// Package migrations embeds Meridian's ordered SQLite schema migrations.
package migrations

import "embed"

// Files contains all versioned SQL migrations.
//
//go:embed *.sql
var Files embed.FS
