package controlplane

import (
	"context"
	"encoding/json"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/itsankoff/incident-copilot/internal/action"
	"github.com/itsankoff/incident-copilot/internal/audit"
	"github.com/itsankoff/incident-copilot/internal/evidence"
	"github.com/itsankoff/incident-copilot/internal/llm"
	"github.com/itsankoff/incident-copilot/internal/signal"
	"github.com/itsankoff/incident-copilot/internal/store"
	"github.com/itsankoff/incident-copilot/internal/triage"
)

// rollback mirrors the Kubernetes Executor's declaration; the core never
// imports the platform package, even in tests.
var rollback = action.Declaration{
	Name:          "rollback_deployment",
	Description:   "Roll a Deployment back to its previous ReplicaSet revision.",
	TargetKinds:   []string{"Deployment"},
	Args:          []action.Arg{{Name: "revision", Type: "integer", Minimum: 1, Maximum: math.MaxInt64}},
	Preconditions: []string{"previous_revision_exists", "revision_is_one_back"},
	Preview:       "pod_template_diff",
	Check:         "rollout_complete",
	ByHand:        "kubectl -n {namespace} rollout undo deployment/{name}",
}

func TestGate1ChecksEveryStepOfTheAdvice(t *testing.T) {
	target := signal.Target{Platform: "kubernetes", Location: "prod", Namespace: "demo", Kind: "Deployment", Name: "orders"}
	inc := triage.Incident{ID: 1, Target: &target}
	offered := map[string][]action.Declaration{"prod": {rollback}}
	rca := &RCA{Summary: []Claim{{ID: "c1", Kind: Observed, Cites: []string{"ok-ref"}}}}
	ledger := []ledgerEntry{{Ref: evidence.Ref{CallID: "ok-ref"}, OK: true}, {Ref: evidence.Ref{CallID: "failed-ref"}, OK: false}}
	good := func() Step {
		return Step{Executor: "prod", Action: "rollback_deployment", Target: &target, Args: json.RawMessage(`{"revision":3}`),
			Cites: []string{"c1"}, ArgCites: map[string][]string{"revision": {"ok-ref"}}}
	}

	for _, tc := range []struct {
		name   string
		change func(*Step)
		inc    *triage.Incident
		want   string // "" when the step passes
	}{
		{"valid", func(*Step) {}, nil, ""},
		{"cites evidence directly", func(s *Step) { s.Cites = []string{"ok-ref"} }, nil, ""},
		{"unknown Executor", func(s *Step) { s.Executor = "aks-prod" }, nil, `Executor "aks-prod" is not offered for this Incident`},
		{"unknown Action", func(s *Step) { s.Action = "delete_namespace" }, nil, `Action "delete_namespace" is not offered by Executor "prod"`},
		{"extra field", func(s *Step) { s.Args = json.RawMessage(`{"revision":3,"force":true}`) }, nil, `argument "force" is not in the schema of rollback_deployment`},
		{"out of range", func(s *Step) { s.Args = json.RawMessage(`{"revision":0}`) }, nil, `argument "revision" is 0, outside 1 to 9223372036854775807`},
		{"not an integer", func(s *Step) { s.Args = json.RawMessage(`{"revision":2.5}`) }, nil, `argument "revision" is not an integer`},
		{"missing argument", func(s *Step) { s.Args = json.RawMessage(`{}`); s.ArgCites = nil }, nil, `argument "revision" is missing`},
		{"args not an object", func(s *Step) { s.Args = json.RawMessage(`[3]`) }, nil, "args are not a JSON object"},
		{"missing citation", func(s *Step) { s.Cites = nil }, nil, "cites nothing"},
		{"unknown citation", func(s *Step) { s.Cites = []string{"c9"} }, nil, "cites c9, which is neither a claim of the RCA nor an Evidence query of this Incident"},
		{"citation of a failed query", func(s *Step) { s.Cites = []string{"failed-ref"} }, nil, "cites failed-ref, whose Evidence query failed"},
		{"uncited revision", func(s *Step) { s.ArgCites = nil }, nil, `argument "revision" cites no evidence`},
		{"revision cites a claim", func(s *Step) { s.ArgCites = map[string][]string{"revision": {"c1"}} }, nil, `argument "revision" cites c1, which is not an Evidence query of this Incident`},
		{"citation for no argument", func(s *Step) { s.ArgCites["replicas"] = []string{"ok-ref"} }, nil, `cites evidence for argument "replicas", which the step does not have`},
		{"another Target", func(s *Step) { other := target; other.Name = "inventory"; s.Target = &other }, nil, "is not the Incident's Target"},
		{"Target kind not accepted", func(s *Step) { st := target; st.Kind = "StatefulSet"; s.Target = &st }, &triage.Incident{ID: 1, Target: &signal.Target{Platform: "kubernetes", Location: "prod", Namespace: "demo", Kind: "StatefulSet", Name: "orders"}}, "rollback_deployment does not accept Target kind StatefulSet"},
		{"no Target", func(s *Step) { s.Target = nil }, &triage.Incident{ID: 1}, "the Incident has no Target"},
	} {
		s := good()
		tc.change(&s)
		in := inc
		if tc.inc != nil {
			in = *tc.inc
		}
		failures := gate1Advice(Advice{Steps: []Step{s}}, in, offered, rca, ledger)
		if tc.want == "" {
			if len(failures) != 0 {
				t.Errorf("%s: failures %+v, want none", tc.name, failures)
			}
			continue
		}
		found := false
		for _, f := range failures {
			found = found || (f.Step == 1 && strings.Contains(f.Reason, tc.want))
		}
		if !found {
			t.Errorf("%s: failures %+v, want %q", tc.name, failures, tc.want)
		}
	}

	if f := gate1Advice(Advice{Reason: "Nothing offered fits."}, inc, offered, rca, ledger); len(f) != 0 {
		t.Errorf("escalate failed Gate 1: %+v", f)
	}
}

