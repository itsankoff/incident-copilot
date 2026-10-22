package triage

import (
	"testing"
	"time"

	"github.com/itsankoff/incident-copilot/internal/signal"
)

var (
	t0 = time.Date(2026, 10, 21, 3, 0, 0, 0, time.UTC)

	orders    = &signal.Target{Platform: "kubernetes", Location: "kind", Namespace: "demo", Kind: "Deployment", Name: "orders"}
	inventory = &signal.Target{Platform: "kubernetes", Location: "kind", Namespace: "demo", Kind: "Deployment", Name: "inventory"}
)

func sig(key, name string, target *signal.Target, startsAt time.Time) signal.Signal {
	return signal.Signal{
		Source: signal.SourceRef{Kind: "alertmanager", Instance: "am"},
		Key:    key, Name: name, Status: signal.Firing, Severity: signal.Warning,
		StartsAt: startsAt, Target: target,
	}
}

func member(s signal.Signal, seen time.Time) Member {
	return Member{Key: s.Key, Source: s.Source, Name: s.Name, Severity: s.Severity, StartsAt: s.StartsAt, FirstSeen: seen, LastSeen: seen}
}

func TestTriage(t *testing.T) {
	now := t0.Add(2 * time.Minute)

	crash := sig("am/aaa", "KubePodCrashLooping", orders, t0)
	ordersInc := Incident{ID: 1, Target: orders, OpenedAt: t0, Members: []Member{member(crash, t0)}, TicketRef: "41"}

	podOnly := sig("am/ppp", "PodNotReady", nil, t0)
	podOnlyInc := Incident{ID: 2, Key: "am/ppp", OpenedAt: t0, Members: []Member{member(podOnly, t0)}}

	tests := []struct {
		name    string
		open    []Incident
		in      signal.Signal
		kind    Kind
		id      int64
		members int
	}{
		{"first Signal for a Target opens", nil, crash, Open, 0, 1},
		{"different name, same Target joins", []Incident{ordersInc}, sig("am/bbb", "KubeDeploymentReplicasMismatch", orders, t0), Join, 1, 2},
		{"other source, same Target joins", []Incident{ordersInc}, sig("detector:CrashLoopBackOff/x", "CrashLoopBackOff", orders, t0), Join, 1, 2},
		{"same key, new startsAt is a new episode and joins", []Incident{ordersInc}, sig("am/aaa", "KubePodCrashLooping", orders, t0.Add(time.Minute)), Join, 1, 2},
		{"same key and startsAt is a repeat notice", []Incident{ordersInc}, crash, Repeat, 1, 1},
		{"different Target opens a second Incident", []Incident{ordersInc}, sig("am/ccc", "KubePodCrashLooping", inventory, t0), Open, 0, 1},
		{"no Target opens keyed by key", []Incident{ordersInc}, podOnly, Open, 0, 1},
		{"no Target joins by key", []Incident{ordersInc, podOnlyInc}, sig("am/ppp", "PodNotReady", nil, t0.Add(time.Minute)), Join, 2, 2},
		{"no Target never joins a Target's Incident", []Incident{ordersInc}, sig("am/aaa", "KubePodCrashLooping", nil, t0), Open, 0, 1},
		{"no Target, different key opens", []Incident{podOnlyInc}, sig("am/qqq", "PodNotReady", nil, t0), Open, 0, 1},
		{"resolved is dropped (resolve is #45)", []Incident{ordersInc}, func() signal.Signal { s := crash; s.Status = signal.Resolved; return s }(), Drop, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := Triage(tt.open, tt.in, now)
			if d.Kind != tt.kind {
				t.Fatalf("kind %q, want %q", d.Kind, tt.kind)
			}
			if d.Incident.ID != tt.id {
				t.Errorf("Incident %d, want %d", d.Incident.ID, tt.id)
			}
			if len(d.Incident.Members) != tt.members {
				t.Errorf("%d members, want %d", len(d.Incident.Members), tt.members)
			}
		})
	}
}

func TestTriageOpenRecordsTargetOrKey(t *testing.T) {
	now := t0.Add(time.Minute)

	d := Triage(nil, sig("am/aaa", "KubePodCrashLooping", orders, t0), now)
	if d.Incident.Target == nil || *d.Incident.Target != *orders || d.Incident.Key != "" {
		t.Errorf("Target Incident: target %v, key %q", d.Incident.Target, d.Incident.Key)
	}
	if !d.Incident.OpenedAt.Equal(now) || !d.Member.FirstSeen.Equal(now) || !d.Member.LastSeen.Equal(now) {
		t.Errorf("times not from the clock: %+v", d)
	}

	d = Triage(nil, sig("am/ppp", "PodNotReady", nil, t0), now)
	if d.Incident.Target != nil || d.Incident.Key != "am/ppp" {
		t.Errorf("no-Target Incident: target %v, key %q", d.Incident.Target, d.Incident.Key)
	}
}

func TestTriageRepeatUpdatesLastSeenOnly(t *testing.T) {
	crash := sig("am/aaa", "KubePodCrashLooping", orders, t0)
	inc := Incident{ID: 1, Target: orders, OpenedAt: t0, Members: []Member{member(crash, t0)}}
	now := t0.Add(5 * time.Minute)

	d := Triage([]Incident{inc}, crash, now)
	got := d.Incident.Members[0]
	if !got.LastSeen.Equal(now) || !got.FirstSeen.Equal(t0) || !d.Incident.OpenedAt.Equal(t0) {
		t.Errorf("repeat: first %v last %v opened %v", got.FirstSeen, got.LastSeen, d.Incident.OpenedAt)
	}
	if !inc.Members[0].LastSeen.Equal(t0) {
		t.Error("Triage modified its input")
	}
}

func TestTriageResolvedForUnknownKeyIsDropped(t *testing.T) {
	crash := sig("am/aaa", "KubePodCrashLooping", orders, t0)
	open := []Incident{{ID: 1, Target: orders, OpenedAt: t0, Members: []Member{member(crash, t0)}}}

	unknown := sig("am/zzz", "KubePodCrashLooping", orders, t0)
	unknown.Status = signal.Resolved
	if d := Triage(open, unknown, t0); d.Kind != Drop || d.Reason != "resolved Signal for an unknown key" {
		t.Errorf("unknown key: %s %q, want a drop for an unknown key", d.Kind, d.Reason)
	}
	known := crash
	known.Status = signal.Resolved
	if d := Triage(open, known, t0); d.Kind != Drop || d.Reason == "resolved Signal for an unknown key" {
		t.Errorf("known key: %s %q, want a drop that is not for an unknown key", d.Kind, d.Reason)
	}
}
