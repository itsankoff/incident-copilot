package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"path/filepath"

	"github.com/pressly/goose/v3"

	"github.com/itsankoff/incident-copilot/internal/store/migrations"
)

// OpenDB opens the SQLite file at path the way the store uses it: foreign
// keys on, a busy timeout, and one connection, which WithForeignKeysOff
// needs. Open and cmd/migrate share it.
func OpenDB(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

// NewMigrator returns the goose Provider over the store's migrations, for a
// db from OpenDB. Run its commands inside WithForeignKeysOff. goose logs
// every statement it runs to log; a nil log logs nothing.
func NewMigrator(db *sql.DB, log *slog.Logger) (*goose.Provider, error) {
	return newMigrator(db, migrations.FS, migrations.Go, log)
}

func newMigrator(db *sql.DB, fsys fs.FS, gos []*goose.Migration, log *slog.Logger) (*goose.Provider, error) {
	opts := []goose.ProviderOption{goose.WithDisableGlobalRegistry(true), goose.WithGoMigrations(gos...)}
	if log != nil {
		opts = append(opts, goose.WithSlog(log), goose.WithVerbose(true))
	}
	return goose.NewProvider(goose.DialectSQLite3, db, fsys, opts...)
}

// WithForeignKeysOff runs fn with foreign keys off on db's one connection,
// then fails if fn left a foreign key violation. SQLite ignores the pragma
// inside a transaction, and goose runs each migration in one, so without
// this a rebuild of a parent table (incidents) fails. Atlas's SQLite runner
// does the same around each file.
func WithForeignKeysOff(ctx context.Context, db *sql.DB, fn func() error) error {
	if db.Stats().MaxOpenConnections != 1 {
		return errors.New("migrations need a single-connection *sql.DB (OpenDB)")
	}
	if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		return err
	}
	err := fn()
	if err == nil {
		var n int
		if err = db.QueryRowContext(ctx, `SELECT count(*) FROM pragma_foreign_key_check`).Scan(&n); err == nil && n > 0 {
			err = fmt.Errorf("migrations left %d foreign key violations (PRAGMA foreign_key_check lists them)", n)
		}
	}
	_, onErr := db.ExecContext(ctx, `PRAGMA foreign_keys = ON`)
	return errors.Join(err, onErr)
}

// migrate applies p's pending migrations, each in its own transaction with
// its version record, and logs each one. It warns when the database has a
// version p does not know: a newer binary migrated it, and this one runs on
// that schema because migrations keep the previous release working.
func migrate(ctx context.Context, db *sql.DB, p *goose.Provider, log *slog.Logger) error {
	var applied []*goose.MigrationResult
	if err := WithForeignKeysOff(ctx, db, func() (err error) { applied, err = p.Up(ctx); return err }); err != nil {
		return err
	}
	for _, r := range applied {
		log.Info("store migration applied", "migration", r.Source.Version, "source", filepath.Base(r.Source.Path), "duration", r.Duration.String())
	}
	current, err := p.GetDBVersion(ctx)
	if err != nil {
		return err
	}
	sources := p.ListSources()
	if known := sources[len(sources)-1].Version; current > known {
		log.Warn("store schema is newer than this binary", "db_version", current, "binary_version", known)
	}
	return nil
}
