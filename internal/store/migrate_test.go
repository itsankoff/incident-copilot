package store

import (
	"context"
	"database/sql"
	"flag"
	"io/fs"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/pressly/goose/v3"

	"github.com/itsankoff/incident-copilot/internal/migrate"
	"github.com/itsankoff/incident-copilot/internal/migrate/migratetest"
	"github.com/itsankoff/incident-copilot/internal/triage"
)

var update = flag.Bool("update", false, "rewrite testdata/schema.txt")

var quiet = slog.New(slog.DiscardHandler)

func golden(t *testing.T, db *sql.DB) {
	t.Helper()
	migratetest.Golden(t, "testdata/schema.txt", migratetest.Schema(t, db), *update)
}

// load builds a database from a sqlite3 .dump in testdata and returns its path.
func load(t *testing.T, dump string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.db")
	migratetest.Load(t, path, dump)
	return path
}

// openDB opens path the way Open does, without migrating it.
func openDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := migrate.OpenDB(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// withExtraMigrations returns the store's migrations plus test-only SQL files.
func withExtraMigrations(extra map[string]string) migrate.Migrations {
	fsys := fstest.MapFS{}
	entries, _ := fs.ReadDir(Migrations.FS, ".")
	for _, e := range entries {
		b, _ := fs.ReadFile(Migrations.FS, e.Name())
		fsys[e.Name()] = &fstest.MapFile{Data: b}
	}
	for name, text := range extra {
		fsys[name] = &fstest.MapFile{Data: []byte(text)}
	}
	return migrate.Migrations{FS: fsys, Go: Migrations.Go}
}

// A fresh database and one of each shape the store's in-code DDL built
// (before PR #85, after #85, after #88) all open, keep their rows, and end
// with the golden schema.
func TestOpenAdoptsEveryDatabaseShape(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name, path string
		want       int
		state      triage.State // what the first Incident's state reads back as
		killSwitch string
	}{
		{"fresh", filepath.Join(t.TempDir(), "state.db"), 0, "", ""},
		{"pre85", load(t, "testdata/pre85.sql"), 2, triage.StateOpen, ""}, // the column's default
		{"post85", load(t, "testdata/post85.sql"), 2, "", ""},             // as that store wrote it
		{"post88", load(t, "testdata/post88.sql"), 2, "", "L1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, err := Open(ctx, tc.path, quiet)
			if err != nil {
				t.Fatal(err)
			}
			got, err := st.Incidents(ctx)
			if err != nil {
				t.Fatal(err)
			}
			level, err := st.KillSwitch(ctx)
			_ = st.Close()
			if err != nil || level != tc.killSwitch {
				t.Fatalf("kill switch %q (%v), want %q", level, err, tc.killSwitch)
			}
			if len(got) != tc.want {
				t.Fatalf("%d Incidents, want %d", len(got), tc.want)
			}
			if tc.want > 0 {
				first, second := got[0], got[1]
				if first.Target == nil || first.Target.Name != "orders" || first.TicketRef != "owner/repo#1" || len(first.Members) != 2 ||
					first.State != tc.state || second.Key != "ic-target/Watchdog" || len(second.Members) != 1 {
					t.Fatalf("Incidents not kept: %+v", got)
				}
			}
			golden(t, openDB(t, tc.path))
		})
	}
}

// Up, down to 0 (nothing left), and up again (the golden schema).
func TestMigrationsUpDownUp(t *testing.T) {
	db := openDB(t, filepath.Join(t.TempDir(), "state.db"))
	m, err := migrate.New(db, Migrations, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	run := func(f func(p *goose.Provider) error) {
		t.Helper()
		if err := m.Run(ctx, f); err != nil {
			t.Fatal(err)
		}
	}
	run(func(p *goose.Provider) error { _, err := p.Up(ctx); return err })
	golden(t, db)
	run(func(p *goose.Provider) error { _, err := p.DownTo(ctx, 0); return err })
	if s := migratetest.Schema(t, db); s != "" {
		t.Fatalf("schema left after down to 0:\n%s", s)
	}
	run(func(p *goose.Provider) error { _, err := p.Up(ctx); return err })
	golden(t, db)
}

// Tools that read only the .sql files (Atlas, sqlc, an ORM's drift check)
// must see the whole schema: DDL lives only in SQL files, and Go migrations
// change data only (00001 does nothing on an empty database).
func TestSQLFilesAloneBuildTheSchema(t *testing.T) {
	db := openDB(t, filepath.Join(t.TempDir(), "state.db"))
	if err := migrate.Apply(context.Background(), db, migrate.Migrations{FS: Migrations.FS}, quiet); err != nil {
		t.Fatal(err)
	}
	golden(t, db)
}

// A rebuild of incidents, the parent of members.incident_id, runs inside
// goose's transaction and keeps the rows and their foreign keys.
func TestIncidentsRebuildRunsInGoosesTransaction(t *testing.T) {
	db := openDB(t, load(t, "testdata/post88.sql"))
	ms := withExtraMigrations(map[string]string{"00099_rebuild.sql": `-- +goose Up
CREATE TABLE incidents_new (
	id            INTEGER PRIMARY KEY AUTOINCREMENT,
	target        TEXT,
	key           TEXT NOT NULL,
	opened_at     TEXT NOT NULL,
	ticket_ref    TEXT NOT NULL DEFAULT '',
	state         TEXT NOT NULL DEFAULT 'open' CHECK (state IN ('', 'open', 'resolved', 'closed')),
	outcome       TEXT NOT NULL DEFAULT '',
	open_since    TEXT NOT NULL DEFAULT '',
	updated_at    TEXT NOT NULL DEFAULT '',
	recurrence_of INTEGER NOT NULL DEFAULT 0
);
INSERT INTO incidents_new SELECT * FROM incidents;
DROP TABLE incidents;
ALTER TABLE incidents_new RENAME TO incidents;
-- +goose Down
SELECT 1;
`})
	if err := migrate.Apply(context.Background(), db, ms, quiet); err != nil {
		t.Fatal(err)
	}
	var members int
	if err := db.QueryRow(`SELECT count(*) FROM members JOIN incidents ON incidents.id = members.incident_id`).Scan(&members); err != nil || members != 3 {
		t.Fatalf("%d members joined to their Incidents (%v), want 3", members, err)
	}
	if s := migratetest.Schema(t, db); !strings.Contains(s, "incidents check CHECK (state IN ('', 'open', 'resolved', 'closed'))") {
		t.Fatalf("the schema does not show the CHECK:\n%s", s)
	}
	if _, err := db.Exec(`UPDATE incidents SET state = 'bogus'`); err == nil || !strings.Contains(err.Error(), "CHECK") {
		t.Fatalf("the rebuilt table's CHECK did not hold: %v", err)
	}
}
