package controlplane

import (
	"bufio"
	"bytes"
	"encoding/json"
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
		"investigation.finished", "rca.posted", "ticket.ready",
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
	want(t, lines[11], map[string]any{"ref": "1", "url": "https://tickets.example/1"})
	want(t, lines[12], map[string]any{"name": "OrdersErrorRate"})
	want(t, lines[13], map[string]any{"decision": "join"})
	want(t, lines[14], map[string]any{"adapter": "fake", "ref": "1", "kind": "signal"})

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
	if strings.Contains(got, "investigation.finished") || !strings.Contains(got, "investigation.exhausted rca.posted ticket.ready") {
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
