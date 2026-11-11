package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/pressly/goose/v3"

	"github.com/itsankoff/incident-copilot/internal/store/migrations"
	"github.com/itsankoff/incident-copilot/internal/triage"
)

var update = flag.Bool("update", false, "rewrite testdata/schema.txt")

const golden = "testdata/schema.txt"

var quiet = slog.New(slog.DiscardHandler)

// schemaOf describes the schema column by column (pragma_table_xinfo),
// with foreign keys, indexes and CHECK constraints. A table created whole
// and one grown by ALTER TABLE store different CREATE text in sqlite_master,
// so the text itself is not compared.
func schemaOf(t *testing.T, db *sql.DB) string {
	t.Helper()
	var b strings.Builder
	dump := func(q string) {
		rows, err := db.Query(q)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = rows.Close() }()
		cols, _ := rows.Columns()
		for rows.Next() {
			v := make([]any, len(cols))
			p := make([]any, len(cols))
			for i := range v {
				p[i] = &v[i]
			}
			if err := rows.Scan(p...); err != nil {
				t.Fatal(err)
			}
			fmt.Fprintln(&b, v...)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
	}
	// The store's own tables, m, leaving out SQLite's and goose's.
	const own = ` m.type = 'table' AND m.name NOT LIKE 'sqlite_%' AND m.name NOT LIKE 'goose_%'`
	dump(`SELECT m.name, 'column', p.cid, p.name, p.type, 'notnull=' || p."notnull", 'default=' || coalesce(p.dflt_value, 'NULL'),
		'pk=' || p.pk, 'hidden=' || p.hidden
		FROM sqlite_master m JOIN pragma_table_xinfo(m.name) p WHERE` + own + ` ORDER BY m.name, p.cid`)
	dump(`SELECT m.name, 'foreign_key', f.id, f.seq, f."from", f."table", f."to", f.on_update, f.on_delete
		FROM sqlite_master m JOIN pragma_foreign_key_list(m.name) f WHERE` + own + ` ORDER BY m.name, f.id, f.seq`)
	dump(`SELECT m.name, 'index', i.name, i."unique", i.origin, i.partial, group_concat(c.name, ',')
		FROM sqlite_master m JOIN pragma_index_list(m.name) i JOIN pragma_index_info(i.name) c WHERE` + own + `
		GROUP BY m.name, i.name ORDER BY m.name, i.name`)
	rows, err := db.Query(`SELECT m.name, m.sql FROM sqlite_master m WHERE` + own + ` ORDER BY m.name`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var name, text string
		if err := rows.Scan(&name, &text); err != nil {
			t.Fatal(err)
		}
		for _, c := range checks(text) {
			fmt.Fprintln(&b, name, "check", c)
		}
	}
	return b.String()
}

var checkKeyword = regexp.MustCompile(`(?i)\bCHECK\s*\(`)

// checks returns the CHECK constraints in a CREATE TABLE statement, with
// comments dropped and whitespace collapsed: SQLite keeps them only there.
func checks(createSQL string) []string {
	var lines []string
	for _, l := range strings.Split(createSQL, "\n") {
		l, _, _ = strings.Cut(l, "--")
		lines = append(lines, l)
	}
	text := strings.Join(strings.Fields(strings.Join(lines, " ")), " ")
	var out []string
	for _, loc := range checkKeyword.FindAllStringIndex(text, -1) {
		depth, end := 0, loc[1]-1
		for i := end; i < len(text); i++ {
			if text[i] == '(' {
				depth++
			} else if text[i] == ')' {
				if depth--; depth == 0 {
					end = i
					break
				}
			}
		}
		out = append(out, text[loc[0]:end+1])
	}
	return out
}

// load builds a database from a sqlite3 .dump in testdata and returns its path.
func load(t *testing.T, dump string) string {
	t.Helper()
	text, err := os.ReadFile(dump)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(string(text)); err != nil {
		t.Fatal(err)
	}
	return path
}

