package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/itsankoff/incident-copilot/internal/signal"
	"github.com/itsankoff/incident-copilot/internal/triage"
)

var t0 = time.Date(2026, 10, 21, 3, 0, 0, 0, time.UTC)

func save(t *testing.T, st *Store, inc triage.Incident) int64 {
	t.Helper()
	ctx := context.Background()
	tx, err := st.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	id, err := tx.Save(ctx, inc)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestLifecycleRoundTrips(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	orders := &signal.Target{Platform: "kubernetes", Location: "kind", Namespace: "demo", Kind: "Deployment", Name: "orders"}
	m := triage.Member{Key: "am/aaa", Name: "A", Severity: signal.Critical, StartsAt: t0, FirstSeen: t0, LastSeen: t0}

	// Two closed Incidents for orders, one resolved for a key: only the
	// latest closed one comes back, linked to the one before.
	first := save(t, st, triage.Incident{Target: orders, OpenedAt: t0, Members: []triage.Member{m}, State: triage.StateClosed})
	ended := m
	ended.End, ended.EndedAt = triage.OutcomeExpired, t0.Add(10*time.Minute)
	second := save(t, st, triage.Incident{Target: orders, OpenedAt: t0, Members: []triage.Member{ended}, State: triage.StateClosed,
		Outcome: triage.OutcomeExpired, RecurrenceOf: first})
	keyed := save(t, st, triage.Incident{Key: "am/ppp", OpenedAt: t0, OpenSince: t0, UpdatedAt: t0.Add(time.Hour),
		Members: []triage.Member{m}, State: triage.StateResolved, Outcome: triage.OutcomeResolved})

	got, err := st.Incidents(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != second || got[1].ID != keyed {
		t.Fatalf("Incidents %+v, want %d and %d", got, second, keyed)
	}
	if g := got[0]; g.RecurrenceOf != first || g.Outcome != triage.OutcomeExpired || g.Members[0].End != triage.OutcomeExpired ||
		!g.Members[0].EndedAt.Equal(ended.EndedAt) {
		t.Errorf("closed Incident %+v", g)
	}
	if g := got[1]; g.State != triage.StateResolved || !g.UpdatedAt.Equal(t0.Add(time.Hour)) || !g.OpenSince.Equal(t0) {
		t.Errorf("resolved Incident %+v", g)
	}
}

func TestOpenAddsLifecycleColumnsToAnOldDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	// The schema before Incidents had a lifecycle.
	for _, q := range []string{
		`CREATE TABLE incidents (id INTEGER PRIMARY KEY AUTOINCREMENT, target TEXT, key TEXT NOT NULL,
			opened_at TEXT NOT NULL, ticket_ref TEXT NOT NULL DEFAULT '')`,
		`CREATE TABLE members (incident_id INTEGER NOT NULL REFERENCES incidents(id), key TEXT NOT NULL,
			starts_at TEXT NOT NULL, source_kind TEXT NOT NULL, source_instance TEXT NOT NULL, name TEXT NOT NULL,
			severity TEXT NOT NULL, link TEXT NOT NULL, first_seen TEXT NOT NULL, last_seen TEXT NOT NULL,
			PRIMARY KEY (incident_id, key, starts_at))`,
		`INSERT INTO incidents (target, key, opened_at, ticket_ref) VALUES (NULL, 'am/ppp', '2026-10-21T03:00:00Z', '19')`,
		`INSERT INTO members VALUES (1, 'am/ppp', '2026-10-21T03:00:00Z', 'alertmanager', 'am', 'PodNotReady', 'warning', '',
			'2026-10-21T03:00:00Z', '2026-10-21T03:05:00Z')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	_ = db.Close()

	for range 2 { // and opening it again changes nothing
		st, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		got, err := st.Incidents(context.Background())
		_ = st.Close()
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].State != triage.StateOpen || got[0].TicketRef != "19" || len(got[0].Members) != 1 || got[0].Members[0].End != "" {
			t.Fatalf("Incidents %+v, want the old one, open", got)
		}
	}
}
