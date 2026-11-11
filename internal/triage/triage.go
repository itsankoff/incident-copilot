// Package triage decides what an incoming Signal, the passing of time, or a
// closed ticket does to the Incidents (ADR 0002 D4). It is a pure function:
// no I/O, and the clock is an argument.
package triage

import (
	"slices"
	"time"

	"github.com/itsankoff/incident-copilot/internal/signal"
)

// State is where an Incident is in its lifecycle.
type State string

const (
	StateOpen     State = "open"     // a member Signal is firing
	StateResolved State = "resolved" // every member Signal has ended; the ticket stays open
	StateClosed   State = "closed"   // its ticket is closed; a later Signal is a recurrence
)

// Outcome is how a member Signal ended, and how a resolved Incident did.
type Outcome string

const (
	OutcomeResolved Outcome = "resolved" // the source said it resolved
	OutcomeExpired  Outcome = "expired"  // no firing notice for Liveness
)

const (
	// Liveness is how long a firing Signal lives without a firing notice:
	// twice the 5-minute re-send every Signal source must keep (ADR 0002 D3).
	Liveness = 10 * time.Minute
	// IdleClose is how long a resolved Incident stays open with no activity.
	IdleClose = 24 * time.Hour
)

// Incident is one problem with one Target, gathering every Signal about it.
type Incident struct {
	ID           int64          // 0 until stored
	Target       *signal.Target // nil when no Signal names a workload
	Key          string         // the opening Signal's key; set only when Target is nil
	OpenedAt     time.Time
	Members      []Member
	TicketRef    string // empty until the ticket exists
	State        State
	Outcome      Outcome   // set while resolved or closed after resolving
	OpenSince    time.Time // when it last opened or reopened
	UpdatedAt    time.Time // the last decision about it; the idle close counts from here
	RecurrenceOf int64     // the closed Incident this one is a recurrence of, or 0
}

// Member is one Signal episode (key plus startsAt) in an Incident.
type Member struct {
	Key       string
	Source    signal.SourceRef
	Name      string
	Severity  signal.Severity
	StartsAt  time.Time
	Link      string
	FirstSeen time.Time
	LastSeen  time.Time
	End       Outcome // empty while firing
	EndedAt   time.Time
}

type Kind string

const (
	Open    Kind = "open"    // a new Incident, a recurrence when RecurrenceOf is set
	Join    Kind = "join"    // a new member of an open Incident, or an expired one firing again
	Repeat  Kind = "repeat"  // a repeat notice: last-seen only
	End     Kind = "end"     // a member ended and others still fire
	Resolve Kind = "resolve" // the last firing member ended
	Reopen  Kind = "reopen"  // a Signal for a resolved Incident
	Close   Kind = "close"   // the Incident is closed
	Drop    Kind = "drop"    // no change
)

// Decision is what a Signal does. Incident is the Incident after the
// decision and Member the member it added or touched; both are zero for Drop,
// and Member is zero for Close.
type Decision struct {
	Kind     Kind
	Incident Incident
	Member   Member
	Reason   string
}

// Triage decides what s does to incs: every Incident that is not closed, at
// most one per Target (or per key when there is no Target), plus closed ones
// for recurrence. It never modifies incs.
func Triage(incs []Incident, s signal.Signal, now time.Time) Decision {
	if s.Status != signal.Firing {
		return resolveKey(incs, s.Key, now)
	}

	m := Member{
		Key: s.Key, Source: s.Source, Name: s.Name, Severity: s.Severity,
		StartsAt: s.StartsAt, Link: s.Link, FirstSeen: now, LastSeen: now,
	}
	i := slices.IndexFunc(incs, func(inc Incident) bool { return inc.State != StateClosed && sameIncident(inc, s) })
	if i < 0 {
		inc := Incident{Target: s.Target, OpenedAt: now, Members: []Member{m}, State: StateOpen, OpenSince: now, UpdatedAt: now}
		if s.Target == nil {
			inc.Key = s.Key
		}
		for _, old := range incs {
			if old.State == StateClosed && sameIncident(old, s) && old.ID > inc.RecurrenceOf {
				inc.RecurrenceOf = old.ID
			}
		}
		return Decision{Kind: Open, Incident: inc, Member: m}
	}

	inc := clone(incs[i])
	kind := Join
	j := slices.IndexFunc(inc.Members, func(old Member) bool { return old.Key == s.Key && old.StartsAt.Equal(s.StartsAt) })
	if j >= 0 {
		old := inc.Members[j]
		if old.End == OutcomeResolved {
			return Decision{Kind: Drop, Reason: "firing notice for an episode that already resolved"}
		}
		if old.End == "" {
			kind = Repeat
		}
		// An expired episode that fires again is live again.
		inc.Members[j].LastSeen, inc.Members[j].End, inc.Members[j].EndedAt = now, "", time.Time{}
		m = inc.Members[j]
	} else {
		inc.Members = append(inc.Members, m)
	}
	inc.UpdatedAt = now
	if inc.State == StateResolved {
		inc.State, inc.Outcome, inc.OpenSince = StateOpen, "", now
		kind = Reopen
	}
	return Decision{Kind: kind, Incident: inc, Member: m}
}

