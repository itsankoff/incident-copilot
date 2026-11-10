// Package store keeps the Control plane's state in SQLite: Incidents, their
// member Signals, and ticket references.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/itsankoff/incident-copilot/internal/signal"
	"github.com/itsankoff/incident-copilot/internal/triage"
)

const schema = `
CREATE TABLE IF NOT EXISTS incidents (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	target     TEXT,              -- JSON signal.Target, NULL when there is none
	key        TEXT NOT NULL,     -- the opening Signal's key when there is no Target
	opened_at  TEXT NOT NULL,
	ticket_ref TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS members (
	incident_id     INTEGER NOT NULL REFERENCES incidents(id),
	key             TEXT NOT NULL,
	starts_at       TEXT NOT NULL,
	source_kind     TEXT NOT NULL,
	source_instance TEXT NOT NULL,
	name            TEXT NOT NULL,
	severity        TEXT NOT NULL,
	link            TEXT NOT NULL,
	first_seen      TEXT NOT NULL,
	last_seen       TEXT NOT NULL,
	PRIMARY KEY (incident_id, key, starts_at)
);`

// columns came after the first schema; adding them to a database that already
// has them fails with "duplicate column name", which Open ignores.
var columns = []string{
	`ALTER TABLE incidents ADD COLUMN state TEXT NOT NULL DEFAULT 'open'`,
	`ALTER TABLE incidents ADD COLUMN outcome TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE incidents ADD COLUMN open_since TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE incidents ADD COLUMN updated_at TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE incidents ADD COLUMN recurrence_of INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE members ADD COLUMN end_outcome TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE members ADD COLUMN ended_at TEXT NOT NULL DEFAULT ''`,
}

type Store struct {
	db *sql.DB
}

// Open opens or creates the database at path.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store %s: %w", path, err)
	}
	for _, c := range columns {
		if _, err := db.Exec(c); err != nil && !strings.Contains(err.Error(), "duplicate column name") {
			_ = db.Close()
			return nil, fmt.Errorf("store %s: %w", path, err)
		}
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// triageable selects what Triage decides over: every Incident that is not
// closed, and the latest closed one per Target or key, which a recurrence
// links to.
const triageable = `state != 'closed' OR id IN (SELECT MAX(id) FROM incidents WHERE state = 'closed' GROUP BY target, key)`

// Incidents returns, with their members, every Incident that is not closed
// and the latest closed one per Target or key.
func (s *Store) Incidents(ctx context.Context) ([]triage.Incident, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, target, key, opened_at, ticket_ref, state, outcome,
		open_since, updated_at, recurrence_of FROM incidents WHERE `+triageable+` ORDER BY id`)
	if err != nil {
		return nil, err
	}
	var out []triage.Incident
	byID := map[int64]int{}
	for rows.Next() {
		var inc triage.Incident
		var target sql.NullString
		var opened, since, updated string
		if err := rows.Scan(&inc.ID, &target, &inc.Key, &opened, &inc.TicketRef, &inc.State, &inc.Outcome,
			&since, &updated, &inc.RecurrenceOf); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if target.Valid {
			inc.Target = new(signal.Target)
			if err := json.Unmarshal([]byte(target.String), inc.Target); err != nil {
				_ = rows.Close()
				return nil, err
			}
		}
		inc.OpenedAt, inc.OpenSince, inc.UpdatedAt = parseTime(opened), parseTime(since), parseTime(updated)
		byID[inc.ID] = len(out)
		out = append(out, inc)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	rows, err = s.db.QueryContext(ctx, `SELECT incident_id, key, starts_at, source_kind, source_instance,
		name, severity, link, first_seen, last_seen, end_outcome, ended_at FROM members
		WHERE incident_id IN (SELECT id FROM incidents WHERE `+triageable+`) ORDER BY incident_id, first_seen, rowid`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id int64
		var m triage.Member
		var startsAt, first, last, ended string
		if err := rows.Scan(&id, &m.Key, &startsAt, &m.Source.Kind, &m.Source.Instance,
			&m.Name, &m.Severity, &m.Link, &first, &last, &m.End, &ended); err != nil {
			return nil, err
		}
		m.StartsAt, m.FirstSeen, m.LastSeen, m.EndedAt = parseTime(startsAt), parseTime(first), parseTime(last), parseTime(ended)
		i := byID[id]
		out[i].Members = append(out[i].Members, m)
	}
	return out, rows.Err()
}

// Tx is one change to the store, committed whole or not at all.
type Tx struct {
	tx *sql.Tx
}

func (s *Store) Begin(ctx context.Context) (*Tx, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	return &Tx{tx: tx}, nil
}

func (t *Tx) Commit() error { return t.tx.Commit() }

// Rollback discards the change; after Commit it does nothing.
func (t *Tx) Rollback() { _ = t.tx.Rollback() }

// Save writes inc and its members, inserting the Incident when its ID is 0,
// and returns its ID.
func (t *Tx) Save(ctx context.Context, inc triage.Incident) (int64, error) {
	id := inc.ID
	if id == 0 {
		var target any
		if inc.Target != nil {
			b, _ := json.Marshal(inc.Target) // a struct of strings always marshals
			target = string(b)
		}
		res, err := t.tx.ExecContext(ctx, `INSERT INTO incidents (target, key, opened_at, recurrence_of) VALUES (?, ?, ?, ?)`,
			target, inc.Key, formatTime(inc.OpenedAt), inc.RecurrenceOf)
		if err != nil {
			return 0, err
		}
		if id, err = res.LastInsertId(); err != nil {
			return 0, err
		}
	}
	if _, err := t.tx.ExecContext(ctx, `UPDATE incidents SET state = ?, outcome = ?, open_since = ?, updated_at = ? WHERE id = ?`,
		string(inc.State), string(inc.Outcome), formatTime(inc.OpenSince), formatTime(inc.UpdatedAt), id); err != nil {
		return 0, err
	}
	for _, m := range inc.Members {
		if _, err := t.tx.ExecContext(ctx, `INSERT INTO members (incident_id, key, starts_at, source_kind,
			source_instance, name, severity, link, first_seen, last_seen, end_outcome, ended_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (incident_id, key, starts_at) DO UPDATE SET last_seen = excluded.last_seen,
			end_outcome = excluded.end_outcome, ended_at = excluded.ended_at`,
			id, m.Key, formatTime(m.StartsAt), m.Source.Kind, m.Source.Instance,
			m.Name, string(m.Severity), m.Link, formatTime(m.FirstSeen), formatTime(m.LastSeen),
			string(m.End), formatTime(m.EndedAt)); err != nil {
			return 0, err
		}
	}
	return id, nil
}

// SetTicket records the ticket reference of Incident id.
func (t *Tx) SetTicket(ctx context.Context, id int64, ref string) error {
	_, err := t.tx.ExecContext(ctx, `UPDATE incidents SET ticket_ref = ? WHERE id = ?`, ref, id)
	return err
}

func formatTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

// parseTime reads a time this package wrote, so it cannot fail.
func parseTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t
}