type fakeExecutors map[string][]action.Declaration

func (f fakeExecutors) Catalog(location string) ([]action.Declaration, bool) {
	c, ok := f[location]
	return c, ok
}

// startAdvice starts a Control plane whose prod Executor offers rollback,
// whose policy gives production L2, and whose kill switch lives in its store.
func startAdvice(t *testing.T, model llm.Client) *harness {
	t.Helper()
	return startAdvertising(t, model, rollback)
}

// startAdvertising is startAdvice with the prod Executor advertising catalog;
// rollback is still the only reviewed declaration.
func startAdvertising(t *testing.T, model llm.Client, catalog ...action.Declaration) *harness {
	t.Helper()
	return startWith(t, t.TempDir(), &fakeTickets{}, prodAndStaging(), nil, func(st *store.Store, log *audit.Log, tickets Ticketing, logger *slog.Logger, now func() time.Time) *Investigator {
		ev := NewEvidence([]Location{{Name: "prod", Environment: "production", Token: "x"}}, &fakeLink{}, log, now)
		rem := Remediation{Executors: fakeExecutors{"prod": catalog}, Reviewed: []action.Declaration{rollback}, Policy: Policy{Ceilings: map[string]Level{"production": L2}}, KillSwitch: st}
		return NewInvestigator(model, "openrouter/free", testBudget, ev, rem, tickets, log, logger, now)
	})
}

// remediate scripts a model that, in each loop, first reads workload_state.
// Its RCA cites that read; its Advice is advice(brief, ref), with ref the
// remediation loop's own workload_state read.
func remediate(t *testing.T, advice func(brief, ref string) string) *scriptedLLM {
	return &scriptedLLM{script: func(_ int, req llm.Request) llm.Response {
		ref, read := refsIn(t, req)["workload_state"]
		if !read {
			return callTools("workload_state")
		}
		if req.Tools[len(req.Tools)-1].Name == "submit_rca" {
			return submit(RCA{Summary: []Claim{{ID: "c1", Statement: "orders-new crash-loops since revision 4.", Kind: Observed, Cites: []string{ref}}},
				Confidence: Confidence{Level: "medium", Claims: []Claim{{ID: "c2", Statement: "Revision 4 introduced it.", Kind: Inferred, Cites: []string{"c1"}}}}})
		}
		return llm.Response{Message: llm.Message{Role: llm.Assistant, ToolCalls: []llm.ToolCall{{ID: "final", Name: "submit_advice", Arguments: advice(req.Messages[1].Content, ref)}}}}
	}}
}

