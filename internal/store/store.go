// Package store keeps the Control plane's state in SQLite: Incidents, their
// member Signals, ticket references, and the kill switch.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	_ "modernc.org/sqlite"

	"github.com/itsankoff/incident-copilot/internal/signal"
	"github.com/itsankoff/incident-copilot/internal/triage"
)

type Store struct {
	db *sql.DB
}

// Open opens or creates the database at path and applies its pending
// migrations, logging each one to log. It fails with the migration's error
// if one fails; that migration has then rolled back whole.
func Open(path string, log *slog.Logger) (*Store, error) {
	db, err := OpenDB(path)
	if err != nil {
		return nil, err
	}
	p, err := NewMigrator(db, nil)
	if err == nil {
		err = migrate(context.Background(), db, p, log)
	}
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store %s: %w", path, err)
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

// KillSwitch returns the kill switch's level, or "" when it is not set.
func (s *Store) KillSwitch(ctx context.Context) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE name = 'kill_switch'`).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

// SetKillSwitch sets the kill switch to level; "" clears it.
func (s *Store) SetKillSwitch(ctx context.Context, level string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO settings (name, value) VALUES ('kill_switch', ?)
		ON CONFLICT (name) DO UPDATE SET value = excluded.value`, level)
	return err
}

func formatTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

// parseTime reads a time this package wrote, so it cannot fail.
func parseTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t
}
