package controlplane

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/itsankoff/incident-copilot/internal/alertmanager"
	"github.com/itsankoff/incident-copilot/internal/audit"
	"github.com/itsankoff/incident-copilot/internal/signal"
	"github.com/itsankoff/incident-copilot/internal/store"
)

var t0 = time.Date(2026, 10, 21, 3, 0, 0, 0, time.UTC)

const (
	tokenProd    = "prod-token"
	tokenStaging = "staging-token"
	secretNote   = "SECRET-ANNOTATION-TEXT"
)

type ticket struct{ ref, title, body string }
type comment struct{ ref, body string }

// fakeTickets records what the Control plane writes to the ticketing system.
type fakeTickets struct {
	mu       sync.Mutex
	created  []ticket
	comments []comment
	fail     bool
}

func (f *fakeTickets) Create(_ context.Context, title, body string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return "", errors.New("ticketing down")
	}
	ref := fmt.Sprint(len(f.created) + 1)
	f.created = append(f.created, ticket{ref, title, body})
	return ref, nil
}

func (f *fakeTickets) Comment(_ context.Context, ref, body string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.comments = append(f.comments, comment{ref, body})
	return nil
}

type harness struct {
	t       *testing.T
	dir     string
	url     string
	tickets *fakeTickets
	now     time.Time
	close   func()
}

