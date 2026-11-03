package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/itsankoff/incident-copilot/internal/audit"
	"github.com/itsankoff/incident-copilot/internal/evidence"
	"github.com/itsankoff/incident-copilot/internal/llm"
)

// fakeLink stands in for every Sensor: it answers each Call with canned data,
// or with a tool error for the tools in fail.
type fakeLink struct {
	mu    sync.Mutex
	calls []evidence.Call
	fail  map[string]bool
}

func (f *fakeLink) Call(_ context.Context, _ string, c evidence.Call) (evidence.Answer, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, c)
	if f.fail[c.Tool] {
		return evidence.Answer{Error: "sensor-side failure"}, nil
	}
	return evidence.Answer{Data: json.RawMessage(fmt.Sprintf(`{"tool":%q,"replicaSet":"orders-new","state":"CrashLoopBackOff"}`, c.Tool))}, nil
}

func (f *fakeLink) Online(string) bool { return true }

func (f *fakeLink) callIDs() map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]string{}
	for _, c := range f.calls {
		out[c.ID] = c.Tool
	}
	return out
}

// scriptedLLM answers request n with script(n, request); it never touches the
// network.
type scriptedLLM struct {
	mu     sync.Mutex
	n      int
	script func(n int, req llm.Request) llm.Response
}

func (s *scriptedLLM) Complete(_ context.Context, req llm.Request) (llm.Response, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.n++
	resp := s.script(s.n, req)
	if resp.Model == "" {
		resp.Model = fmt.Sprintf("vendor/free-model-%d", s.n)
	}
	resp.Usage = llm.Usage{InputTokens: 100, OutputTokens: 20}
	return resp, nil
}

func callTools(names ...string) llm.Response {
	m := llm.Message{Role: llm.Assistant}
	for i, n := range names {
		m.ToolCalls = append(m.ToolCalls, llm.ToolCall{ID: fmt.Sprintf("tc%d", i), Name: n, Arguments: "{}"})
	}
	return llm.Response{Message: m, FinishReason: "tool_calls"}
}

func submit(rca RCA) llm.Response {
	b, _ := json.Marshal(rca)
	return llm.Response{Message: llm.Message{Role: llm.Assistant, ToolCalls: []llm.ToolCall{{ID: "final", Name: "submit_rca", Arguments: string(b)}}}, FinishReason: "tool_calls"}
}

// refsIn returns the call ids of the Evidence refs in req's tool results, by
// the tool that produced them.
func refsIn(t *testing.T, req llm.Request) map[string]string {
	t.Helper()
	calls := map[string]string{}
	for _, m := range req.Messages {
		if m.Role == llm.Assistant {
			for _, tc := range m.ToolCalls {
				calls[tc.ID] = tc.Name
			}
		}
	}
	out := map[string]string{}
	for _, m := range req.Messages {
		if m.Role != llm.Tool {
			continue
		}
		var res struct {
			Ref evidence.Ref `json:"ref"`
		}
		if err := json.Unmarshal([]byte(m.Content), &res); err != nil {
			t.Fatalf("tool result is not JSON: %v: %s", err, m.Content)
		}
		if res.Ref.CallID != "" {
			out[calls[m.ToolCallID]] = res.Ref.CallID
		}
	}
	return out
}

func startRCA(t *testing.T, model llm.Client, l *fakeLink, budget Budget) *harness {
	t.Helper()
	return startWith(t, t.TempDir(), &fakeTickets{}, prodAndStaging(), nil, func(log *audit.Log, tickets Ticketing, now func() time.Time) *Investigator {
		ev := NewEvidence([]Location{{Name: "prod", Environment: "production", Token: "x"}}, l, log, now)
		return NewInvestigator(model, "openrouter/free", budget, ev, tickets, log, now)
	})
}

var testBudget = Budget{Steps: 4, Tokens: 100_000, Time: time.Minute}

// rcaComment waits for the investigations to finish and returns the one RCA
// comment on ticket ref.
func (h *harness) rcaComment(ref string) string {
	h.t.Helper()
	h.cp.Wait()
	var out []string
	for _, c := range h.tickets.comments {
		if c.ref == ref && strings.Contains(c.body, "Root-cause analysis") {
			out = append(out, c.body)
		}
	}
	if len(out) != 1 {
		h.t.Fatalf("RCA comments on %s = %d, want 1: %+v", ref, len(out), h.tickets.comments)
	}
	return out[0]
}

