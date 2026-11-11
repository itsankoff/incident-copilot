// Package migrations is the Control plane store's schema history, as goose
// migrations: NNNNN_name.sql files hold every schema change (DDL), and
// NNNNN_name.go files change data only. Never edit a migration once it has
// merged; add a new one. See CONTRIBUTING.md.
package migrations

import (
	"context"
	"database/sql"
	"embed"
	"path/filepath"
	"runtime"

	"github.com/pressly/goose/v3"
)

//go:embed *.sql
var FS embed.FS

// Go lists the Go migrations, each registered by its own NNNNN_name.go file.
var Go []*goose.Migration

// register adds the calling file's Go migration, versioned by the number in
// its file name. Both directions run in goose's transaction (the store has
// one connection, and goose refuses a Go migration without a transaction
// there). A nil down does nothing.
func register(up, down func(context.Context, *sql.Tx) error) {
	_, file, _, _ := runtime.Caller(1)
	version, err := goose.NumericComponent(file)
	if err != nil {
		panic(err)
	}
	m := goose.NewGoMigration(version, &goose.GoFunc{RunTx: up}, &goose.GoFunc{RunTx: down})
	m.Source = filepath.Base(file)
	Go = append(Go, m)
}
