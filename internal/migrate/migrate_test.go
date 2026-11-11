package migrate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/pressly/goose/v3"
)

var quiet = slog.New(slog.DiscardHandler)

// parents is a two-table history: child references parent.
var parents = fstest.MapFS{
	"00001_parents.sql": {Data: []byte(`-- +goose Up
CREATE TABLE parent (id INTEGER PRIMARY KEY, name TEXT NOT NULL);
CREATE TABLE child (id INTEGER PRIMARY KEY, parent_id INTEGER NOT NULL REFERENCES parent(id));
INSERT INTO parent VALUES (1, 'p');
INSERT INTO child VALUES (1, 1);
-- +goose Down
DROP TABLE child;
DROP TABLE parent;
`)},
}

// plus returns parents with one more migration.
func plus(name, text string) fstest.MapFS {
	fsys := fstest.MapFS{name: {Data: []byte(text)}}
	for k, v := range parents {
		fsys[k] = v
	}
	return fsys
}

func open(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := OpenDB(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func version(t *testing.T, db *sql.DB) int64 {
	t.Helper()
	var v int64
	if err := db.QueryRow(`SELECT max(version_id) FROM goose_db_version`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func count(t *testing.T, db *sql.DB, q string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(q).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// A migration that fails rolls back whole: no partial schema, and the
// version stays where it was.
func TestApplyRollsBackAFailingMigrationWhole(t *testing.T) {
	db := open(t, filepath.Join(t.TempDir(), "x.db"))
	ctx := context.Background()
	if err := Apply(ctx, db, Migrations{FS: parents}, quiet); err != nil {
		t.Fatal(err)
	}
	err := Apply(ctx, db, Migrations{FS: plus("00002_broken.sql", `-- +goose Up
CREATE TABLE later (x TEXT);
ALTER TABLE parent ADD COLUMN later TEXT NOT NULL DEFAULT '';
INSERT INTO nope VALUES (1);
-- +goose Down
SELECT 1;
`)}, quiet)
	if err == nil || !strings.Contains(err.Error(), "no such table: nope") {
		t.Fatalf("Apply: %v, want the migration's error", err)
	}
	if v := version(t, db); v != 1 {
		t.Errorf("version %d, want 1", v)
	}
	if n := count(t, db, `SELECT count(*) FROM sqlite_master WHERE name = 'later'`) +
		count(t, db, `SELECT count(*) FROM pragma_table_info('parent') WHERE name = 'later'`); n != 0 {
		t.Errorf("the failed migration left %d of its changes", n)
	}
}

// A migration that leaves a foreign key violation fails and rolls back
// whole, though migrations run with foreign keys off.
func TestApplyRollsBackAMigrationThatLeavesAForeignKeyViolation(t *testing.T) {
	db := open(t, filepath.Join(t.TempDir(), "x.db"))
	ctx := context.Background()
	err := Apply(ctx, db, Migrations{FS: plus("00002_orphan.sql", `-- +goose Up
INSERT INTO child VALUES (2, 42);
-- +goose Down
DELETE FROM child WHERE id = 2;
`)}, quiet)
	if err == nil || !strings.Contains(err.Error(), "1 foreign key violations (child row 2 references parent") {
		t.Fatalf("Apply: %v, want the violation", err)
	}
	if v := version(t, db); v != 1 {
		t.Errorf("version %d, want 1", v)
	}
	if n := count(t, db, `SELECT count(*) FROM child`); n != 1 {
		t.Errorf("%d children, want only the first", n)
	}
	if n := count(t, db, `PRAGMA foreign_keys`); n != 1 {
		t.Errorf("foreign_keys = %d after the failure, want 1", n)
	}
}

// rebuild recreates parent, which child references, as SQLite's table
// rebuild does, in a plain transactional migration.
const rebuild = `-- +goose Up
CREATE TABLE parent_new (id INTEGER PRIMARY KEY, name TEXT NOT NULL CHECK (name != ''));
INSERT INTO parent_new SELECT * FROM parent;
DROP TABLE parent;
ALTER TABLE parent_new RENAME TO parent;
-- +goose Down
CREATE TABLE parent_old (id INTEGER PRIMARY KEY, name TEXT NOT NULL);
INSERT INTO parent_old SELECT * FROM parent;
DROP TABLE parent;
ALTER TABLE parent_old RENAME TO parent;
`

// Run lets a migration rebuild a parent table inside goose's transaction;
// SQLite alone refuses it.
func TestRunLetsAMigrationRebuildAParentTable(t *testing.T) {
	ctx := context.Background()
	ms := Migrations{FS: plus("00002_rebuild.sql", rebuild)}
	db := open(t, filepath.Join(t.TempDir(), "x.db"))
	m, err := New(db, ms, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Run(ctx, func(p *goose.Provider) error {
		if _, err := p.Up(ctx); err != nil {
			return err
		}
		_, err := p.Down(ctx)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if n := count(t, db, `SELECT count(*) FROM child JOIN parent ON parent.id = child.parent_id`); n != 1 {
		t.Errorf("%d children joined to their parent, want 1", n)
	}

	db = open(t, filepath.Join(t.TempDir(), "y.db"))
	m, err = New(db, ms, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.p.Up(ctx); err == nil || !strings.Contains(err.Error(), "FOREIGN KEY") { // not through Run
		t.Fatalf("Up with foreign keys on: %v, want SQLite's FOREIGN KEY failure", err)
	}
}

// Apply logs each migration it applies; on an up-to-date database it
// applies nothing, logs nothing, and writes nothing.
func TestApplyOnAnUpToDateDatabaseChangesNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.db")
	var logs bytes.Buffer
	apply := func() {
		t.Helper()
		logs.Reset()
		db, err := OpenDB(path)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = db.Close() }()
		if err := Apply(context.Background(), db, Migrations{FS: plus("00002_rebuild.sql", rebuild)}, slog.New(slog.NewTextHandler(&logs, nil))); err != nil {
			t.Fatal(err)
		}
	}
	apply()
	if n := strings.Count(logs.String(), `msg="migration applied"`); n != 2 || !strings.Contains(logs.String(), "source=00002_rebuild.sql") {
		t.Fatalf("first Apply logged %d applied migrations:\n%s", n, logs.String())
	}
	before := sum(t, path)
	apply()
	if logs.Len() != 0 {
		t.Errorf("second Apply logged:\n%s", logs.String())
	}
	if !bytes.Equal(before, sum(t, path)) {
		t.Error("the database changed on a second Apply")
	}
}

func sum(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256(b)
	return h[:]
}

// A database migrated by a newer binary still opens, with a warning.
func TestApplyWarnsWhenTheDatabaseIsNewer(t *testing.T) {
	db := open(t, filepath.Join(t.TempDir(), "x.db"))
	ctx := context.Background()
	if err := Apply(ctx, db, Migrations{FS: plus("00099_newer.sql", "-- +goose Up\nSELECT 1;\n-- +goose Down\nSELECT 1;\n")}, quiet); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	if err := Apply(ctx, db, Migrations{FS: parents}, slog.New(slog.NewTextHandler(&logs, nil))); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs.String(), "level=WARN") || !strings.Contains(logs.String(), "db_version=99 binary_version=1") {
		t.Fatalf("no warning about the newer database:\n%s", logs.String())
	}
}

// Migrations need the single connection OpenDB gives: the pragma that turns
// foreign keys off is per connection.
func TestNewRefusesAPooledDB(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := New(db, Migrations{FS: parents}, nil); err == nil {
		t.Fatal("New accepted a pooled *sql.DB")
	}
}
