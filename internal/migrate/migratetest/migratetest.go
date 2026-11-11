// Package migratetest checks a migrated database's schema against a golden
// file, for the tests of each database's migrations.
package migratetest

import (
	"database/sql"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// Schema describes db's tables column by column (pragma_table_xinfo), with
// their foreign keys, indexes and CHECK constraints, leaving out SQLite's
// and goose's own. A table created whole and one grown by ALTER TABLE store
// different CREATE text in sqlite_master, so the text itself is not compared.
func Schema(t *testing.T, db *sql.DB) string {
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
	// The database's own tables, m, leaving out SQLite's and goose's.
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

// Golden fails t unless got equals the golden file at path. With update set
// (go test -update), it first rewrites the file.
func Golden(t *testing.T, path, got string, update bool) {
	t.Helper()
	if update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Fatalf("schema differs from %s (go test -update rewrites it):\ngot\n%s\nwant\n%s", path, got, want)
	}
}

// Load builds a database at path from a sqlite3 .dump file.
func Load(t *testing.T, path, dump string) {
	t.Helper()
	text, err := os.ReadFile(dump)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(string(text)); err != nil {
		t.Fatal(err)
	}
}
