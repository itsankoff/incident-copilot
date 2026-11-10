package controlplane

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/itsankoff/incident-copilot/internal/audit"
	"github.com/itsankoff/incident-copilot/internal/llm"
	"github.com/itsankoff/incident-copilot/internal/triage"
)

func resolvedAlert(a map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range a {
		out[k] = v
	}
	out["status"] = "resolved"
	return out
}

func startingAt(a map[string]any, at time.Time) map[string]any {
	out := map[string]any{}
	for k, v := range a {
		out[k] = v
	}
	out["startsAt"] = at.Format(time.RFC3339)
	return out
}

func withSeverity(a map[string]any, sev string) map[string]any {
	l := map[string]string{}
	for k, v := range a["labels"].(map[string]string) {
		l[k] = v
	}
	l["severity"] = sev
	out := map[string]any{}
	for k, v := range a {
		out[k] = v
	}
	out["labels"] = l
	return out
}

func (h *harness) sweep() {
	h.t.Helper()
	if err := h.cp.Sweep(context.Background()); err != nil {
		h.t.Fatal(err)
	}
}

// decisions returns the kinds of the audited triage decisions, in order.
func decisions(t *testing.T, log []audit.Entry) []string {
	t.Helper()
	var out []string
	for _, e := range ofType(log, "triage.decision") {
		var d struct{ Kind string }
		if err := json.Unmarshal(e.Data, &d); err != nil {
			t.Fatal(err)
		}
		out = append(out, d.Kind)
	}
	return out
}

func (f *fakeTickets) commentsOn(ref string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.comments {
		if c.ref == ref {
			out = append(out, c.body)
		}
	}
	return out
}

func TestResolvedSignalsResolveTheIncident(t *testing.T) {
	h := newHarness(t, t.TempDir(), &fakeTickets{})
	crash := alert("KubePodCrashLooping", "aaa", orders)
	h.send(tokenProd, crash)
	h.now = h.now.Add(time.Minute)
	h.send(tokenProd, resolvedAlert(crash))

	got := h.tickets.commentsOn("1")
	if len(got) != 1 || !strings.Contains(got[0], "Resolved") || !strings.Contains(got[0], "outcome: **resolved**") {
		t.Fatalf("comments %q, want one saying resolved", got)
	}
	if len(h.tickets.closes) != 0 {
		t.Error("the ticket of a resolved Incident was closed")
	}
	if d := decisions(t, h.auditLog()); strings.Join(d, ",") != "open,resolve" {
		t.Errorf("decisions %v", d)
	}
}

func TestSilentSignalExpires(t *testing.T) {
	h := newHarness(t, t.TempDir(), &fakeTickets{})
	h.send(tokenProd, alert("KubePodCrashLooping", "aaa", orders))
	h.now = h.now.Add(9 * time.Minute)
	h.sweep()
	if c := h.tickets.commentsOn("1"); len(c) != 0 {
		t.Fatalf("comments before 10 minutes: %q", c)
	}
	h.now = h.now.Add(time.Minute)
	h.sweep()

	got := h.tickets.commentsOn("1")
	if len(got) != 1 || !strings.Contains(got[0], "expired") || !strings.Contains(got[0], "outcome: **expired**") {
		t.Fatalf("comments %q, want one saying resolved as expired", got)
	}
	if d := decisions(t, h.auditLog()); strings.Join(d, ",") != "open,resolve" {
		t.Errorf("decisions %v", d)
	}
}