// newHarness starts a Control plane whose state and audit log live in dir.
func newHarness(t *testing.T, dir string, tickets *fakeTickets) *harness {
	t.Helper()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	log, err := audit.Open(filepath.Join(dir, "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, dir: dir, tickets: tickets, now: t0.Add(time.Minute)}
	am := alertmanager.New([]signal.Source{
		{Kind: "alertmanager", Instance: "prod-am", Token: tokenProd, Location: "prod", Mapping: signal.KubePrometheus},
		{Kind: "alertmanager", Instance: "staging-am", Token: tokenStaging, Location: "staging", Mapping: signal.KubePrometheus},
	})
	cp := New(am, st, tickets, log, func() time.Time { return h.now })
	srv := httptest.NewServer(cp.Handler())
	h.url = srv.URL
	h.close = func() {
		srv.Close()
		_ = st.Close()
		_ = log.Close()
	}
	t.Cleanup(h.close)
	return h
}

func (h *harness) post(auth, body string) int {
	h.t.Helper()
	req, err := http.NewRequest(http.MethodPost, h.url+"/webhook/alertmanager", strings.NewReader(body))
	if err != nil {
		h.t.Fatal(err)
	}
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

// send posts alerts with token and requires a 200.
func (h *harness) send(token string, alerts ...map[string]any) {
	h.t.Helper()
	if code := h.post("Bearer "+token, payload(alerts...)); code != http.StatusOK {
		h.t.Fatalf("status %d, want 200", code)
	}
}

// auditLog verifies the chain and returns its entries.
func (h *harness) auditLog() []audit.Entry {
	h.t.Helper()
	b, err := os.ReadFile(filepath.Join(h.dir, "audit.jsonl"))
	if err != nil {
		h.t.Fatal(err)
	}
	if err := audit.Verify(strings.NewReader(string(b))); err != nil {
		h.t.Fatalf("audit chain: %v", err)
	}
	var out []audit.Entry
	sc := bufio.NewScanner(strings.NewReader(string(b)))
	for sc.Scan() {
		var e audit.Entry
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			h.t.Fatal(err)
		}
		out = append(out, e)
	}
	return out
}

func ofType(entries []audit.Entry, typ string) []audit.Entry {
	var out []audit.Entry
	for _, e := range entries {
		if e.Type == typ {
			out = append(out, e)
		}
	}
	return out
}

func alert(name, fingerprint string, labels map[string]string) map[string]any {
	l := map[string]string{"alertname": name, "severity": "critical"}
	for k, v := range labels {
		l[k] = v
	}
	return map[string]any{
		"status":       "firing",
		"labels":       l,
		"annotations":  map[string]string{"description": secretNote},
		"startsAt":     t0.Format(time.RFC3339),
		"endsAt":       "0001-01-01T00:00:00Z",
		"generatorURL": "http://prometheus/graph?g0.expr=" + name,
		"fingerprint":  fingerprint,
	}
}

func payload(alerts ...map[string]any) string {
	b, _ := json.Marshal(map[string]any{
		"version": "4", "status": "firing", "receiver": "incident-copilot",
		"groupKey": "{}:{}", "truncatedAlerts": 0, "alerts": alerts,
	})
	return string(b)
}

var orders = map[string]string{"namespace": "demo", "deployment": "orders", "pod": "orders-7d9f8c6b5-x2k4p"}

func TestAlertOpensOneTicket(t *testing.T) {
	h := newHarness(t, t.TempDir(), &fakeTickets{})
	h.send(tokenProd, alert("KubePodCrashLooping", "aaa", orders))

	if len(h.tickets.created) != 1 {
		t.Fatalf("%d tickets, want 1", len(h.tickets.created))
	}
	tk := h.tickets.created[0]
	for _, want := range []string{"KubePodCrashLooping", "prod/demo/Deployment/orders"} {
		if !strings.Contains(tk.title, want) {
			t.Errorf("title %q does not name %q", tk.title, want)
		}
	}
	for _, want := range []string{"KubePodCrashLooping", "critical", t0.Format(time.RFC3339), "http://prometheus/graph?g0.expr=KubePodCrashLooping"} {
		if !strings.Contains(tk.body, want) {
			t.Errorf("body does not list %q:\n%s", want, tk.body)
		}
	}
	if strings.Contains(tk.title+tk.body, secretNote) {
		t.Error("ticket contains Alert annotations")
	}

	log := h.auditLog()
	received, decided := ofType(log, "signal.received"), ofType(log, "triage.decision")
	if len(received) != 1 || len(decided) != 1 {
		t.Fatalf("audit: %d signal.received, %d triage.decision, want 1 each", len(received), len(decided))
	}
	if received[0].Incident == 0 || decided[0].Incident != received[0].Incident {
		t.Errorf("audit Incident ids: received %d, decision %d", received[0].Incident, decided[0].Incident)
	}
	var sig signal.Signal
	if err := json.Unmarshal(received[0].Data, &sig); err != nil {
		t.Fatal(err)
	}
	if sig.Key != "prod-am/aaa" || sig.Source.Instance != "prod-am" || !sig.ReceivedAt.Equal(h.now) {
		t.Errorf("audited Signal: key %q source %v receivedAt %v", sig.Key, sig.Source, sig.ReceivedAt)
	}
	if !strings.Contains(string(decided[0].Data), `"open"`) {
		t.Errorf("decision is not open: %s", decided[0].Data)
	}
	if len(ofType(log, "ticket.created")) != 1 {
		t.Error("ticket creation not audited")
	}
}

func TestSameTargetJoinsIncident(t *testing.T) {
	h := newHarness(t, t.TempDir(), &fakeTickets{})
	h.send(tokenProd, alert("KubePodCrashLooping", "aaa", orders))
	h.send(tokenProd, alert("KubeDeploymentReplicasMismatch", "bbb", map[string]string{"namespace": "demo", "deployment": "orders"}))

	if len(h.tickets.created) != 1 {
		t.Fatalf("%d tickets, want 1", len(h.tickets.created))
	}
	if len(h.tickets.comments) != 1 || h.tickets.comments[0].ref != h.tickets.created[0].ref {
		t.Fatalf("comments %+v, want one on ticket %s", h.tickets.comments, h.tickets.created[0].ref)
	}
	if c := h.tickets.comments[0].body; !strings.Contains(c, "KubeDeploymentReplicasMismatch") || strings.Contains(c, secretNote) {
		t.Errorf("join comment: %q", c)
	}
	decided := ofType(h.auditLog(), "triage.decision")
	if len(decided) != 2 || decided[1].Incident != decided[0].Incident || !strings.Contains(string(decided[1].Data), `"join"`) {
		t.Errorf("audit decisions: %+v", decided)
	}
}

func TestRepeatNoticeChangesNoTicket(t *testing.T) {
	h := newHarness(t, t.TempDir(), &fakeTickets{})
	a := alert("KubePodCrashLooping", "aaa", orders)
	h.send(tokenProd, a)
	h.now = h.now.Add(5 * time.Minute)
	h.send(tokenProd, a)

	if len(h.tickets.created) != 1 || len(h.tickets.comments) != 0 {
		t.Errorf("%d tickets, %d comments, want 1 and 0", len(h.tickets.created), len(h.tickets.comments))
	}
	if d := ofType(h.auditLog(), "triage.decision"); len(d) != 2 || !strings.Contains(string(d[1].Data), `"repeat"`) {
		t.Errorf("audit decisions: %+v", d)
	}
}

func TestDifferentTargetsOpenSeparateIncidents(t *testing.T) {
	h := newHarness(t, t.TempDir(), &fakeTickets{})
	h.send(tokenProd,
		alert("KubePodCrashLooping", "aaa", orders),
		alert("KubePodCrashLooping", "ccc", map[string]string{"namespace": "demo", "deployment": "inventory"}),
	)
	// Same namespace and Deployment, but another Signal source's Location.
	h.send(tokenStaging, alert("KubePodCrashLooping", "aaa", orders))

	if len(h.tickets.created) != 3 || len(h.tickets.comments) != 0 {
		t.Fatalf("%d tickets, %d comments, want 3 and 0", len(h.tickets.created), len(h.tickets.comments))
	}
	for i, want := range []string{"prod/demo/Deployment/orders", "prod/demo/Deployment/inventory", "staging/demo/Deployment/orders"} {
		if !strings.Contains(h.tickets.created[i].title, want) {
			t.Errorf("ticket %d title %q, want %q", i, h.tickets.created[i].title, want)
		}
	}
}

func TestPodOnlyAlertHasNoTarget(t *testing.T) {
	h := newHarness(t, t.TempDir(), &fakeTickets{})
	h.send(tokenProd, alert("KubePodNotReady", "ppp", map[string]string{"namespace": "demo", "pod": "orders-7d9f8c6b5-x2k4p"}))

	if len(h.tickets.created) != 1 {
		t.Fatalf("%d tickets, want 1", len(h.tickets.created))
	}
	tk := h.tickets.created[0]
	if !strings.Contains(tk.title, "prod-am/ppp") || !strings.Contains(tk.title, "KubePodNotReady") {
		t.Errorf("title %q does not name the key and Signal", tk.title)
	}
	if strings.Contains(tk.title+tk.body, "Deployment") {
		t.Errorf("a Target was derived from a pod name: %q\n%s", tk.title, tk.body)
	}
	var sig signal.Signal
	if err := json.Unmarshal(ofType(h.auditLog(), "signal.received")[0].Data, &sig); err != nil {
		t.Fatal(err)
	}
	if sig.Target != nil {
		t.Errorf("Signal has Target %+v, want none", *sig.Target)
	}
}

func TestUnauthenticatedRequestIsRejected(t *testing.T) {
	tests := []struct{ name, auth string }{
		{"missing", ""},
		{"wrong token", "Bearer not-the-token"},
		{"empty token", "Bearer "},
		{"wrong scheme", "Basic " + tokenProd},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, t.TempDir(), &fakeTickets{})
			if code := h.post(tt.auth, payload(alert("KubePodCrashLooping", "aaa", orders))); code != http.StatusUnauthorized {
				t.Fatalf("status %d, want 401", code)
			}
			if len(h.tickets.created) != 0 {
				t.Error("ticket created for an unauthenticated request")
			}
			log := h.auditLog()
			if len(log) != 1 || log[0].Type != "auth.rejected" {
				t.Fatalf("audit %+v, want one auth.rejected", log)
			}
			if strings.Contains(string(log[0].Data), "not-the-token") {
				t.Error("audit records the presented token")
			}
		})
	}
}

func TestStateSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	tickets := &fakeTickets{}
	h := newHarness(t, dir, tickets)
	h.send(tokenProd, alert("KubePodCrashLooping", "aaa", orders))
	h.close()

	h = newHarness(t, dir, tickets)
	h.send(tokenProd, alert("KubeDeploymentReplicasMismatch", "bbb", orders))
	if len(tickets.created) != 1 || len(tickets.comments) != 1 {
		t.Errorf("after restart: %d tickets, %d comments, want 1 and 1", len(tickets.created), len(tickets.comments))
	}
	h.auditLog() // still one valid chain
}

func TestTicketFailureIsRetried(t *testing.T) {
	tickets := &fakeTickets{fail: true}
	h := newHarness(t, t.TempDir(), tickets)
	a := alert("KubePodCrashLooping", "aaa", orders)
	if code := h.post("Bearer "+tokenProd, payload(a)); code != http.StatusInternalServerError {
		t.Fatalf("status %d, want 500 so Alertmanager retries", code)
	}

	tickets.fail = false
	h.send(tokenProd, a) // the retry is a repeat notice
	if len(tickets.created) != 1 {
		t.Errorf("%d tickets after retry, want 1", len(tickets.created))
	}
}

func TestBadAlertInBatchIsDropped(t *testing.T) {
	h := newHarness(t, t.TempDir(), &fakeTickets{})
	h.send(tokenProd, alert("NoFingerprint", "", orders), alert("KubePodCrashLooping", "aaa", orders))

	if len(h.tickets.created) != 1 || !strings.Contains(h.tickets.created[0].title, "KubePodCrashLooping") {
		t.Errorf("tickets %+v, want one for the good alert", h.tickets.created)
	}
	if len(ofType(h.auditLog(), "signal.dropped")) != 1 {
		t.Error("dropped alert not audited")
	}
}

func TestUnparseableBody(t *testing.T) {
	h := newHarness(t, t.TempDir(), &fakeTickets{})
	if code := h.post("Bearer "+tokenProd, "{not json"); code != http.StatusBadRequest {
		t.Errorf("status %d, want 400", code)
	}
}