// resolveKey ends the firing member with key in the open Incidents.
func resolveKey(incs []Incident, key string, now time.Time) Decision {
	known := false
	for _, inc := range incs {
		if inc.State == StateClosed {
			continue
		}
		for j, m := range inc.Members {
			if m.Key != key {
				continue
			}
			if m.End == "" {
				return end(inc, j, OutcomeResolved, now)
			}
			known = true
		}
	}
	if known {
		return Decision{Kind: Drop, Reason: "resolved Signal for an episode that already ended"}
	}
	return Decision{Kind: Drop, Reason: "resolved Signal for an unknown key"}
}

// Tick decides what the passing of time does to incs: it expires members with
// no firing notice for Liveness, and closes Incidents resolved with no
// activity for IdleClose. Each Decision's Incident includes the ones before
// it, so they apply in order.
func Tick(incs []Incident, now time.Time) []Decision {
	var out []Decision
	for _, inc := range incs {
		switch inc.State {
		case StateOpen:
			for j, m := range inc.Members {
				if m.End == "" && now.Sub(m.LastSeen) >= Liveness {
					d := end(inc, j, OutcomeExpired, now)
					out = append(out, d)
					inc = d.Incident
				}
			}
		case StateResolved:
			if now.Sub(inc.UpdatedAt) >= IdleClose {
				out = append(out, closeIncident(inc, now, "no activity for 24 hours"))
			}
		}
	}
	return out
}

// TicketClosed decides what a human closing inc's ticket does: the ticket is
// the source of truth, so the Incident closes.
func TicketClosed(inc Incident, now time.Time) Decision {
	return closeIncident(inc, now, "the ticket was closed by a human")
}

// Severity is the highest severity among the firing members, or empty when
// none fires. It is shown in the ticket and is an input to nothing else.
func (inc Incident) Severity() signal.Severity {
	var best signal.Severity
	for _, m := range inc.Members {
		if m.End == "" && rank[m.Severity] > rank[best] {
			best = m.Severity
		}
	}
	return best
}

var rank = map[signal.Severity]int{signal.Unknown: 1, signal.Info: 2, signal.Warning: 3, signal.Critical: 4}

// end ends member j of inc with outcome, resolving inc if it was the last
// firing one.
func end(inc Incident, j int, outcome Outcome, now time.Time) Decision {
	inc = clone(inc)
	inc.Members[j].End, inc.Members[j].EndedAt = outcome, now
	inc.UpdatedAt = now
	m := inc.Members[j]
	if slices.ContainsFunc(inc.Members, func(m Member) bool { return m.End == "" }) {
		return Decision{Kind: End, Incident: inc, Member: m}
	}
	// The outcome counts only the members that ended since the Incident
	// last opened, not those carried over from before a reopen.
	inc.State, inc.Outcome = StateResolved, OutcomeResolved
	for _, m := range inc.Members {
		if !m.EndedAt.Before(inc.OpenSince) && m.End != OutcomeResolved {
			inc.Outcome = OutcomeExpired
		}
	}
	return Decision{Kind: Resolve, Incident: inc, Member: m}
}

func closeIncident(inc Incident, now time.Time, reason string) Decision {
	inc = clone(inc)
	inc.State, inc.UpdatedAt = StateClosed, now
	return Decision{Kind: Close, Incident: inc, Reason: reason}
}

func clone(inc Incident) Incident {
	inc.Members = slices.Clone(inc.Members)
	return inc
}

func sameIncident(inc Incident, s signal.Signal) bool {
	if s.Target == nil {
		return inc.Target == nil && inc.Key == s.Key
	}
	return inc.Target != nil && *inc.Target == *s.Target
}
