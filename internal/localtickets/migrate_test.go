package localtickets

import (
	"context"
	"flag"
	"path/filepath"
	"testing"

	"github.com/pressly/goose/v3"

	"github.com/itsankoff/incident-copilot/internal/migrate"
	"github.com/itsankoff/incident-copilot/internal/migrate/migratetest"
)

var update = flag.Bool("update", false, "rewrite testdata/schema.txt")

// A fresh tickets database and one localtickets built before migrations
// (PR #91) both open, keep their tickets, and end with the golden schema.
func TestOpenAdoptsATicketsDatabaseFromBeforeMigrations(t *testing.T) {
	ctx := context.Background()
	legacy := filepath.Join(t.TempDir(), "tickets.db")
	migratetest.Load(t, legacy, "testdata/post91.sql")
	for _, tc := range []struct {
		name, path string
		seeded     bool // holds post91.sql's two tickets
	}{
		{"fresh", filepath.Join(t.TempDir(), "tickets.db"), false},
		{"post91", legacy, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tk := open(t, tc.path)
			if tc.seeded {
				first, err := tk.ticket(ctx, "1")
				if err != nil {
					t.Fatal(err)
				}
				second, err := tk.ticket(ctx, "2")
				if err != nil {
					t.Fatal(err)
				}
				if first.Title != "orders is crash looping" || len(first.Comments) != 1 || first.Closed != "" || second.Closed == "" {
					t.Fatalf("tickets not kept: %+v, %+v", first, second)
				}
			}
			migratetest.Golden(t, "testdata/schema.txt", migratetest.Schema(t, tk.db), *update)
		})
	}
}

// Up, down to 0 (nothing left), and up again (the golden schema).
func TestMigrationsUpDownUp(t *testing.T) {
	db, err := migrate.OpenDB(filepath.Join(t.TempDir(), "tickets.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	m, err := migrate.New(db, Migrations, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, step := range []func(p *goose.Provider) error{
		func(p *goose.Provider) error { _, err := p.Up(ctx); return err },
		func(p *goose.Provider) error { _, err := p.DownTo(ctx, 0); return err },
	} {
		if err := m.Run(ctx, step); err != nil {
			t.Fatal(err)
		}
	}
	if s := migratetest.Schema(t, db); s != "" {
		t.Fatalf("schema left after down to 0:\n%s", s)
	}
	if err := m.Run(ctx, func(p *goose.Provider) error { _, err := p.Up(ctx); return err }); err != nil {
		t.Fatal(err)
	}
	migratetest.Golden(t, "testdata/schema.txt", migratetest.Schema(t, db), *update)
}
