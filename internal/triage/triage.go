// Package triage decides what an incoming Signal does to the open Incidents
// (ADR 0002 D4). It is a pure function: no I/O, and the clock is an argument.
package triage

import (
	"slices"
	"time"

	"github.com/itsankoff/incident-copilot/internal/signal"
)

// Incident is one problem with one Target, gathering every Signal about it.
type Incident struct {
	ID        int64          // 0 until stored
	Target    *signal.Target // nil when no Signal names a workload
	Key       string         // the opening Signal's key; set only when Target is nil
	OpenedAt  time.Time
	Members   []Member
	TicketRef string // empty until the ticket exists
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
}

type Kind string

const (
	Open   Kind = "open"   // a new Incident
	Join   Kind = "join"   // a new member of an open Incident
	Repeat Kind = "repeat" // a repeat notice: last-seen only
	Drop   Kind = "drop"   // no change
)

// Decision is what a Signal does. Incident is the Incident after the
// decision and Member the member it added or touched; both are zero for Drop.
type Decision struct {
	Kind     Kind
	Incident Incident
	Member   Member
	Reason   string
}

// Triage decides what s does given the open Incidents, at most one per Target
// (or per key when there is no Target). It never modifies open.
func Triage(open []Incident, s signal.Signal, now time.Time) Decision {
	if s.Status != signal.Firing {
		// Resolving Incidents is #45; until then a resolved Signal changes nothing.
		return Decision{Kind: Drop, Reason: "resolved Signals are not handled yet"}
	}

	m := Member{
		Key: s.Key, Source: s.Source, Name: s.Name, Severity: s.Severity,
		StartsAt: s.StartsAt, Link: s.Link, FirstSeen: now, LastSeen: now,
	}
	i := slices.IndexFunc(open, func(inc Incident) bool { return sameIncident(inc, s) })
	if i < 0 {
		inc := Incident{Target: s.Target, OpenedAt: now, Members: []Member{m}}
		if s.Target == nil {
			inc.Key = s.Key
		}
		return Decision{Kind: Open, Incident: inc, Member: m}
	}

	inc := open[i]
	inc.Members = slices.Clone(inc.Members)
	for j, old := range inc.Members {
		if old.Key == s.Key && old.StartsAt.Equal(s.StartsAt) {
			inc.Members[j].LastSeen = now
			return Decision{Kind: Repeat, Incident: inc, Member: inc.Members[j]}
		}
	}
	inc.Members = append(inc.Members, m)
	return Decision{Kind: Join, Incident: inc, Member: m}
}

func sameIncident(inc Incident, s signal.Signal) bool {
	if s.Target == nil {
		return inc.Target == nil && inc.Key == s.Key
	}
	return inc.Target != nil && *inc.Target == *s.Target
}
