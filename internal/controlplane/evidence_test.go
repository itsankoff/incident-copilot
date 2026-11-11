package controlplane

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/itsankoff/incident-copilot/internal/audit"
	"github.com/itsankoff/incident-copilot/internal/evidence"
	"github.com/itsankoff/incident-copilot/internal/link"
	"github.com/itsankoff/incident-copilot/internal/sensor"
	"github.com/itsankoff/incident-copilot/internal/signal"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// stubTool answers with its Location's name and counts its calls.
type stubTool struct {
	location string
	mu       sync.Mutex
	calls    int
}

func (s *stubTool) Name() string { return "workload_state" }

func (s *stubTool) Run(context.Context, evidence.Query) (json.RawMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	return json.Marshal(map[string]string{"from": s.location})
}

func (s *stubTool) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

type linkHarness struct {
	t        *testing.T
	dir      string
	url      string
	clock    *clock
	hub      *link.Hub
	evidence *Evidence
}

var twoLocations = []Location{
	{Name: "ic-target", Environment: "staging", Token: "target-token"},
	{Name: "aks-prod", Environment: "production", Token: "prod-token-x"},
}

func startLink(t *testing.T) *linkHarness {
	t.Helper()
	dir := t.TempDir()
	log, err := audit.Open(filepath.Join(dir, "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = log.Close() })
	c := &clock{t: t0}
	tokens := map[string]string{}
	for _, l := range twoLocations {
		tokens[l.Name] = l.Token
	}
	hub := link.NewHub(tokens, log, c.now)
	srv := httptest.NewServer(hub.Handler())
	t.Cleanup(srv.Close)
	return &linkHarness{t, dir, srv.URL, c, hub, NewEvidence(twoLocations, hub, log, c.now)}
}

// sensorUp runs a Sensor until the returned function kills it.
func (h *linkHarness) sensorUp(location, token string, tools ...evidence.Tool) (kill func()) {
	h.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	s := sensor.New(link.NewClient(h.url, location, token), tools, slog.New(slog.NewTextHandler(io.Discard, nil)))
	s.Retry = 5 * time.Millisecond
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.Run(ctx)
	}()
	kill = func() {
		cancel()
		<-done
	}
	h.t.Cleanup(kill)
	return kill
}

func (h *linkHarness) waitOnline(location string, want bool) {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for h.hub.Online(location) != want {
		if time.Now().After(deadline) {
			h.t.Fatalf("Online(%s) never became %v", location, want)
		}
		time.Sleep(time.Millisecond)
	}
}

func (h *linkHarness) entries(typ string) []audit.Entry {
	h.t.Helper()
	raw, err := os.ReadFile(filepath.Join(h.dir, "audit.jsonl"))
	if err != nil {
		h.t.Fatal(err)
	}
	if err := audit.Verify(bytes.NewReader(raw)); err != nil {
		h.t.Fatalf("audit chain: %v", err)
	}
	var out []audit.Entry
	for _, line := range bytes.Split(bytes.TrimSpace(raw), []byte("\n")) {
		var e audit.Entry
		if len(line) == 0 {
			continue
		}
		if err := json.Unmarshal(line, &e); err != nil {
			h.t.Fatal(err)
		}
		if e.Type == typ {
			out = append(out, e)
		}
	}
	return out
}

func query(location string) evidence.Query {
	return evidence.Query{Tool: "workload_state", Target: signal.Target{Platform: "kubernetes", Location: location, Namespace: "demo", Kind: "Deployment", Name: "orders"}}
}

func TestEvidenceQueryReachesOnlyTheTargetsLocation(t *testing.T) {
	h := startLink(t)
	a, b := &stubTool{location: "ic-target"}, &stubTool{location: "aks-prod"}
	h.sensorUp("ic-target", "target-token", a)
	h.sensorUp("aks-prod", "prod-token-x", b)
	h.waitOnline("ic-target", true)
	h.waitOnline("aks-prod", true)

	for i, q := range []struct {
		location string
		from     string
		a, b     int
	}{{"ic-target", `{"from":"ic-target"}`, 1, 0}, {"aks-prod", `{"from":"aks-prod"}`, 1, 1}, {"ic-target", `{"from":"ic-target"}`, 2, 1}} {
		res, err := h.evidence.Query(context.Background(), 7, query(q.location))
		if err != nil {
			t.Fatalf("query %d: %v", i, err)
		}
		if string(res.Data) != q.from {
			t.Errorf("query %d data = %s, want %s", i, res.Data, q.from)
		}
		if res.Ref.Location != q.location || res.Ref.Target.Location != q.location || res.Ref.Source != "sensor" || res.Ref.CallID == "" {
			t.Errorf("query %d ref = %+v", i, res.Ref)
		}
		if res.Ref.Window.End.IsZero() || res.Ref.Window.Start.IsZero() {
			t.Errorf("query %d ref has no window: %+v", i, res.Ref.Window)
		}
		if a.count() != q.a || b.count() != q.b {
			t.Errorf("query %d calls = %d/%d, want %d/%d", i, a.count(), b.count(), q.a, q.b)
		}
	}

	// Each call is audited once, keyed by its ref, under its Incident.
	entries := h.entries("evidence.query")
	if len(entries) != 3 {
		t.Fatalf("evidence.query entries = %d, want 3", len(entries))
	}
	var got struct {
		Ref     evidence.Ref `json:"ref"`
		Tool    string       `json:"tool"`
		Outcome string       `json:"outcome"`
	}
	if err := json.Unmarshal(entries[0].Data, &got); err != nil {
		t.Fatal(err)
	}
	if entries[0].Incident != 7 || got.Ref.CallID == "" || got.Tool != "workload_state" || got.Outcome != "ok" {
		t.Errorf("audit entry = %+v %s", got, entries[0].Data)
	}
}

func TestToolFailureComesBackAsAnAuditedError(t *testing.T) {
	h := startLink(t)
	h.sensorUp("ic-target", "target-token") // no tools
	h.waitOnline("ic-target", true)

	_, err := h.evidence.Query(context.Background(), 0, query("ic-target"))
	if err == nil || !strings.Contains(err.Error(), "workload_state") {
		t.Fatalf("err = %v, want it to name the tool", err)
	}
	e := h.entries("evidence.query")
	if len(e) != 1 || !strings.Contains(string(e[0].Data), `"outcome":"tool_error"`) {
		t.Fatalf("audit = %+v", e)
	}
	if strings.Contains(string(e[0].Data), "no such tool") {
		t.Errorf("Sensor error text in audit: %s", e[0].Data)
	}
}

func TestLocationNotInConfigIsRefusedAtLinkSetup(t *testing.T) {
	h := startLink(t)
	h.sensorUp("ghost", "target-token", &stubTool{})
	h.sensorUp("aks-prod", "not-the-token", &stubTool{})
	deadline := time.Now().Add(5 * time.Second)
	for len(h.entries("link.refused")) < 2 {
		if time.Now().After(deadline) {
			t.Fatal("no link.refused entries")
		}
		time.Sleep(time.Millisecond)
	}
	for _, e := range h.entries("link.refused") {
		if strings.Contains(string(e.Data), "not-the-token") {
			t.Errorf("credential in audit: %s", e.Data)
		}
	}
	if h.hub.Online("ghost") || h.hub.Online("aks-prod") {
		t.Error("a refused Sensor is online")
	}
	if len(h.entries("location.online")) != 0 {
		t.Errorf("location.online audited for a refused Sensor")
	}
}

func TestKilledSensorGoesOfflineAndARestartBringsItBack(t *testing.T) {
	h := startLink(t)
	kill := h.sensorUp("ic-target", "target-token", &stubTool{})
	h.waitOnline("ic-target", true)

	h.clock.advance(link.OfflineAfter - time.Second)
	if err := h.hub.Sweep(); err != nil {
		t.Fatal(err)
	}
	if !h.hub.Online("ic-target") {
		t.Fatal("offline before the bound")
	}

	kill()
	h.clock.advance(2 * time.Second)
	if err := h.hub.Sweep(); err != nil {
		t.Fatal(err)
	}
	if h.hub.Online("ic-target") {
		t.Fatal("still online past the bound")
	}
	if _, err := h.evidence.Query(context.Background(), 0, query("ic-target")); !errors.Is(err, evidence.ErrOffline) {
		t.Fatalf("query offline: err = %v", err)
	}

	h.sensorUp("ic-target", "target-token", &stubTool{})
	h.waitOnline("ic-target", true)
	if on, off := len(h.entries("location.online")), len(h.entries("location.offline")); on != 2 || off != 1 {
		t.Errorf("audited online %d, offline %d; want 2, 1", on, off)
	}
}

func TestASensorCannotAnswerAnotherLocationsCall(t *testing.T) {
	h := startLink(t)
	target := link.NewClient(h.url, "ic-target", "target-token")
	prod := link.NewClient(h.url, "aks-prod", "prod-token-x")
	// Reaching Next is what brings the Location online; the first poll returns
	// the Call only once Query has sent it.
	h.hub.PollHold = time.Millisecond
	if _, _, err := target.Next(context.Background()); err != nil {
		t.Fatal(err)
	}
	h.hub.PollHold = 5 * time.Second

	type outcome struct {
		res evidence.Result
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		res, err := h.evidence.Query(context.Background(), 0, query("ic-target"))
		done <- outcome{res, err}
	}()
	call, ok, err := target.Next(context.Background())
	if err != nil || !ok {
		t.Fatalf("Next: %v %v", ok, err)
	}
	if err := prod.Reply(context.Background(), call.ID, evidence.Answer{Data: json.RawMessage(`"forged"`)}); err == nil {
		t.Error("another Location's reply was accepted")
	}
	if err := target.Reply(context.Background(), call.ID, evidence.Answer{Data: json.RawMessage(`"real"`)}); err != nil {
		t.Fatal(err)
	}
	if got := <-done; got.err != nil || string(got.res.Data) != `"real"` {
		t.Fatalf("got %s, %v", got.res.Data, got.err)
	}
	if len(h.entries("link.reply_dropped")) != 1 {
		t.Errorf("forged reply not audited")
	}
}

func TestACallThatTimedOutNeverReachesASensor(t *testing.T) {
	h := startLink(t)
	h.hub.PollHold = time.Millisecond
	c := link.NewClient(h.url, "ic-target", "target-token")
	if _, _, err := c.Next(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	if _, err := h.evidence.Query(ctx, 0, query("ic-target")); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want a deadline", err)
	}
	if call, ok, err := c.Next(context.Background()); err != nil || ok {
		t.Fatalf("Next = %+v %v %v, want no Call", call, ok, err)
	}
}

func TestDebugHandlerAnswersQueriesAndListsLocations(t *testing.T) {
	h := startLink(t)
	h.sensorUp("ic-target", "target-token", &stubTool{location: "ic-target"})
	h.waitOnline("ic-target", true)
	srv := httptest.NewServer(h.evidence.DebugHandler())
	defer srv.Close()

	body, _ := json.Marshal(query("ic-target"))
	resp, err := http.Post(srv.URL+"/debug/evidence", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var res evidence.Result
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || res.Ref.CallID == "" || string(res.Data) != `{"from":"ic-target"}` {
		t.Errorf("%d %+v", resp.StatusCode, res)
	}

	body, _ = json.Marshal(query("aks-prod"))
	off, err := http.Post(srv.URL+"/debug/evidence", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	_ = off.Body.Close()
	if off.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("offline status = %d", off.StatusCode)
	}

	list, err := http.Get(srv.URL + "/debug/locations")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = list.Body.Close() }()
	var got []LocationStatus
	if err := json.NewDecoder(list.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	want := []LocationStatus{{"ic-target", "staging", true}, {"aks-prod", "production", false}}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("locations = %+v, want %+v", got, want)
	}
}
