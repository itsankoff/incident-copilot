// Command migrate runs a Control plane database's migrations against a local
// SQLite file, through the same runner the Control plane uses when it opens
// the database, so it sees the Go migrations too. For development only: the
// Control plane migrates its own databases when it starts.
//
//	migrate [flags] status                each migration, applied or pending
//	migrate [flags] up                    apply every pending migration
//	migrate [flags] down                  undo the last applied migration
//	migrate [flags] redo                  undo the last applied migration and apply it again
//	migrate [flags] create NAME [sql|go]  write the next numbered migration: SQL, or a Go data migration
//
// -schema picks the database: state (the store, the default) or tickets (the
// demo's local tickets).
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

	"github.com/itsankoff/incident-copilot/internal/localtickets"
	"github.com/itsankoff/incident-copilot/internal/migrate"
	"github.com/itsankoff/incident-copilot/internal/store"
)

// schemas are the Control plane's databases: their migrations, and the
// directory that holds them.
var schemas = map[string]struct {
	migrations   migrate.Migrations
	dir          string
	goMigrations bool // whether the directory's package has register, for Go migrations
}{
	"state":   {store.Migrations, "internal/store/migrations", true},
	"tickets": {localtickets.Migrations, "internal/localtickets/migrations", false},
}

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "migrate:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("migrate", flag.ContinueOnError)
	name := fs.String("schema", "state", "the database: state or tickets")
	path := fs.String("db", "", "the SQLite file to migrate (default build/dev/<schema>.db)")
	dir := fs.String("dir", "", "where create writes the new migration (default the schema's migrations directory)")
	fs.Usage = func() {
		_, _ = fmt.Fprintln(fs.Output(), "usage: migrate [flags] status | up | down | redo | create NAME [sql|go]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	schema, ok := schemas[*name]
	if !ok {
		return fmt.Errorf("unknown schema %q: want state or tickets", *name)
	}
	if fs.NArg() == 0 {
		fs.Usage()
		return errors.New("no command")
	}
	if fs.Arg(0) == "create" {
		if *dir == "" {
			*dir = schema.dir
		}
		return create(*dir, schema.goMigrations, fs.Args()[1:])
	}

	if *path == "" {
		*path = filepath.Join("build", "dev", *name+".db")
	}
	if err := os.MkdirAll(filepath.Dir(*path), 0o755); err != nil {
		return err
	}
	db, err := migrate.OpenDB(*path)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	m, err := migrate.New(db, schema.migrations, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if err != nil {
		return err
	}
	return m.Run(ctx, func(p *goose.Provider) error {
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
func create(dir string, goMigrations bool, args []string) error {
	kind := "sql"
	if len(args) == 2 {
		kind = args[1]
	}
	tmpl := map[string]*template.Template{"sql": sqlTemplate, "go": goTemplate}[kind]
	if len(args) == 0 || len(args) > 2 || tmpl == nil {
		return errors.New("usage: create NAME [sql|go]")
	}
	if kind == "go" && !goMigrations {
		return fmt.Errorf("%s has no Go migrations yet: add a register function like internal/store/migrations' first", dir)
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