func TestSignalReopensResolvedIncidentOnItsTicket(t *testing.T) {
	h := newHarness(t, t.TempDir(), &fakeTickets{})
	crash := alert("KubePodCrashLooping", "aaa", orders)
	h.send(tokenProd, crash)
	h.send(tokenProd, resolvedAlert(crash))
	h.now = h.now.Add(time.Hour)
	h.send(tokenProd, startingAt(crash, t0.Add(time.Hour)))

	if len(h.tickets.created) != 1 {
		t.Fatalf("%d tickets, want 1", len(h.tickets.created))
	}
	got := h.tickets.commentsOn("1")
	if len(got) != 2 || !strings.Contains(got[1], "Reopened") || !strings.Contains(got[1], "KubePodCrashLooping") {
		t.Fatalf("comments %q, want the resolve, then a reopen", got)
	}
	incs, err := h.store.Incidents(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(incs) != 1 || incs[0].State != triage.StateOpen || len(incs[0].Members) != 2 {
		t.Errorf("Incidents %+v, want one open with both episodes", incs)
	}
}

func TestIdleResolvedIncidentIsClosedWithAComment(t *testing.T) {
	h := newHarness(t, t.TempDir(), &fakeTickets{})
	crash := alert("KubePodCrashLooping", "aaa", orders)
	h.send(tokenProd, crash)
	h.send(tokenProd, resolvedAlert(crash))
	h.now = h.now.Add(24*time.Hour - time.Second)
	h.sweep()
	if len(h.tickets.closes) != 0 {
		t.Fatal("closed before 24 hours")
	}
	h.now = h.now.Add(time.Second)
	h.sweep()

	if len(h.tickets.closes) != 1 || h.tickets.closes[0] != "1" {
		t.Fatalf("closes %v, want ticket 1", h.tickets.closes)
	}
	got := h.tickets.commentsOn("1")
	if len(got) != 2 || !strings.Contains(got[1], "no activity for 24 hours") {
		t.Fatalf("comments %q, want a closing comment", got)
	}
	log := h.auditLog()
	if d := decisions(t, log); strings.Join(d, ",") != "open,resolve,close" {
		t.Errorf("decisions %v", d)
	}
	if len(ofType(log, "ticket.closed")) != 1 {
		t.Error("ticket close not audited")
	}
}

func TestHumanClosedTicketIsPickedUpAndASignalRecurs(t *testing.T) {
	for _, via := range []string{"sweep", "signal"} {
		t.Run(via, func(t *testing.T) {
			h := newHarness(t, t.TempDir(), &fakeTickets{})
			h.send(tokenProd, alert("KubePodCrashLooping", "aaa", orders))
			h.tickets.closeByHuman("1", "not_planned")
			h.now = h.now.Add(time.Minute)
			if via == "sweep" {
				h.sweep()
				incs, err := h.store.Incidents(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				if len(incs) != 1 || incs[0].State != triage.StateClosed {
					t.Fatalf("Incidents %+v, want the one, closed", incs)
				}
			}
			h.send(tokenProd, alert("KubeDeploymentReplicasMismatch", "bbb", orders))

			if len(h.tickets.created) != 2 {
				t.Fatalf("%d tickets, want a recurrence ticket", len(h.tickets.created))
			}
			if b := h.tickets.created[1].body; !strings.Contains(b, "Recurrence of") || !strings.Contains(b, "#1") {
				t.Errorf("recurrence ticket does not link the old one:\n%s", b)
			}
			if c := h.tickets.commentsOn("1"); len(c) != 0 {
				t.Errorf("comments on the closed ticket: %q", c)
			}
			log := h.auditLog()
			if d := decisions(t, log); strings.Join(d, ",") != "open,close,open" {
				t.Errorf("decisions %v", d)
			}
			closeEntry := ofType(log, "triage.decision")[1]
			if closeEntry.Incident != 1 || !strings.Contains(string(closeEntry.Data), "closed by a human") || !strings.Contains(string(closeEntry.Data), "not_planned") {
				t.Errorf("close decision %s", closeEntry.Data)
			}
			if open := ofType(log, "triage.decision")[2]; open.Incident != 2 || !strings.Contains(string(open.Data), `"recurrenceOf":1`) {
				t.Errorf("recurrence decision %d %s", open.Incident, open.Data)
			}
		})
	}
}

// blockingLLM is a long-running investigation step: it blocks until its
// context is cancelled, after closing started.
type blockingLLM struct{ started chan struct{} }

func (b *blockingLLM) Complete(ctx context.Context, _ llm.Request) (llm.Response, error) {
	close(b.started)
	<-ctx.Done()
	return llm.Response{}, ctx.Err()
}

func TestClosingTheTicketCancelsInFlightWork(t *testing.T) {
	model := &blockingLLM{started: make(chan struct{})}
	h := startRCA(t, model, &fakeLink{}, testBudget)
	h.send(tokenProd, alert("KubePodCrashLooping", "aaa", orders))
	select {
	case <-model.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the investigation never asked the LLM")
	}

	h.tickets.closeByHuman("1", "completed")
	h.sweep()
	h.cp.Wait() // returns only once the cancelled investigation has stopped

	log := h.auditLog()
	cancelled := ofType(log, "investigation.cancelled")
	if len(cancelled) != 1 || cancelled[0].Incident != 1 || !strings.Contains(string(cancelled[0].Data), "closed by a human") {
		t.Fatalf("investigation.cancelled %+v, want one saying why", cancelled)
	}
	if n := len(ofType(log, "investigation.finished")); n != 0 {
		t.Errorf("%d investigation.finished after the cancel", n)
	}
	if c := h.tickets.commentsOn("1"); len(c) != 0 {
		t.Errorf("comments on the closed ticket: %q", c)
	}
}

func TestTicketSeverityFollowsTheHighestFiringSignal(t *testing.T) {
	h := newHarness(t, t.TempDir(), &fakeTickets{})
	crit := alert("KubePodCrashLooping", "aaa", orders)
	warn := withSeverity(alert("KubeDeploymentReplicasMismatch", "bbb", orders), "warning")
	h.send(tokenProd, warn)
	h.send(tokenProd, crit)
	h.send(tokenProd, resolvedAlert(crit))
	h.send(tokenProd, resolvedAlert(warn))

	if tt := h.tickets.created[0].title; !strings.HasPrefix(tt, "[warning] ") {
		t.Errorf("first title %q, want the warning", tt)
	}
	var titles []string
	for _, r := range h.tickets.retitled {
		titles = append(titles, r.title)
	}
	if len(titles) != 2 || !strings.HasPrefix(titles[0], "[critical] ") || !strings.HasPrefix(titles[1], "[warning] ") {
		t.Fatalf("retitled %q, want critical, then warning, then nothing once all ended", titles)
	}
	if n := len(ofType(h.auditLog(), "ticket.retitled")); n != 2 {
		t.Errorf("%d ticket.retitled audited, want 2", n)
	}
}
