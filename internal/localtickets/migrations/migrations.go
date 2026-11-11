// Package migrations is the demo's local tickets database's schema history,
// as goose migrations in NNNNN_name.sql files. Never edit a migration once it
// has merged; add a new one. See CONTRIBUTING.md.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
