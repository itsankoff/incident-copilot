// Package store keeps the Control plane's state in SQLite: open Incidents,
// their member Signals, and ticket references.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
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
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// OpenIncidents returns every open Incident with its members.
func (s *Store) OpenIncidents(ctx context.Context) ([]triage.Incident, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, target, key, opened_at, ticket_ref FROM incidents ORDER BY id`)
	if err != nil {
		return nil, err
	}
	var out []triage.Incident
	byID := map[int64]int{}
	for rows.Next() {
		var inc triage.Incident
		var target sql.NullString
		var opened string
		if err := rows.Scan(&inc.ID, &target, &inc.Key, &opened, &inc.TicketRef); err != nil {
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
		inc.OpenedAt = parseTime(opened)
		byID[inc.ID] = len(out)
		out = append(out, inc)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	rows, err = s.db.QueryContext(ctx, `SELECT incident_id, key, starts_at, source_kind, source_instance,
		name, severity, link, first_seen, last_seen FROM members ORDER BY incident_id, first_seen, rowid`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id int64
		var m triage.Member
		var startsAt, first, last string
		if err := rows.Scan(&id, &m.Key, &startsAt, &m.Source.Kind, &m.Source.Instance,
			&m.Name, &m.Severity, &m.Link, &first, &last); err != nil {
			return nil, err
		}
		m.StartsAt, m.FirstSeen, m.LastSeen = parseTime(startsAt), parseTime(first), parseTime(last)
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
		res, err := t.tx.ExecContext(ctx, `INSERT INTO incidents (target, key, opened_at) VALUES (?, ?, ?)`,
			target, inc.Key, formatTime(inc.OpenedAt))
		if err != nil {
			return 0, err
		}
		if id, err = res.LastInsertId(); err != nil {
			return 0, err
		}
	}
	for _, m := range inc.Members {
		if _, err := t.tx.ExecContext(ctx, `INSERT INTO members (incident_id, key, starts_at, source_kind,
			source_instance, name, severity, link, first_seen, last_seen) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (incident_id, key, starts_at) DO UPDATE SET last_seen = excluded.last_seen`,
			id, m.Key, formatTime(m.StartsAt), m.Source.Kind, m.Source.Instance,
			m.Name, string(m.Severity), m.Link, formatTime(m.FirstSeen), formatTime(m.LastSeen)); err != nil {
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
