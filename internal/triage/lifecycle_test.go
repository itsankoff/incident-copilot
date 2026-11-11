package triage

import (
	"slices"
	"testing"
	"time"

	"github.com/itsankoff/incident-copilot/internal/signal"
)

// world plays Signals, Ticks, and human closes through Triage and keeps the
// Incidents as the Control plane would store them, numbering new ones from 1.
type world struct {
	t    *testing.T
	incs []Incident
	next int64
}

func (w *world) apply(d Decision) {
	if d.Kind == Drop {
		return
	}
	if d.Incident.ID == 0 {
		w.next++
		d.Incident.ID = w.next
		d.Incident.TicketRef = "t" + string(rune('0'+w.next))
		w.incs = append(w.incs, d.Incident)
		return
	}
	i := slices.IndexFunc(w.incs, func(inc Incident) bool { return inc.ID == d.Incident.ID })
	w.incs[i] = d.Incident
}

func (w *world) get(id int64) Incident {
	w.t.Helper()
	i := slices.IndexFunc(w.incs, func(inc Incident) bool { return inc.ID == id })
	if i < 0 {
		w.t.Fatalf("no Incident %d", id)
	}
	return w.incs[i]
}

// step is one thing that happens at t0+at: a Signal, or a Tick when sig has
// no key, or a human closing Incident closeID's ticket.
type step struct {
	at      time.Duration
	sig     signal.Signal
	closeID int64
	want    []Kind // the kinds of the decisions the step makes, in order
}

func (w *world) play(steps []step) {
	w.t.Helper()
	for i, st := range steps {
		now := t0.Add(st.at)
		var ds []Decision
		switch {
		case st.closeID != 0:
			ds = []Decision{TicketClosed(w.get(st.closeID), now)}
		case st.sig.Key == "":
			ds = Tick(w.incs, now)
		default:
			ds = []Decision{Triage(w.incs, st.sig, now)}
		}
		var kinds []Kind
		for _, d := range ds {
			kinds = append(kinds, d.Kind)
			w.apply(d)
		}
		if !slices.Equal(kinds, st.want) {
			w.t.Fatalf("step %d at %s: kinds %v, want %v", i, st.at, kinds, st.want)
		}
	}
}

func resolved(s signal.Signal) signal.Signal {
	s.Status = signal.Resolved
	return s
}

func withSeverity(s signal.Signal, sev signal.Severity) signal.Signal {
	s.Severity = sev
	return s
}

