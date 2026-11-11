package migrations

import (
	"context"
	"database/sql"
	"fmt"
)

// Databases created before migrations existed, by store.Open's own DDL, come
// in two shapes: the first schema, and the first schema plus PR #85's
// lifecycle columns. adoptLegacy brings the first shape to the second, so
// that 00002_baseline.sql finds every database it adopts already complete.
// On an empty database it does nothing, which is why tools that read only
// the .sql files still see the whole schema.
//
// This is the one Go migration that changes the schema, and only for
// databases that predate migrations.
func init() { register(adoptLegacy, nil) }

// legacyColumns are the columns PR #85 added, as its ALTER TABLE statements
// had them.
var legacyColumns = []struct{ table, column, def string }{
	{"incidents", "state", `TEXT NOT NULL DEFAULT 'open'`},
	{"incidents", "outcome", `TEXT NOT NULL DEFAULT ''`},
	{"incidents", "open_since", `TEXT NOT NULL DEFAULT ''`},
	{"incidents", "updated_at", `TEXT NOT NULL DEFAULT ''`},
	{"incidents", "recurrence_of", `INTEGER NOT NULL DEFAULT 0`},
	{"members", "end_outcome", `TEXT NOT NULL DEFAULT ''`},
	{"members", "ended_at", `TEXT NOT NULL DEFAULT ''`},
}

func adoptLegacy(ctx context.Context, tx *sql.Tx) error {
	for _, c := range legacyColumns {
		var tables, columns int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, c.table).Scan(&tables); err != nil {
			return err
		}
		if tables == 0 {
			continue // a new database: 00002 creates the table whole
		}
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM pragma_table_info(?) WHERE name = ?`, c.table, c.column).Scan(&columns); err != nil {
			return err
		}
		if columns == 0 {
			if _, err := tx.ExecContext(ctx, fmt.Sprintf(`ALTER TABLE %s ADD COLUMN %s %s`, c.table, c.column, c.def)); err != nil {
				return err
			}
		}
	}
	return nil
}
