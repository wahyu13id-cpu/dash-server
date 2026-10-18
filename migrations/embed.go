// Package migrations embeds the SQL migration files so the binary can set up
// its database without external files being present at runtime.
package migrations

import "embed"

// FS contains every .sql migration, applied in lexical filename order.
//
//go:embed *.sql
var FS embed.FS