// openDBFor opens path the way Open does, without migrating it.
func openDBFor(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := OpenDB(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func checkGolden(t *testing.T, got string) {
	t.Helper()
	if *update {
		if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Fatalf("schema differs from %s (go test ./internal/store -update rewrites it):\ngot\n%s\nwant\n%s", golden, got, want)
	}
}

// with returns the store's migrations plus extra test-only SQL files.
func with(extra map[string]string) fs.FS {
	fsys := fstest.MapFS{}
	entries, _ := fs.ReadDir(migrations.FS, ".")
	for _, e := range entries {
		b, _ := fs.ReadFile(migrations.FS, e.Name())
		fsys[e.Name()] = &fstest.MapFile{Data: b}
	}
	for name, text := range extra {
		fsys[name] = &fstest.MapFile{Data: []byte(text)}
	}
	return fsys
}

func provider(t *testing.T, db *sql.DB, fsys fs.FS, gos []*goose.Migration) *goose.Provider {
	t.Helper()
	p, err := newMigrator(db, fsys, gos, nil)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func dbVersion(t *testing.T, p *goose.Provider) int64 {
	t.Helper()
	v, err := p.GetDBVersion(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// A fresh database and one of each shape the store's in-code DDL built
// (before PR #85, after #85, after #88) all open, keep their rows, and end
// with the golden schema.
func TestOpenAdoptsEveryDatabaseShape(t *testing.T) {
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
			st, err := Open(tc.path, quiet)
			if err != nil {
				t.Fatal(err)
			}
			got, err := st.Incidents(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			level, err := st.KillSwitch(context.Background())
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
			checkGolden(t, schemaOf(t, openDBFor(t, tc.path)))
		})
	}
}

// Opening a database that is up to date applies nothing and writes nothing.
func TestOpenUpToDateChangesNothing(t *testing.T) {
	for _, path := range []string{filepath.Join(t.TempDir(), "state.db"), load(t, "testdata/pre85.sql")} {
		var logs bytes.Buffer
		open := func() {
			t.Helper()
			logs.Reset()
			st, err := Open(path, slog.New(slog.NewTextHandler(&logs, nil)))
			if err != nil {
				t.Fatal(err)
			}
			_ = st.Close()
		}
		open()
		if n := strings.Count(logs.String(), "store migration applied"); n != len(provider(t, openDBFor(t, path), migrations.FS, migrations.Go).ListSources()) {
			t.Fatalf("first Open logged %d applied migrations:\n%s", n, logs.String())
		}
		before := sum(t, path)
		open()
		if logs.Len() != 0 {
			t.Errorf("second Open logged:\n%s", logs.String())
		}
		if after := sum(t, path); !bytes.Equal(before, after) {
			t.Fatalf("%s changed on a second Open", path)
		}
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

// A database migrated by a newer binary still opens, with a warning
// (expand/contract keeps the previous release working on the new schema).
func TestOpenWarnsWhenTheDatabaseIsNewer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	db := openDBFor(t, path)
	newer := provider(t, db, with(map[string]string{"00099_newer.sql": "-- +goose Up\nCREATE TABLE later (x TEXT);\n-- +goose Down\nDROP TABLE later;\n"}), migrations.Go)
	if err := migrate(context.Background(), db, newer, quiet); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	var logs bytes.Buffer
	st, err := Open(path, slog.New(slog.NewTextHandler(&logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	_ = st.Close()
	if !strings.Contains(logs.String(), "level=WARN") || !strings.Contains(logs.String(), "db_version=99") {
		t.Fatalf("no warning about the newer database:\n%s", logs.String())
	}
}

// A migration that fails rolls back whole: no partial schema, and the
// version stays where it was.
func TestFailedMigrationRollsBackWhole(t *testing.T) {
	db := openDBFor(t, load(t, "testdata/post85.sql"))
	ctx := context.Background()
	if err := migrate(ctx, db, provider(t, db, migrations.FS, migrations.Go), quiet); err != nil {
		t.Fatal(err)
	}
	broken := provider(t, db, with(map[string]string{"00099_broken.sql": `-- +goose Up
CREATE TABLE later (name TEXT PRIMARY KEY);
ALTER TABLE incidents ADD COLUMN later TEXT NOT NULL DEFAULT '';
INSERT INTO nope VALUES (1);
-- +goose Down
SELECT 1;
`}), migrations.Go)
	before := dbVersion(t, broken)
	err := migrate(ctx, db, broken, quiet)
	if err == nil || !strings.Contains(err.Error(), "no such table: nope") {
		t.Fatalf("migrate: %v, want the migration's error", err)
	}
	if v := dbVersion(t, broken); v != before {
		t.Errorf("version %d after the failure, want %d", v, before)
	}
	checkGolden(t, schemaOf(t, db))
}

// Up, down to 0 (nothing left), and up again (the golden schema).
func TestMigrationsUpDownUp(t *testing.T) {
	db := openDBFor(t, filepath.Join(t.TempDir(), "state.db"))
	p := provider(t, db, migrations.FS, migrations.Go)
	ctx := context.Background()
	run := func(f func() error) {
		t.Helper()
		if err := WithForeignKeysOff(ctx, db, f); err != nil {
			t.Fatal(err)
		}
	}
	run(func() error { _, err := p.Up(ctx); return err })
	checkGolden(t, schemaOf(t, db))
	run(func() error { _, err := p.DownTo(ctx, 0); return err })
	if s := schemaOf(t, db); s != "" {
		t.Fatalf("schema left after down to 0:\n%s", s)
	}
	run(func() error { _, err := p.Up(ctx); return err })
	checkGolden(t, schemaOf(t, db))
}

// Tools that read only the .sql files (Atlas, sqlc, an ORM's drift check)
// must see the whole schema: DDL lives only in SQL files, and Go migrations
// change data only (00001 does nothing on an empty database).
func TestSQLFilesAloneBuildTheSchema(t *testing.T) {
	db := openDBFor(t, filepath.Join(t.TempDir(), "state.db"))
	if err := migrate(context.Background(), db, provider(t, db, migrations.FS, nil), quiet); err != nil {
		t.Fatal(err)
	}
	checkGolden(t, schemaOf(t, db))
}

// rebuild is SQLite's table rebuild of incidents, the parent of
// members.incident_id, as a plain transactional migration.
const rebuild = `-- +goose Up
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
`

// A rebuild of a parent table runs inside goose's transaction, keeps rows
// and foreign keys, because migrations run with foreign keys off.
func TestParentTableRebuildRunsInGoosesTransaction(t *testing.T) {
	ctx := context.Background()
	t.Run("with foreign keys off", func(t *testing.T) {
		db := openDBFor(t, load(t, "testdata/post85.sql"))
		p := provider(t, db, with(map[string]string{"00099_rebuild.sql": rebuild}), migrations.Go)
		if err := migrate(ctx, db, p, quiet); err != nil {
			t.Fatal(err)
		}
		var members int
		if err := db.QueryRow(`SELECT count(*) FROM members JOIN incidents ON incidents.id = members.incident_id`).Scan(&members); err != nil || members != 3 {
			t.Fatalf("%d members joined to their Incidents (%v), want 3", members, err)
		}
		if s := schemaOf(t, db); !strings.Contains(s, "incidents check CHECK (state IN ('', 'open', 'resolved', 'closed'))") {
			t.Fatalf("the golden schema does not show the CHECK:\n%s", s)
		}
		if _, err := db.Exec(`UPDATE incidents SET state = 'bogus'`); err == nil || !strings.Contains(err.Error(), "CHECK") {
			t.Fatalf("the rebuilt table's CHECK did not hold: %v", err)
		}
		var fk int
		if err := db.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil || fk != 1 {
			t.Fatalf("foreign_keys = %d (%v) after migrating, want 1", fk, err)
		}
	})
	t.Run("with foreign keys on, as SQLite would run it", func(t *testing.T) {
		db := openDBFor(t, load(t, "testdata/post85.sql"))
		p := provider(t, db, with(map[string]string{"00099_rebuild.sql": rebuild}), migrations.Go)
		if _, err := p.Up(ctx); err == nil || !strings.Contains(err.Error(), "FOREIGN KEY") {
			t.Fatalf("Up: %v, want SQLite's FOREIGN KEY failure", err)
		}
	})
}

// A migration that leaves a foreign key violation fails the run.
func TestMigrationLeavingAForeignKeyViolationFails(t *testing.T) {
	db := openDBFor(t, filepath.Join(t.TempDir(), "state.db"))
	p := provider(t, db, with(map[string]string{"00099_orphan.sql": `-- +goose Up
INSERT INTO members VALUES (42, 'k', 's', 'kind', 'inst', 'n', 'info', '', 'f', 'l', '', '');
-- +goose Down
DELETE FROM members WHERE incident_id = 42;
`}), migrations.Go)
	if err := migrate(context.Background(), db, p, quiet); err == nil || !strings.Contains(err.Error(), "foreign key") {
		t.Fatalf("migrate: %v, want a foreign key violation", err)
	}
}