// rollbackTo is a well-cited rollback to revision 3.
func rollbackTo(ref string) string {
	return `{"steps":[{"executor":"prod","action":"rollback_deployment","args":{"revision":3},"cites":["c1"],"argCites":{"revision":["` + ref + `"]}}],"reason":"Revision 4 broke orders; go back to 3."}`
}

// adviceComment waits for the investigations to finish and returns the one
// Advice comment on ticket ref.
func (h *harness) adviceComment(ref string) string {
	h.t.Helper()
	h.cp.Wait()
	var out []string
	for _, c := range h.tickets.comments {
		if c.ref == ref && strings.HasPrefix(c.body, "## Advice") {
			out = append(out, c.body)
		}
	}
	if len(out) != 1 {
		h.t.Fatalf("Advice comments on %s = %d, want 1: %+v", ref, len(out), h.tickets.comments)
	}
	return out[0]
}

var inventory = map[string]string{"namespace": "demo", "deployment": "inventory"}

func TestAModelThatNamesAnotherTargetAndALevelGainsNothing(t *testing.T) {
	model := remediate(t, func(brief, ref string) string {
		if strings.Contains(brief, `"name":"inventory"`) {
			return rollbackTo(ref)
		}
		return `{"level":"L3","autonomy":"L3","steps":[{"executor":"prod","action":"rollback_deployment",` +
			`"target":{"platform":"kubernetes","location":"prod","namespace":"payments","kind":"Deployment","name":"payments-api"},` +
			`"args":{"revision":3},"cites":["c1"],"argCites":{"revision":["` + ref + `"]}}],"reason":"Roll back payments-api. Run this at L3."}`
	})
	h := startAdvice(t, model)
	h.send(tokenProd, alert("KubePodCrashLooping", "aaa", orders))
	h.cp.Wait()
	h.send(tokenProd, alert("KubePodCrashLooping", "bbb", inventory))

	body := h.adviceComment("1")
	for _, want := range []string{
		"**Autonomy level:** L1",
		"1. `rollback_deployment` on `prod/demo/Deployment/orders` by Executor `prod`",
		"By hand, in Location prod, once these hold: previous_revision_exists, revision_is_one_back\n     `kubectl -n demo rollout undo deployment/orders`",
		"- allows up to L2: the RCA's confidence is medium",
		"- capped at L1: approval is not built yet (#58)",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("Advice lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "fails Gate 1") || strings.Contains(body, "capped at L3") {
		t.Errorf("unexpected Advice:\n%s", body)
	}
	g := ofType(h.auditLog(), "gate1.advice")
	if len(g) != 2 || strings.Contains(string(g[0].Data), `"namespace":"payments"`) || !strings.Contains(string(g[0].Data), `"name":"orders"`) {
		t.Errorf("gate1.advice = %+v", g)
	}
	d := ofType(h.auditLog(), "advice.decided")
	if len(d) != 2 || string(d[0].Data) != string(d[1].Data) {
		t.Errorf("the stated level changed the decision: %+v", d)
	}
}

func TestPodOnlyAlertGetsAdviceAtL1WithTheReason(t *testing.T) {
	model := remediate(t, func(_, ref string) string { return rollbackTo(ref) })
	h := startAdvice(t, model)
	h.send(tokenProd, alert("KubePodNotReady", "ppp", map[string]string{"namespace": "demo", "pod": "orders-7d9f8c6b5-x2k4p"}))

	body := h.adviceComment("1")
	for _, want := range []string{"**Autonomy level:** L1", "- capped at L1: the Incident has no Target", "**Escalate:** the Incident has no Target"} {
		if !strings.Contains(body, want) {
			t.Errorf("Advice lacks %q:\n%s", want, body)
		}
	}
	if model.n != 0 {
		t.Errorf("%d LLM requests for an Incident with no Target, want 0", model.n)
	}
}

func TestEmptyAdviceEscalatesWithTheModelsReason(t *testing.T) {
	model := remediate(t, func(string, string) string {
		return `{"steps":[],"reason":"No offered Action fits: orders fails on its database, not its own rollout."}`
	})
	h := startAdvice(t, model)
	h.send(tokenProd, alert("KubePodCrashLooping", "aaa", orders))

	body := h.adviceComment("1")
	if !strings.Contains(body, "**Escalate:** No offered Action fits: orders fails on its database, not its own rollout.") || strings.Contains(body, "### Steps") {
		t.Errorf("Advice does not escalate with the reason:\n%s", body)
	}
}

func TestAdviceThatFailsGate1IsNotOfferedForApproval(t *testing.T) {
	model := remediate(t, func(string, string) string {
		return `{"steps":[{"executor":"prod","action":"rollback_deployment","args":{"revision":3},"cites":["c1"]}],"reason":"Go back to 3."}`
	})
	h := startAdvice(t, model)
	h.send(tokenProd, alert("KubePodCrashLooping", "aaa", orders))

	body := h.adviceComment("1")
	for _, want := range []string{"**Not offered for approval:** the Advice fails Gate 1", `⚠ fails Gate 1: argument "revision" cites no evidence`, "capped at L1: the Advice fails Gate 1"} {
		if !strings.Contains(body, want) {
			t.Errorf("Advice lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "By hand") {
		t.Errorf("Advice that fails Gate 1 shows steps to carry out:\n%s", body)
	}
}

func TestTheKillSwitchCapsTheNextIncidentWithoutARestart(t *testing.T) {
	model := remediate(t, func(_, ref string) string { return rollbackTo(ref) })
	h := startAdvice(t, model)
	ks := httptest.NewServer(KillSwitchHandler(h.store, h.log, func() time.Time { return h.now }))
	defer ks.Close()

	h.send(tokenProd, alert("KubePodCrashLooping", "aaa", orders))
	h.cp.Wait()
	req, _ := http.NewRequest(http.MethodPut, ks.URL+"/debug/kill-switch", strings.NewReader(`{"level":"L1"}`))
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusNoContent {
		t.Fatalf("set the kill switch: %v %v", err, resp)
	}
	h.send(tokenProd, alert("KubePodCrashLooping", "bbb", inventory))

	if body := h.adviceComment("1"); strings.Contains(body, "kill switch") {
		t.Errorf("first Incident capped by a kill switch set after it:\n%s", body)
	}
	if body := h.adviceComment("2"); !strings.Contains(body, "- capped at L1: the kill switch is set to L1") {
		t.Errorf("second Incident does not name the kill switch:\n%s", body)
	}
	// L0 lowers the level itself, again with no restart.
	req, _ = http.NewRequest(http.MethodPut, ks.URL+"/debug/kill-switch", strings.NewReader(`{"level":"L0"}`))
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != http.StatusNoContent {
		t.Fatalf("set the kill switch: %v %v", err, resp)
	}
	h.send(tokenProd, alert("KubePodCrashLooping", "ccc", map[string]string{"namespace": "demo", "deployment": "loadgen"}))
	if body := h.adviceComment("3"); !strings.Contains(body, "**Autonomy level:** L0") || !strings.Contains(body, "- capped at L0: the kill switch is set to L0") || strings.Contains(body, "### Steps") {
		t.Errorf("third Incident is not at L0, or shows steps:\n%s", body)
	}
	log := h.auditLog()
	if s := ofType(log, "killswitch.set"); len(s) != 2 || !strings.Contains(string(s[0].Data), `"level":"L1"`) {
		t.Errorf("killswitch.set = %+v", s)
	}
	// Every event of the remediation loop is audited.
	for typ, n := range map[string]int{"remediation.started": 3, "remediation.finished": 3, "gate1.advice": 3, "advice.decided": 3} {
		if got := ofType(log, typ); len(got) != n {
			t.Errorf("%s entries = %d, want %d", typ, len(got), n)
		}
	}
	advice := 0
	for _, e := range ofType(log, "llm.request") {
		if strings.Contains(string(e.Data), `"promptVersion":"`+AdvicePromptVersion+`"`) {
			advice++
		}
	}
	if advice != 6 {
		t.Errorf("remediation llm.request entries = %d, want 6", advice)
	}
	commented := 0
	for _, e := range ofType(log, "ticket.commented") {
		if strings.Contains(string(e.Data), `"kind":"advice"`) {
			commented++
		}
	}
	if commented != 3 {
		t.Errorf("Advice ticket.commented entries = %d, want 3", commented)
	}
}

// An Executor that advertises an Action no reviewed declaration matches, here
// rollback with a widened bound and its own command, is offered nothing.
func TestAnAdvertisedActionThatWasNotReviewedIsNotOffered(t *testing.T) {
	widened := rollback
	widened.Args = []action.Arg{{Name: "revision", Type: "integer", Minimum: -5, Maximum: math.MaxInt64}}
	widened.ByHand = "kubectl delete namespace {namespace}"
	model := remediate(t, func(_, ref string) string { return rollbackTo(ref) })
	h := startAdvertising(t, model, widened)
	h.send(tokenProd, alert("KubePodCrashLooping", "aaa", orders))

	body := h.adviceComment("1")
	if !strings.Contains(body, "**Escalate:** the Executor of Location prod offers no Action that matches a reviewed declaration") || strings.Contains(body, "delete namespace") {
		t.Errorf("an unreviewed Action was offered:\n%s", body)
	}
}

// Advice follows the Incident lifecycle (#45) as the RCA does: it is never
// posted on a closed ticket.
func TestAdviceIsNotPostedOnATicketClosedWhileItWasDrafted(t *testing.T) {
	var h *harness
	model := remediate(t, func(_, ref string) string {
		h.tickets.closeByHuman("1", "not_planned")
		return rollbackTo(ref)
	})
	h = startAdvice(t, model)
	h.send(tokenProd, alert("KubePodCrashLooping", "aaa", orders))
	h.cp.Wait()

	if c := h.tickets.commentsOn("1"); len(c) != 1 || !strings.HasPrefix(c[0], "## Root-cause analysis") {
		t.Fatalf("comments %q, want the RCA only", c)
	}
	log := h.auditLog()
	cancelled := ofType(log, "remediation.cancelled")
	if len(cancelled) != 1 || !strings.Contains(string(cancelled[0].Data), "closed by a human (not_planned)") {
		t.Errorf("remediation.cancelled %+v, want one saying why", cancelled)
	}
	if n := len(ofType(log, "remediation.finished")); n != 1 {
		t.Errorf("%d remediation.finished, want 1: the loop ran to its end", n)
	}
}

// adviceBlockingLLM answers the RCA loop as next does, then blocks the
// remediation loop until its context is cancelled, after closing started.
type adviceBlockingLLM struct {
	next    llm.Client
	started chan struct{}
}

func (b *adviceBlockingLLM) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	if req.Tools[len(req.Tools)-1].Name != "submit_advice" {
		return b.next.Complete(ctx, req)
	}
	close(b.started)
	<-ctx.Done()
	return llm.Response{}, ctx.Err()
}

func TestClosingTheIncidentCancelsTheRemediationLoop(t *testing.T) {
	model := &adviceBlockingLLM{next: remediate(t, func(_, ref string) string { return rollbackTo(ref) }), started: make(chan struct{})}
	h := startAdvice(t, model)
	h.send(tokenProd, alert("KubePodCrashLooping", "aaa", orders))
	select {
	case <-model.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the remediation loop never asked the LLM")
	}

	h.tickets.closeByHuman("1", "completed")
	h.sweep()
	h.cp.Wait()

	if c := h.tickets.commentsOn("1"); len(c) != 1 || !strings.HasPrefix(c[0], "## Root-cause analysis") {
		t.Fatalf("comments %q, want the RCA only", c)
	}
	log := h.auditLog()
	cancelled := ofType(log, "remediation.cancelled")
	if len(cancelled) != 1 || cancelled[0].Incident != 1 || !strings.Contains(string(cancelled[0].Data), "closed by a human") {
		t.Fatalf("remediation.cancelled %+v, want one saying why", cancelled)
	}
	for _, typ := range []string{"remediation.finished", "gate1.advice", "advice.decided"} {
		if n := len(ofType(log, typ)); n != 0 {
			t.Errorf("%d %s after the cancel", n, typ)
		}
	}
}
