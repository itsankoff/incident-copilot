package controlplane

import (
	"bufio"
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/itsankoff/incident-copilot/internal/llm"
)

// syncBuffer is a log sink an investigation may write while a test reads it.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// milestones waits for the investigations to finish and returns the Control
// plane's info-level log lines.
func (h *harness) milestones() []map[string]any {
	h.t.Helper()
	h.cp.Wait()
	var out []map[string]any
	sc := bufio.NewScanner(strings.NewReader(h.logs.String()))
	for sc.Scan() {
		var line map[string]any
		if err := json.Unmarshal(sc.Bytes(), &line); err != nil {
			h.t.Fatal(err)
		}
		if line["level"] != "DEBUG" {
			out = append(out, line)
		}
	}
	return out
}

func msgs(lines []map[string]any) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = l["msg"].(string)
	}
	return out
}

// want checks that line has every field in fields.
func want(t *testing.T, line map[string]any, fields map[string]any) {
	t.Helper()
	for k, v := range fields {
		if line[k] != v {
			t.Errorf("%s: %s = %v, want %v (line %v)", line["msg"], k, line[k], v, line)
		}
	}
}

func TestAnIncidentsMilestonesAreLoggedOnceInOrder(t *testing.T) {
	l := &fakeLink{}
	model := &scriptedLLM{script: func(n int, req llm.Request) llm.Response {
		if n == 1 {
			return callTools("workload_state", "events")
		}
		refs := refsIn(t, req)
		return submit(RCA{Summary: []Claim{{ID: "c1", Statement: "Crash-looping since the rollout.", Kind: Observed, Cites: []string{refs["workload_state"]}}},
			Confidence: Confidence{Level: "medium", Claims: []Claim{{ID: "c2", Statement: "Seen directly.", Kind: Inferred, Cites: []string{"c1"}}}}})
	}}
	h := startRCA(t, model, l, testBudget)
	h.send(tokenProd, alert("KubePodCrashLooping", "aaa", orders))
	h.cp.Wait()
	h.send(tokenProd, alert("OrdersErrorRate", "bbb", orders))
	h.send(tokenProd, alert("OrdersErrorRate", "bbb", orders)) // a repeat notice

	lines := h.milestones()
	got := msgs(lines)
	order := []string{
		"signal.received", "triage.decision", "ticket.created",
		"investigation.started",
		"investigation.step", "investigation.step", "investigation.step", // the LLM, then its two Evidence queries
		"investigation.step", "investigation.step", // the LLM, then submit_rca
		"investigation.finished", "rca.posted",
		"remediation.skipped", "advice.posted", "ticket.ready", // no Executor is linked, so no Actions are offered
		"signal.received", "triage.decision", "ticket.commented",
	}
	if strings.Join(got, " ") != strings.Join(order, " ") {
		t.Fatalf("milestones:\n %q\nwant\n %q", got, order)
	}
	for _, line := range lines {
		if line["incident"] != float64(1) {
			t.Errorf("%s: incident %v, want 1", line["msg"], line["incident"])
		}
	}

	want(t, lines[0], map[string]any{"source": "alertmanager/prod-am", "name": "KubePodCrashLooping", "target": "prod/demo/Deployment/orders"})
	want(t, lines[1], map[string]any{"decision": "open"})
	want(t, lines[2], map[string]any{"adapter": "fake", "ref": "1", "kind": "incident"})
	want(t, lines[3], map[string]any{"model": "openrouter/free"})
	if b, ok := lines[3]["budget"].(map[string]any); !ok || b["steps"] != float64(4) || b["tokens"] != float64(100_000) || b["time"] != "1m0s" {
		t.Errorf("investigation.started budget = %v", lines[3]["budget"])
	}
	want(t, lines[4], map[string]any{"step": float64(1), "llm": "vendor/free-model-1", "ok": true})
	want(t, lines[5], map[string]any{"step": float64(1), "tool": "workload_state", "ok": true})
	want(t, lines[6], map[string]any{"step": float64(1), "tool": "events", "ok": true})
	want(t, lines[7], map[string]any{"step": float64(2), "llm": "vendor/free-model-2", "ok": true})
	want(t, lines[8], map[string]any{"step": float64(2), "tool": "submit_rca", "ok": true})
	want(t, lines[9], map[string]any{"confidence": "medium", "steps": float64(2), "duration": "0s"})
	want(t, lines[10], map[string]any{"ref": "1"})
	if r, _ := lines[11]["reason"].(string); !strings.Contains(r, "not linked") {
		t.Errorf("remediation.skipped reason %q", lines[11]["reason"])
	}
	want(t, lines[12], map[string]any{"ref": "1", "level": "L1"})
	want(t, lines[13], map[string]any{"ref": "1", "url": "https://tickets.example/1"})
	want(t, lines[14], map[string]any{"name": "OrdersErrorRate"})
	want(t, lines[15], map[string]any{"decision": "join"})
	want(t, lines[16], map[string]any{"adapter": "fake", "ref": "1", "kind": "join"})

	// Names and counts only: no Evidence, model text, or Alert annotations,
	// at any level.
	all := h.logs.String()
	for _, leak := range []string{"CrashLoopBackOff", "orders-new", "Crash-looping since", secretNote} {
		if strings.Contains(all, leak) {
			t.Errorf("the log contains %q", leak)
		}
	}
}

