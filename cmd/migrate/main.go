// Command migrate runs the Control plane store's migrations against a local
// SQLite file, through the same goose Provider store.Open uses, so it sees
// the Go migrations too. For development only: the Control plane migrates
// its own database when it starts.
//
//	migrate [-db FILE] status                each migration, applied or pending
//	migrate [-db FILE] up                    apply every pending migration
//	migrate [-db FILE] down                  undo the last applied migration
//	migrate [-db FILE] redo                  undo the last applied migration and apply it again
//	migrate [-dir DIR] create NAME [sql|go]  write the next numbered migration: SQL, or a Go data migration
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"text/template"

	"github.com/pressly/goose/v3"

	"github.com/itsankoff/incident-copilot/internal/store"
)

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "migrate:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("migrate", flag.ContinueOnError)
	path := fs.String("db", "build/dev/state.db", "the SQLite file to migrate")
	dir := fs.String("dir", "internal/store/migrations", "where create writes the new migration")
	fs.Usage = func() {
		_, _ = fmt.Fprintln(fs.Output(), "usage: migrate [flags] status | up | down | redo | create NAME [sql|go]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		fs.Usage()
		return errors.New("no command")
	}
	if fs.Arg(0) == "create" {
		return create(*dir, fs.Args()[1:])
	}

	if err := os.MkdirAll(filepath.Dir(*path), 0o755); err != nil {
		return err
	}
	db, err := store.OpenDB(*path)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	p, err := store.NewMigrator(db, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if err != nil {
		return err
	}
	return store.WithForeignKeysOff(ctx, db, func() error {
		switch fs.Arg(0) {
		case "status":
			ss, err := p.Status(ctx)
			for _, s := range ss {
				if _, werr := fmt.Fprintf(out, "%5d  %-8s %s\n", s.Source.Version, s.State, filepath.Base(s.Source.Path)); werr != nil {
					return werr
				}
			}
			return err
		case "up":
			_, err := p.Up(ctx)
			return err
		case "down":
			_, err := p.Down(ctx)
			return err
		case "redo":
			if _, err := p.Down(ctx); err != nil {
				return err
			}
			_, err := p.UpByOne(ctx)
			return err
		}
		return fmt.Errorf("unknown command %q", fs.Arg(0))
	})
}

// create writes the next sequentially numbered migration into dir.
func create(dir string, args []string) error {
	kind := "sql"
	if len(args) == 2 {
		kind = args[1]
	}
	tmpl := map[string]*template.Template{"sql": sqlTemplate, "go": goTemplate}[kind]
	if len(args) == 0 || len(args) > 2 || tmpl == nil {
		return errors.New("usage: create NAME [sql|go]")
	}
	goose.SetSequential(true)
	return goose.CreateWithTemplate(nil, dir, tmpl, args[0], kind)
}

var sqlTemplate = template.Must(template.New("sql").Parse(`-- What this migration changes, and why. Keep the previous release working on
-- the new schema (expand/contract); see CONTRIBUTING.md.
-- +goose Up


-- +goose Down

`))

var goTemplate = template.Must(template.New("go").Parse(`package migrations

import (
	"context"
	"database/sql"
)

// Data only: schema changes (DDL) belong in a .sql migration. Read rows into
// local types, never the app's, so that this keeps working as they change.
func init() { register(up{{.CamelName}}, down{{.CamelName}}) }

func up{{.CamelName}}(ctx context.Context, tx *sql.Tx) error {
	return nil
}

func down{{.CamelName}}(ctx context.Context, tx *sql.Tx) error {
	return nil
}
`))