func TestLifecycle(t *testing.T) {
	crash := sig("am/aaa", "KubePodCrashLooping", orders, t0)
	mismatch := sig("am/bbb", "KubeDeploymentReplicasMismatch", orders, t0)
	podOnly := sig("am/ppp", "PodNotReady", nil, t0)
	crashAgain := sig("am/aaa", "KubePodCrashLooping", orders, t0.Add(time.Hour))

	tests := []struct {
		name  string
		steps []step
		check func(t *testing.T, w *world)
	}{
		{
			name: "every member resolved: resolved, outcome resolved",
			steps: []step{
				{at: 0, sig: crash, want: []Kind{Open}},
				{at: time.Minute, sig: mismatch, want: []Kind{Join}},
				{at: 2 * time.Minute, sig: resolved(crash), want: []Kind{End}},
				{at: 3 * time.Minute, sig: resolved(mismatch), want: []Kind{Resolve}},
			},
			check: func(t *testing.T, w *world) {
				inc := w.get(1)
				if inc.State != StateResolved || inc.Outcome != OutcomeResolved {
					t.Errorf("state %q outcome %q, want resolved/resolved", inc.State, inc.Outcome)
				}
			},
		},
		{
			name: "no firing notice for 10 minutes expires the member",
			steps: []step{
				{at: 0, sig: crash, want: []Kind{Open}},
				{at: 10*time.Minute - time.Second, want: nil},
				{at: 10 * time.Minute, want: []Kind{Resolve}},
			},
			check: func(t *testing.T, w *world) {
				inc := w.get(1)
				if inc.State != StateResolved || inc.Outcome != OutcomeExpired || inc.Members[0].End != OutcomeExpired {
					t.Errorf("state %q outcome %q member end %q, want resolved/expired/expired", inc.State, inc.Outcome, inc.Members[0].End)
				}
			},
		},
		{
			name: "a repeat notice keeps the member live",
			steps: []step{
				{at: 0, sig: crash, want: []Kind{Open}},
				{at: 6 * time.Minute, sig: crash, want: []Kind{Repeat}},
				{at: 15 * time.Minute, want: nil},
				{at: 16 * time.Minute, want: []Kind{Resolve}},
			},
		},
		{
			name: "one resolved and one expired: outcome expired",
			steps: []step{
				{at: 0, sig: crash, want: []Kind{Open}},
				{at: 5 * time.Minute, sig: mismatch, want: []Kind{Join}},
				{at: 6 * time.Minute, sig: resolved(mismatch), want: []Kind{End}},
				{at: 10 * time.Minute, want: []Kind{Resolve}},
			},
			check: func(t *testing.T, w *world) {
				if o := w.get(1).Outcome; o != OutcomeExpired {
					t.Errorf("outcome %q, want expired", o)
				}
			},
		},
		{
			name: "two members expire in one Tick: an end, then the resolve",
			steps: []step{
				{at: 0, sig: crash, want: []Kind{Open}},
				{at: time.Minute, sig: mismatch, want: []Kind{Join}},
				{at: 11 * time.Minute, want: []Kind{End, Resolve}},
			},
		},
		{
			name: "a resolved Signal for an ended member is dropped",
			steps: []step{
				{at: 0, sig: crash, want: []Kind{Open}},
				{at: time.Minute, sig: resolved(crash), want: []Kind{Resolve}},
				{at: 2 * time.Minute, sig: resolved(crash), want: []Kind{Drop}},
			},
		},
		{
			name: "a late firing notice for a resolved episode is dropped",
			steps: []step{
				{at: 0, sig: crash, want: []Kind{Open}},
				{at: time.Minute, sig: resolved(crash), want: []Kind{Resolve}},
				{at: 2 * time.Minute, sig: crash, want: []Kind{Drop}},
			},
		},
		{
			name: "a new Signal reopens a resolved Incident with its history",
			steps: []step{
				{at: 0, sig: crash, want: []Kind{Open}},
				{at: time.Minute, sig: resolved(crash), want: []Kind{Resolve}},
				{at: time.Hour, sig: crashAgain, want: []Kind{Reopen}},
			},
			check: func(t *testing.T, w *world) {
				if len(w.incs) != 1 {
					t.Fatalf("%d Incidents, want 1", len(w.incs))
				}
				inc := w.get(1)
				if inc.State != StateOpen || inc.Outcome != "" || inc.TicketRef != "t1" || !inc.OpenedAt.Equal(t0) {
					t.Errorf("reopened: state %q outcome %q ticket %q opened %v", inc.State, inc.Outcome, inc.TicketRef, inc.OpenedAt)
				}
				if len(inc.Members) != 2 || inc.Members[0].End != OutcomeResolved || inc.Members[1].End != "" {
					t.Errorf("members %+v, want the resolved episode and the new one", inc.Members)
				}
			},
		},
		{
			name: "a firing notice for an expired episode reopens and revives it",
			steps: []step{
				{at: 0, sig: crash, want: []Kind{Open}},
				{at: 10 * time.Minute, want: []Kind{Resolve}},
				{at: 11 * time.Minute, sig: crash, want: []Kind{Reopen}},
				{at: 20 * time.Minute, want: nil},
			},
			check: func(t *testing.T, w *world) {
				inc := w.get(1)
				if inc.State != StateOpen || len(inc.Members) != 1 || inc.Members[0].End != "" {
					t.Errorf("state %q members %+v, want open with the one live member", inc.State, inc.Members)
				}
			},
		},
		{
			name: "the outcome counts only the members that ended since the reopen",
			steps: []step{
				{at: 0, sig: crash, want: []Kind{Open}},
				{at: 10 * time.Minute, want: []Kind{Resolve}},
				{at: time.Hour, sig: mismatch, want: []Kind{Reopen}},
				{at: time.Hour + time.Minute, sig: resolved(mismatch), want: []Kind{Resolve}},
			},
			check: func(t *testing.T, w *world) {
				if o := w.get(1).Outcome; o != OutcomeResolved {
					t.Errorf("outcome %q, want resolved", o)
				}
			},
		},
		{
			name: "24 hours with no activity closes a resolved Incident",
			steps: []step{
				{at: 0, sig: crash, want: []Kind{Open}},
				{at: time.Minute, sig: resolved(crash), want: []Kind{Resolve}},
				{at: 24*time.Hour + time.Minute - time.Second, want: nil},
				{at: 24*time.Hour + time.Minute, want: []Kind{Close}},
				{at: 25 * time.Hour, want: nil},
			},
			check: func(t *testing.T, w *world) {
				if s := w.get(1).State; s != StateClosed {
					t.Errorf("state %q, want closed", s)
				}
			},
		},
		{
			name: "an expiry is activity: the idle close counts from it",
			steps: []step{
				{at: 0, sig: crash, want: []Kind{Open}},
				{at: 10 * time.Minute, want: []Kind{Resolve}},
				{at: 24*time.Hour + 9*time.Minute, want: nil},
				{at: 24*time.Hour + 10*time.Minute, want: []Kind{Close}},
			},
		},
		{
			name: "a Signal after the idle close opens a recurrence",
			steps: []step{
				{at: 0, sig: crash, want: []Kind{Open}},
				{at: time.Minute, sig: resolved(crash), want: []Kind{Resolve}},
				{at: 25 * time.Hour, want: []Kind{Close}},
				{at: 26 * time.Hour, sig: crashAgain, want: []Kind{Open}},
			},
			check: func(t *testing.T, w *world) {
				inc := w.get(2)
				if inc.RecurrenceOf != 1 || inc.State != StateOpen || len(inc.Members) != 1 || inc.TicketRef != "t2" {
					t.Errorf("recurrence %+v, want a new open Incident linked to 1", inc)
				}
			},
		},
		{
			name: "a ticket closed by a human closes an open Incident, and a later Signal recurs",
			steps: []step{
				{at: 0, sig: crash, want: []Kind{Open}},
				{at: time.Minute, closeID: 1, want: []Kind{Close}},
				{at: 2 * time.Minute, sig: mismatch, want: []Kind{Open}},
				{at: 3 * time.Minute, sig: resolved(crash), want: []Kind{Drop}},
				{at: 15 * time.Minute, want: []Kind{Resolve}},
			},
			check: func(t *testing.T, w *world) {
				if s := w.get(1).State; s != StateClosed {
					t.Errorf("Incident 1 state %q, want closed", s)
				}
				if r := w.get(2).RecurrenceOf; r != 1 {
					t.Errorf("Incident 2 recurrence of %d, want 1", r)
				}
			},
		},
		{
			name: "a recurrence links the latest closed Incident",
			steps: []step{
				{at: 0, sig: crash, want: []Kind{Open}},
				{at: time.Minute, closeID: 1, want: []Kind{Close}},
				{at: 2 * time.Minute, sig: mismatch, want: []Kind{Open}},
				{at: 3 * time.Minute, closeID: 2, want: []Kind{Close}},
				{at: 4 * time.Minute, sig: crashAgain, want: []Kind{Open}},
			},
			check: func(t *testing.T, w *world) {
				if r := w.get(3).RecurrenceOf; r != 2 {
					t.Errorf("Incident 3 recurrence of %d, want 2", r)
				}
			},
		},
		{
			name: "no Target: keyed by source and name through resolve, reopen, close, and recurrence",
			steps: []step{
				{at: 0, sig: podOnly, want: []Kind{Open}},
				{at: 10 * time.Minute, want: []Kind{Resolve}},
				{at: 20 * time.Minute, sig: sig("am/ppp", "PodNotReady", nil, t0.Add(20*time.Minute)), want: []Kind{Reopen}},
				{at: 21 * time.Minute, sig: resolved(sig("am/ppp", "PodNotReady", nil, t0.Add(20*time.Minute))), want: []Kind{Resolve}},
				{at: 22 * time.Minute, sig: sig("am/qqq", "KubePodNotReady", nil, t0.Add(22*time.Minute)), want: []Kind{Open}},
				{at: 25*time.Hour + 21*time.Minute, want: []Kind{Close, Resolve}},
				{at: 25*time.Hour + 22*time.Minute, sig: sig("am/ppp", "PodNotReady", nil, t0.Add(25*time.Hour)), want: []Kind{Open}},
			},
			check: func(t *testing.T, w *world) {
				first := w.get(1)
				if first.Target != nil || first.Key != "am/PodNotReady" || first.State != StateClosed || first.Outcome != OutcomeResolved {
					t.Errorf("first: %+v", first)
				}
				if other := w.get(2); other.Key != "am/KubePodNotReady" || other.State != StateResolved || other.Outcome != OutcomeExpired {
					t.Errorf("other name: %+v", other)
				}
				if rec := w.get(3); rec.Key != "am/PodNotReady" || rec.RecurrenceOf != 1 {
					t.Errorf("recurrence: %+v", rec)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := &world{t: t}
			w.play(tt.steps)
			if tt.check != nil {
				tt.check(t, w)
			}
		})
	}
}

func TestDecisionsRecordTheClock(t *testing.T) {
	w := &world{t: t}
	crash := sig("am/aaa", "KubePodCrashLooping", orders, t0)
	w.play([]step{
		{at: 0, sig: crash, want: []Kind{Open}},
		{at: 3 * time.Minute, sig: resolved(crash), want: []Kind{Resolve}},
	})
	inc := w.get(1)
	if m := inc.Members[0]; !m.EndedAt.Equal(t0.Add(3*time.Minute)) || !inc.UpdatedAt.Equal(m.EndedAt) {
		t.Errorf("ended %v updated %v, want both at 3m", m.EndedAt, inc.UpdatedAt)
	}
	if d := TicketClosed(inc, t0.Add(time.Hour)); d.Incident.State != StateClosed || !d.Incident.UpdatedAt.Equal(t0.Add(time.Hour)) || d.Reason == "" {
		t.Errorf("human close: %+v", d)
	}
}

func TestSeverityIsTheHighestOpenMember(t *testing.T) {
	w := &world{t: t}
	warn := withSeverity(sig("am/aaa", "A", orders, t0), signal.Warning)
	crit := withSeverity(sig("am/bbb", "B", orders, t0), signal.Critical)
	info := withSeverity(sig("am/ccc", "C", orders, t0), signal.Info)
	sevs := []signal.Severity{}
	for _, s := range []signal.Signal{warn, crit, info, resolved(crit), resolved(warn), resolved(info)} {
		w.apply(Triage(w.incs, s, t0))
		sevs = append(sevs, w.get(1).Severity())
	}
	want := []signal.Severity{signal.Warning, signal.Critical, signal.Critical, signal.Warning, signal.Info, ""}
	if !slices.Equal(sevs, want) {
		t.Errorf("severities %v, want %v", sevs, want)
	}
}