func TestAnInvestigationOutOfBudgetLogsExhausted(t *testing.T) {
	l := &fakeLink{fail: map[string]bool{"events": true}}
	model := &scriptedLLM{script: func(int, llm.Request) llm.Response { return callTools("events") }}
	h := startRCA(t, model, l, Budget{Steps: 2, Tokens: 100_000, Time: time.Minute})
	h.send(tokenProd, alert("KubePodCrashLooping", "aaa", orders))

	lines := h.milestones()
	got := strings.Join(msgs(lines), " ")
	if strings.Contains(got, "investigation.finished") || !strings.Contains(got, "investigation.exhausted rca.posted remediation.skipped advice.posted ticket.ready") {
		t.Fatalf("milestones %q, want investigation.exhausted and no investigation.finished", got)
	}
	for _, line := range lines {
		switch {
		case line["msg"] == "investigation.exhausted":
			want(t, line, map[string]any{"budget": "steps", "confidence": "low", "steps": float64(2)})
		case line["tool"] == "events":
			want(t, line, map[string]any{"ok": false})
		}
	}
	if strings.Contains(h.logs.String(), "sensor-side failure") {
		t.Error("the log contains the Sensor's error text")
	}
}

func TestAnIncidentsLifecycleIsLogged(t *testing.T) {
	h := newHarness(t, t.TempDir(), &fakeTickets{})
	crash := alert("KubePodCrashLooping", "aaa", orders)
	h.send(tokenProd, crash)
	h.send(tokenProd, resolvedAlert(crash))
	h.now = h.now.Add(24 * time.Hour)
	h.sweep()

	lines := h.milestones()
	got := msgs(lines)
	order := []string{
		"signal.received", "triage.decision", "ticket.created",
		"signal.received", "triage.decision", "ticket.commented",
		"triage.decision", "ticket.commented", "ticket.closed", // the Sweep's idle close
	}
	if strings.Join(got, " ") != strings.Join(order, " ") {
		t.Fatalf("milestones:\n %q\nwant\n %q", got, order)
	}
	want(t, lines[4], map[string]any{"incident": float64(1), "decision": "resolve"})
	want(t, lines[5], map[string]any{"incident": float64(1), "kind": "resolve"})
	want(t, lines[6], map[string]any{"incident": float64(1), "decision": "close"})
	if r, _ := lines[6]["reason"].(string); !strings.Contains(r, "24 hours") {
		t.Errorf("close decision reason %q", lines[6]["reason"])
	}
	want(t, lines[7], map[string]any{"incident": float64(1), "kind": "close"})
	want(t, lines[8], map[string]any{"incident": float64(1), "adapter": "fake", "ref": "1"})
}

func TestAHumanCloseCancellingAnInvestigationIsLogged(t *testing.T) {
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

	lines := h.milestones()
	got := strings.Join(msgs(lines), " ")
	if !strings.HasSuffix(got, "investigation.cancelled") || strings.Contains(got, "rca.posted") {
		t.Fatalf("milestones %q, want them to end in investigation.cancelled", got)
	}
	var closeDecision bool
	for _, line := range lines {
		if line["msg"] == "triage.decision" && line["decision"] == "close" {
			closeDecision = true
			if r, _ := line["reason"].(string); !strings.Contains(r, "closed by a human") {
				t.Errorf("close decision reason %q", line["reason"])
			}
		}
		if line["msg"] == "investigation.cancelled" {
			want(t, line, map[string]any{"incident": float64(1), "steps": float64(1)})
		}
	}
	if !closeDecision {
		t.Errorf("no close decision logged in %q", got)
	}
}

func TestTheRemediationLoopIsLoggedBeforeTheTicketIsReady(t *testing.T) {
	h := startAdvice(t, remediate(t, func(_, ref string) string { return rollbackTo(ref) }))
	h.send(tokenProd, alert("KubePodCrashLooping", "aaa", orders))

	lines := h.milestones()
	got := msgs(lines)
	i := slices.Index(got, "rca.posted")
	if i < 0 {
		t.Fatalf("no rca.posted in %q", got)
	}
	tail := []string{
		"remediation.started",
		"remediation.step", "remediation.step", // the LLM, then its workload_state read
		"remediation.step", "remediation.step", // the LLM, then submit_advice
		"remediation.finished", "advice.posted", "ticket.ready",
	}
	if strings.Join(got[i+1:], " ") != strings.Join(tail, " ") {
		t.Fatalf("milestones after rca.posted:\n %q\nwant\n %q", got[i+1:], tail)
	}
	rest := lines[i+1:]
	want(t, rest[0], map[string]any{"incident": float64(1), "model": "openrouter/free"})
	want(t, rest[2], map[string]any{"step": float64(1), "tool": "workload_state", "ok": true})
	want(t, rest[4], map[string]any{"step": float64(2), "tool": "submit_advice", "ok": true})
	want(t, rest[5], map[string]any{"submitted": true, "steps": float64(2)})
	// Approval is not built yet (#58), so Advice stays at L1.
	want(t, rest[6], map[string]any{"ref": "1", "level": "L1"})
	want(t, rest[7], map[string]any{"ref": "1", "url": "https://tickets.example/1"})
	if strings.Contains(h.logs.String(), "Revision 4 broke orders") {
		t.Error("the log contains the model's Advice text")
	}
}