func TestAlertGetsAnRCAWhoseClaimsCiteTheEvidenceQueries(t *testing.T) {
	l := &fakeLink{}
	model := &scriptedLLM{script: func(n int, req llm.Request) llm.Response {
		if n == 1 {
			return callTools("workload_state", "events")
		}
		refs := refsIn(t, req)
		return submit(RCA{
			Summary: []Claim{
				{ID: "c1", Statement: "The new ReplicaSet orders-new is crash-looping.", Kind: Observed, Cites: []string{refs["workload_state"]}},
				{ID: "c2", Statement: "Events show back-off restarting the container.", Kind: Observed, Cites: []string{refs["events"]}},
			},
			Confidence: Confidence{Level: "medium", Claims: []Claim{
				{ID: "c3", Statement: "The rollout introduced the crash.", Kind: Inferred, Cites: []string{"c1", "c2"}},
			}},
		})
	}}
	h := startRCA(t, model, l, testBudget)
	h.send(tokenProd, alert("KubePodCrashLooping", "aaa", orders))

	body := h.rcaComment("1")
	ids := l.callIDs()
	if len(ids) != 2 {
		t.Fatalf("Evidence queries = %v, want workload_state and events", ids)
	}
	for id, tool := range ids {
		if !strings.Contains(body, id) {
			t.Errorf("RCA does not cite %s call %s:\n%s", tool, id, body)
		}
	}
	for _, want := range []string{"orders-new is crash-looping", "**c3**", "c1, c2", "medium"} {
		if !strings.Contains(body, want) {
			t.Errorf("RCA lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "fails Gate 1") {
		t.Errorf("a well-cited RCA failed Gate 1:\n%s", body)
	}
	for _, c := range l.calls {
		if c.Target.Name != "orders" || c.Target.Location != "prod" {
			t.Errorf("query read %s, want the Incident's Target", c.Target)
		}
	}
}

func TestGate1CatchesARefFromAnotherIncidentAndAFailedCall(t *testing.T) {
	l := &fakeLink{fail: map[string]bool{"events": true}}
	var first string // the orders Incident's workload_state ref
	model := &scriptedLLM{script: func(n int, req llm.Request) llm.Response {
		if n%2 == 1 {
			return callTools("workload_state", "events")
		}
		refs := refsIn(t, req)
		if first == "" {
			first = refs["workload_state"]
			return submit(RCA{Summary: []Claim{{ID: "c1", Statement: "orders is crash-looping.", Kind: Observed, Cites: []string{first}}},
				Confidence: Confidence{Level: "high", Claims: []Claim{{ID: "c2", Statement: "Seen directly.", Kind: Inferred, Cites: []string{"c1"}}}}})
		}
		return submit(RCA{
			Summary: []Claim{
				{ID: "c1", Statement: "Borrowed from orders.", Kind: Observed, Cites: []string{first}},
				{ID: "c2", Statement: "From the failed events call.", Kind: Observed, Cites: []string{refs["events"]}},
				{ID: "c3", Statement: "Fine on its own.", Kind: Observed, Cites: []string{refs["workload_state"]}},
			},
			Confidence: Confidence{Level: "high", Claims: []Claim{{ID: "c4", Statement: "Follows from nothing.", Kind: Inferred, Cites: []string{"c9"}}}},
		})
	}}
	h := startRCA(t, model, l, testBudget)
	h.send(tokenProd, alert("KubePodCrashLooping", "aaa", orders))
	h.cp.Wait()
	h.send(tokenProd, alert("KubePodCrashLooping", "bbb", map[string]string{"namespace": "demo", "deployment": "inventory"}))

	if body := h.rcaComment("1"); strings.Contains(body, "fails Gate 1") || !strings.Contains(body, "**Confidence:** high") {
		t.Errorf("first RCA should pass:\n%s", body)
	}
	body := h.rcaComment("2")
	for _, want := range []string{
		"**Confidence:** low (capped from \"high\"",
		"cites " + first + ", which is not an Evidence query of this Incident",
		"whose Evidence query failed",
		"cites c9, which is not another claim",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("RCA lacks %q:\n%s", want, body)
		}
	}
	if n := strings.Count(body, "fails Gate 1"); n != 3 {
		t.Errorf("%d Gate 1 failures, want 3 (c1, c2, c4):\n%s", n, body)
	}
	gates := ofType(h.auditLog(), "gate1.rca")
	if len(gates) != 2 || !strings.Contains(string(gates[1].Data), `"passed":false`) || gates[1].Incident != 2 {
		t.Errorf("gate1.rca audit = %+v", gates)
	}
}

func TestAModelThatNeverStopsIsCutOffByTheStepBudget(t *testing.T) {
	l := &fakeLink{}
	model := &scriptedLLM{script: func(int, llm.Request) llm.Response { return callTools("workload_state") }}
	h := startRCA(t, model, l, Budget{Steps: 3, Tokens: 100_000, Time: time.Minute})
	h.send(tokenProd, alert("KubePodCrashLooping", "aaa", orders))

	body := h.rcaComment("1")
	if model.n != 3 || len(l.callIDs()) != 3 {
		t.Errorf("LLM requests %d, Evidence queries %d; want 3 each", model.n, len(l.callIDs()))
	}
	for _, want := range []string{"**No RCA:**", "step budget of 3 ran out", "**Confidence:** low"} {
		if !strings.Contains(body, want) {
			t.Errorf("RCA lacks %q:\n%s", want, body)
		}
	}
	for id := range l.callIDs() {
		if !strings.Contains(body, id) {
			t.Errorf("Evidence list lacks %s:\n%s", id, body)
		}
	}
}

// Seen live: a free model repeated summary claims under confidence. The same
// claim twice is one claim; one id for two different claims is not.
func TestGate1TakesARepeatedClaimOnceAndRefusesAReusedID(t *testing.T) {
	l := &fakeLink{}
	model := &scriptedLLM{script: func(n int, req llm.Request) llm.Response {
		if n%2 == 1 {
			return callTools("workload_state")
		}
		ref := refsIn(t, req)["workload_state"]
		c1 := Claim{ID: "c1", Statement: "Crash-looping.", Kind: Observed, Cites: []string{ref}}
		other := c1
		if n == 4 {
			other.Statement = "Something else."
		}
		return submit(RCA{Summary: []Claim{c1}, Confidence: Confidence{Level: "high", Claims: []Claim{other}}})
	}}
	h := startRCA(t, model, l, testBudget)
	h.send(tokenProd, alert("KubePodCrashLooping", "aaa", orders))
	h.cp.Wait()
	h.send(tokenProd, alert("KubePodCrashLooping", "bbb", map[string]string{"namespace": "demo", "deployment": "inventory"}))

	if body := h.rcaComment("1"); strings.Contains(body, "fails Gate 1") {
		t.Errorf("a repeated claim failed Gate 1:\n%s", body)
	}
	body := h.rcaComment("2")
	if n := strings.Count(body, `claim id "c1" names two different claims`); n != 2 { // shown under both
		t.Errorf("reused id reported %d times, want once per claim shown (2):\n%s", n, body)
	}
}

// Seen live: a free model gave confidence.claims as claim ids. Told what did
// not match, it can submit again.
func TestAMalformedSubmissionIsExplainedToTheModel(t *testing.T) {
	l := &fakeLink{}
	var told string
	model := &scriptedLLM{script: func(n int, req llm.Request) llm.Response {
		switch n {
		case 1:
			return callTools("workload_state")
		case 2:
			return llm.Response{Message: llm.Message{Role: llm.Assistant, ToolCalls: []llm.ToolCall{{ID: "bad", Name: "submit_rca",
				Arguments: `{"summary":[],"confidence":{"level":"high","claims":["c1"]}}`}}}}
		}
		told = req.Messages[len(req.Messages)-1].Content
		refs := refsIn(t, req)
		return submit(RCA{Summary: []Claim{{ID: "c1", Statement: "Crash-looping.", Kind: Observed, Cites: []string{refs["workload_state"]}}},
			Confidence: Confidence{Level: "high", Claims: []Claim{{ID: "c2", Statement: "Seen directly.", Kind: Inferred, Cites: []string{"c1"}}}}})
	}}
	h := startRCA(t, model, l, testBudget)
	h.send(tokenProd, alert("KubePodCrashLooping", "aaa", orders))

	body := h.rcaComment("1")
	if !strings.Contains(told, "confidence.claims") {
		t.Errorf("the model was told %q, want the field that did not match", told)
	}
	if !strings.Contains(body, "**Confidence:** high") || !strings.Contains(body, "Crash-looping.") {
		t.Errorf("the resubmitted RCA was not written:\n%s", body)
	}
}

func TestAnInvestigationIsAuditedWholeAndReproducibly(t *testing.T) {
	l := &fakeLink{}
	model := &scriptedLLM{script: func(n int, req llm.Request) llm.Response {
		switch n {
		case 1:
			return callTools("workload_state", "events")
		case 2:
			return llm.Response{Message: llm.Message{Role: llm.Assistant, Content: "I think it is the rollout."}, FinishReason: "stop"}
		}
		refs := refsIn(t, req)
		return submit(RCA{Summary: []Claim{{ID: "c1", Statement: "Crash-looping.", Kind: Observed, Cites: []string{refs["workload_state"]}}},
			Confidence: Confidence{Level: "medium", Claims: []Claim{{ID: "c2", Statement: "Directly observed.", Kind: Inferred, Cites: []string{"c1"}}}}})
	}}
	h := startRCA(t, model, l, testBudget)
	h.send(tokenProd, alert("KubePodCrashLooping", "aaa", orders))
	h.cp.Wait()

	log := h.auditLog()
	for _, e := range log {
		if strings.Contains(string(e.Data), "CrashLoopBackOff") {
			t.Errorf("%s holds raw tool output: %s", e.Type, e.Data)
		}
	}
	reqs, resps := ofType(log, "llm.request"), ofType(log, "llm.response")
	if len(reqs) != 3 || len(resps) != 3 {
		t.Fatalf("llm.request %d, llm.response %d; want 3 each", len(reqs), len(resps))
	}
	for i, e := range resps {
		var got struct {
			Model, AnsweredBy string
			Message           llm.Message
		}
		if err := json.Unmarshal(e.Data, &got); err != nil {
			t.Fatal(err)
		}
		if want := fmt.Sprintf("vendor/free-model-%d", i+1); got.Model != "openrouter/free" || got.AnsweredBy != want || e.Incident != 1 {
			t.Errorf("response %d: model %q answeredBy %q incident %d; want openrouter/free, %s, 1", i, got.Model, got.AnsweredBy, e.Incident, want)
		}
	}
	if !strings.Contains(string(resps[1].Data), "I think it is the rollout.") {
		t.Errorf("response text not audited: %s", resps[1].Data)
	}
	if req := string(reqs[2].Data); !strings.Contains(req, `"promptVersion":"`+PromptVersion+`"`) || !strings.Contains(req, "Call submit_rca") || !strings.Contains(req, "sha256:") {
		t.Errorf("request not audited whole: %s", req)
	}
	if q := ofType(log, "evidence.query"); len(q) != 2 || q[0].Incident != 1 {
		t.Errorf("evidence.query = %+v, want 2 under Incident 1", q)
	}
	for _, typ := range []string{"investigation.started", "investigation.finished"} {
		e := ofType(log, typ)
		if len(e) != 1 || !strings.Contains(string(e[0].Data), `"model":"openrouter/free"`) || !strings.Contains(string(e[0].Data), `"promptVersion":"`+PromptVersion+`"`) {
			t.Errorf("%s = %+v", typ, e)
		}
	}
	if f := ofType(log, "investigation.finished"); !strings.Contains(string(f[0].Data), `"answeredBy":["vendor/free-model-1","vendor/free-model-2","vendor/free-model-3"]`) {
		t.Errorf("investigation.finished lacks the answering models: %s", f[0].Data)
	}
	if c := ofType(log, "ticket.commented"); len(c) != 1 || !strings.Contains(string(c[0].Data), `"kind":"rca"`) {
		t.Errorf("ticket.commented = %+v", c)
	}
}
