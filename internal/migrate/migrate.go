// Package migrate runs a SQLite database's goose migrations. The Control
// plane keeps two databases this way: its state (internal/store) and the
// demo's local tickets (internal/localtickets), each with its own migrations
// directory. See CONTRIBUTING.md for how to add a migration.
package migrate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"path/filepath"

	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/database"
	_ "modernc.org/sqlite"
)

// Migrations is one database's schema history: its NNNNN_name.sql files and
// its Go migrations, each registered by its own NNNNN_name.go file.
type Migrations struct {
	FS fs.FS
	Go []*goose.Migration
}

// OpenDB opens the SQLite file at path the way the Control plane uses its
// databases: foreign keys on, a busy timeout, and one connection, which
// Migrator needs.
func OpenDB(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

// Migrator runs one database's migrations.
type Migrator struct {
	db *sql.DB
	p  *goose.Provider
}

// New returns a Migrator of ms over db, a database from OpenDB. goose logs
// every statement it runs to log; a nil log logs nothing.
func New(db *sql.DB, ms Migrations, log *slog.Logger) (*Migrator, error) {
	if db.Stats().MaxOpenConnections != 1 {
		return nil, errors.New("migrations need a single-connection *sql.DB (OpenDB)")
	}
	versions, err := database.NewStore(database.DialectSQLite3, goose.DefaultTablename)
	if err != nil {
		return nil, err
	}
	opts := []goose.ProviderOption{
		goose.WithStore(foreignKeyChecked{versions}),
		goose.WithDisableGlobalRegistry(true),
		goose.WithGoMigrations(ms.Go...),
	}
	if log != nil {
		opts = append(opts, goose.WithSlog(log), goose.WithVerbose(true))
	}
	p, err := goose.NewProvider(goose.DialectCustom, db, ms.FS, opts...)
	if err != nil {
		return nil, err
	}
	return &Migrator{db: db, p: p}, nil
}

// Run runs fn, any goose commands, with foreign keys off on the database's
// one connection, and turns them back on after. SQLite ignores the pragma
// inside a transaction, and goose runs each migration in one, so without
// this a migration that rebuilds a parent table fails. Each migration is
// still checked for violations (foreignKeyChecked).
func (m *Migrator) Run(ctx context.Context, fn func(*goose.Provider) error) error {
	if _, err := m.db.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		return err
	}
	err := fn(m.p)
	_, onErr := m.db.ExecContext(ctx, `PRAGMA foreign_keys = ON`)
	return errors.Join(err, onErr)
}

// Apply applies ms's pending migrations to db, as the Control plane does
// when it opens a database, and logs each one. Each migration commits whole
// with its version record, or not at all. Apply warns when the database has
// a version ms does not know: a newer binary migrated it, and this one runs
// on that schema because migrations keep the previous release working.
func Apply(ctx context.Context, db *sql.DB, ms Migrations, log *slog.Logger) error {
	m, err := New(db, ms, nil)
	if err != nil {
		return err
	}
	var applied []*goose.MigrationResult
	if err := m.Run(ctx, func(p *goose.Provider) (err error) { applied, err = p.Up(ctx); return err }); err != nil {
		return err
	}
	for _, r := range applied {
		log.Info("migration applied", "migration", r.Source.Version, "source", filepath.Base(r.Source.Path), "duration", r.Duration.String())
	}
	current, err := m.p.GetDBVersion(ctx)
	if err != nil {
		return err
	}
	sources := m.p.ListSources()
	if known := sources[len(sources)-1].Version; current > known {
		log.Warn("database schema is newer than this binary", "db_version", current, "binary_version", known)
	}
	return nil
}

// foreignKeyChecked is goose's SQLite version table, which also fails a
// migration that leaves a foreign key violation. goose records a migration's
// version inside the migration's transaction, so the failure rolls the
// migration back whole. Atlas's SQLite runner checks the same way after each
// file.
type foreignKeyChecked struct{ database.Store }

func (s foreignKeyChecked) Insert(ctx context.Context, db database.DBTxConn, req database.InsertRequest) error {
	if err := checkForeignKeys(ctx, db); err != nil {
		return err
	}
	return s.Store.Insert(ctx, db, req)
}

func (s foreignKeyChecked) Delete(ctx context.Context, db database.DBTxConn, version int64) error {
	if err := checkForeignKeys(ctx, db); err != nil {
		return fmt.Errorf("migration %d down: %w", version, err)
	}
	return s.Store.Delete(ctx, db, version)
}

func checkForeignKeys(ctx context.Context, db database.DBTxConn) error {
	var n int
	var first sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT count(*), min("table" || ' row ' || rowid || ' references ' || parent)
		FROM pragma_foreign_key_check`).Scan(&n, &first); err != nil {
		return err
	}
	if n > 0 {
		return fmt.Errorf("%d foreign key violations (%s, PRAGMA foreign_key_check lists them all)", n, first.String)
	}
	return nil
}
