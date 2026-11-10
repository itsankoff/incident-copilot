package controlplane

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/itsankoff/incident-copilot/internal/action"
	"github.com/itsankoff/incident-copilot/internal/llm"
	"github.com/itsankoff/incident-copilot/internal/signal"
	"github.com/itsankoff/incident-copilot/internal/triage"
)

// AdvicePromptVersion names the remediation loop's prompts and tools. Change
// it whenever they change.
const AdvicePromptVersion = "advice-v1"

//go:embed prompts/advice-v1.txt
var advicePrompt string

// Executors tells the Control plane what each Location's Executor offers;
// *link.Hub implements it.
type Executors interface {
	// Catalog returns the Actions location's Executor offers. ok is false
	// while that Executor is not linked.
	Catalog(location string) (actions []action.Declaration, ok bool)
}

// KillSwitchStore holds the kill switch; *store.Store implements it.
type KillSwitchStore interface {
	KillSwitch(ctx context.Context) (string, error) // a level, or "" when unset
	SetKillSwitch(ctx context.Context, level string) error
}

// Remediation is what the remediation loop and Decide read besides the LLM.
type Remediation struct {
	Executors Executors
	// Reviewed are the Actions this build's code declares. An Executor's
	// advertised Action is offered only when it matches one exactly, so a
	// widened bound or an unknown Action on the link is never offered (ADR
	// 0003 D2).
	Reviewed   []action.Declaration
	Policy     Policy
	KillSwitch KillSwitchStore
}

// Advice is the remediation the model drafts from the RCA: an ordered list of
// steps, each an Action an Executor offers. No steps means escalate to a
// human, and Reason says why.
type Advice struct {
	Steps  []Step `json:"steps"`
	Reason string `json:"reason"`
}

// Step is one step of Advice (ADR 0003 D6).
type Step struct {
	Executor string `json:"executor"`
	Action   string `json:"action"`
	// Target is set by the Control plane from the Incident; whatever the
	// model wrote there is overwritten.
	Target   *signal.Target      `json:"target"`
	Args     json.RawMessage     `json:"args"`
	Cites    []string            `json:"cites"`    // RCA claim ids or Evidence ref callIds
	ArgCites map[string][]string `json:"argCites"` // per argument, the Evidence refs its value comes from
}

// stepFailure is one reason Advice fails Gate 1. Step counts from 1.
type stepFailure struct {
	Step   int    `json:"step"`
	Reason string `json:"reason"`
}

// gate1Advice is Gate 1 for Advice (ADR 0003 D4): each step names an Executor
// offered for this Incident and an Action it offers, its args pass the
// Action's schema, its target is the Incident's Target, it cites RCA claims
// or this Incident's Evidence, and each argument cites the Evidence its value
// comes from. offered holds only the Executor of the Incident's Location.
func gate1Advice(a Advice, inc triage.Incident, offered map[string][]action.Declaration, rca *RCA, ledger []ledgerEntry) []stepFailure {
	claims := map[string]bool{}
	if rca != nil {
		for _, c := range append(slices.Clone(rca.Summary), rca.Confidence.Claims...) {
			claims[c.ID] = true
		}
	}
	queries := map[string]bool{} // callId → succeeded
	for _, e := range ledger {
		queries[e.Ref.CallID] = e.OK
	}

	var out []stepFailure
	for i, s := range a.Steps {
		fail := func(format string, args ...any) {
			out = append(out, stepFailure{i + 1, fmt.Sprintf(format, args...)})
		}
		switch {
		case inc.Target == nil:
			fail("the Incident has no Target")
		case s.Target == nil:
			fail("the step has no target")
		case *s.Target != *inc.Target:
			fail("target %s is not the Incident's Target %s", s.Target, inc.Target)
		}

		d, found := declaration(offered, s)
		switch _, linked := offered[s.Executor]; {
		case !linked:
			fail("Executor %q is not offered for this Incident", s.Executor)
		case !found:
			fail("Action %q is not offered by Executor %q", s.Action, s.Executor)
		default:
			if inc.Target != nil && !d.Accepts(inc.Target.Kind) {
				fail("%s does not accept Target kind %s", d.Name, inc.Target.Kind)
			}
			for _, r := range d.CheckArgs(s.Args) {
				fail("%s", r)
			}
		}

		if len(s.Cites) == 0 {
			fail("cites nothing")
		}
		for _, id := range s.Cites {
			if !claims[id] {
				if p := queryProblem(queries, id, "neither a claim of the RCA nor an Evidence query of this Incident"); p != "" {
					fail("cites %s, %s", id, p)
				}
			}
		}

		// Every argument is a value other than the Target's identity, so each
		// cites the Evidence it comes from (ADR 0003 D4).
		var args map[string]json.RawMessage
		_ = json.Unmarshal(s.Args, &args) // a bad shape is reported by CheckArgs
		for _, name := range sortedKeys(args) {
			cites := s.ArgCites[name]
			if len(cites) == 0 {
				fail("argument %q cites no evidence", name)
			}
			for _, id := range cites {
				if p := queryProblem(queries, id, "not an Evidence query of this Incident"); p != "" {
					fail("argument %q cites %s, %s", name, id, p)
				}
			}
		}
		for _, name := range sortedKeys(s.ArgCites) {
			if _, ok := args[name]; !ok {
				fail("cites evidence for argument %q, which the step does not have", name)
			}
		}
	}
	return out
}

// declaration is the offered declaration of s's Action.
func declaration(offered map[string][]action.Declaration, s Step) (action.Declaration, bool) {
	catalog := offered[s.Executor]
	i := slices.IndexFunc(catalog, func(d action.Declaration) bool { return d.Name == s.Action })
	if i < 0 {
		return action.Declaration{}, false
	}
	return catalog[i], true
}

// queryProblem says why id is not a successful Evidence query of this
// Incident (unknown completes "which is …"), or "" when it is one.
func queryProblem(queries map[string]bool, id, unknown string) string {
	succeeded, isQuery := queries[id]
	switch {
	case !isQuery:
		return "which is " + unknown
	case !succeeded:
		return "whose Evidence query failed"
	}
	return ""
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

var submitAdviceSpec = llm.ToolSpec{
	Name:        "submit_advice",
	Description: "Submit the Advice. This ends the remediation loop.",
	Parameters: json.RawMessage(`{"type":"object","required":["steps","reason"],"properties":{` +
		`"steps":{"type":"array","description":"empty to escalate to a human","items":{"type":"object","required":["executor","action","args","cites","argCites"],"properties":{` +
		`"executor":{"type":"string"},"action":{"type":"string"},` +
		`"args":{"type":"object","description":"exactly the Action's arguments"},` +
		`"cites":{"type":"array","items":{"type":"string"},"description":"RCA claim ids or Evidence ref callIds"},` +
		`"argCites":{"type":"object","additionalProperties":{"type":"array","items":{"type":"string"}},"description":"per argument, the Evidence ref callIds its value was read from"}}}},` +
		`"reason":{"type":"string","description":"why these steps; or why escalate, such as why no offered Action fits"}}}`),
}

// adviceBrief is what the remediation loop starts from: the Target, the RCA
// after Gate 1, the Evidence refs it may cite, and the offered Actions.
func adviceBrief(inc triage.Incident, rca *RCA, conf string, ledger []ledgerEntry, offered map[string][]action.Declaration) string {
	type ref struct {
		CallID string `json:"callId"`
		Tool   string `json:"tool"`
		OK     bool   `json:"ok"`
	}
	type executor struct {
		Name    string               `json:"name"`
		Actions []action.Declaration `json:"actions"`
	}
	brief := struct {
		Target     *signal.Target `json:"target"`
		RCA        *RCA           `json:"rca"`
		Confidence string         `json:"confidenceAfterGate1"`
		Evidence   []ref          `json:"evidence"`
		Executors  []executor     `json:"executors"`
	}{Target: inc.Target, RCA: rca, Confidence: conf}
	for _, e := range ledger {
		brief.Evidence = append(brief.Evidence, ref{e.Ref.CallID, e.Tool, e.OK})
	}
	for _, name := range sortedKeys(offered) {
		brief.Executors = append(brief.Executors, executor{name, offered[name]})
	}
	b, _ := json.Marshal(brief) // plain data always marshals
	return "Draft Advice for this Incident:\n" + string(b)
}

// offered is the reviewed part of the catalog of the Executor in the
// Incident's Location, keyed by its name, the Location's: Executors elsewhere
// cannot act on the Target. When nothing is offered, why says so.
func (v *Investigator) offered(inc triage.Incident) (offered map[string][]action.Declaration, why string) {
	if inc.Target == nil {
		return nil, "the Incident has no Target, so no Action can be aimed at it"
	}
	var advertised []action.Declaration
	linked := false
	if v.rem.Executors != nil {
		advertised, linked = v.rem.Executors.Catalog(inc.Target.Location)
	}
	if !linked {
		return nil, fmt.Sprintf("the Executor of Location %s is not linked, so no Actions are offered", inc.Target.Location)
	}
	var catalog []action.Declaration
	for _, d := range advertised {
		if slices.ContainsFunc(v.rem.Reviewed, func(r action.Declaration) bool { return reflect.DeepEqual(r, d) }) {
			catalog = append(catalog, d)
		}
	}
	if len(catalog) == 0 {
		return nil, fmt.Sprintf("the Executor of Location %s offers no Action that matches a reviewed declaration", inc.Target.Location)
	}
	return map[string][]action.Declaration{inc.Target.Location: catalog}, ""
}

// killSwitch reads the kill switch afresh. Any failure to read it caps at L1.
func (v *Investigator) killSwitch(ctx context.Context) KillSwitch {
	if v.rem.KillSwitch == nil {
		return KillSwitch{Problem: "no kill switch store is configured"}
	}
	s, err := v.rem.KillSwitch.KillSwitch(ctx)
	if err != nil {
		return KillSwitch{Problem: err.Error()}
	}
	if s == "" {
		return KillSwitch{}
	}
	l, err := ParseLevel(s)
	if err != nil {
		return KillSwitch{Problem: err.Error()}
	}
	return KillSwitch{Set: true, Level: l}
}

// advise runs the remediation loop on the RCA, checks its Advice with Gate 1,
// decides its Autonomy level, and comments it on the ticket. Nothing runs: at
// L1 the ticket shows the steps to carry out by hand. It returns only audit
// and ticketing failures.
func (v *Investigator) advise(ctx context.Context, inc triage.Incident, rca *RCA, rcaLedger []ledgerEntry, conf string) error {
	offered, skip := v.offered(inc)
	var adv Advice
	var run *loopRun
	if skip == "" && rca == nil {
		skip = "there is no RCA to draft Advice from"
	}
	if skip != "" {
		adv.Reason = skip
		if err := v.audit.Append(v.now(), "remediation.skipped", inc.ID, map[string]string{"reason": skip}); err != nil {
			return err
		}
	} else {
		names := map[string][]string{}
		for ex, cat := range offered {
			for _, d := range cat {
				names[ex] = append(names[ex], d.Name)
			}
		}
		if err := v.audit.Append(v.now(), "remediation.started", inc.ID, map[string]any{
			"model": v.model, "promptVersion": AdvicePromptVersion, "budget": v.budget,
			"tools": toolNames(evidenceTools), "offered": names,
		}); err != nil {
			return err
		}
		var submitted Advice
		r, err := v.loop(ctx, inc, loopSpec{
			promptVersion: AdvicePromptVersion, system: advicePrompt, brief: adviceBrief(inc, rca, conf, rcaLedger, offered),
			submit: submitAdviceSpec, submission: "Advice", nudge: "Call submit_advice with your Advice.",
			accept: func(arguments string) error {
				var a Advice
				if err := json.Unmarshal([]byte(arguments), &a); err != nil {
					return err
				}
				submitted = a
				return nil
			},
		})
		if err != nil {
			return err
		}
		run = &r
		if err := v.audit.Append(v.now(), "remediation.finished", inc.ID, map[string]any{
			"model": v.model, "answeredBy": r.models, "promptVersion": AdvicePromptVersion,
			"stopped": r.stopped, "steps": r.steps, "tokens": r.tokens,
		}); err != nil {
			return err
		}
		adv = submitted
		if !r.submitted {
			adv = Advice{Reason: "the remediation loop stopped before the model answered: " + r.stopped}
		}
	}

	// The Control plane, never the model, names each step's target.
	for i := range adv.Steps {
		adv.Steps[i].Target = inc.Target
	}
	ledger := slices.Clone(rcaLedger)
	if run != nil {
		ledger = append(ledger, run.ledger...)
	}
	failures := gate1Advice(adv, inc, offered, rca, ledger)
	if err := v.audit.Append(v.now(), "gate1.advice", inc.ID, map[string]any{
		"passed": len(failures) == 0, "failures": failures, "advice": adv,
	}); err != nil {
		return err
	}

	guards := []Guard{
		{Name: "gate1", Passed: len(failures) == 0},
		// STUB: plan-level limits (the number of steps, the same Action
		// repeated on the Target) are a guard to be specified (ADR 0003
		// Guards); until then this one always passes.
		{Name: "plan_limits", Passed: true},
		// Approval is #58: until it exists, nothing may go above L1.
		{Name: "approval", Reason: "approval is not built yet (#58)"},
	}
	if !guards[0].Passed {
		guards[0].Reason = "the Advice fails Gate 1"
	}
	env := ""
	if inc.Target != nil {
		env = v.evidence.environment(inc.Target.Location)
	}
	// Preconditions are checked on fresh state by the Executor before each
	// step it runs (#57). At L1 nothing runs, and the ticket lists them for
	// whoever carries the steps out.
	in := DecideInput{Incident: inc, Environment: env, Policy: v.rem.Policy, KillSwitch: v.killSwitch(ctx), Confidence: conf, Guards: guards}
	d := Decide(in)
	if err := v.audit.Append(v.now(), "advice.decided", inc.ID, map[string]any{
		"level": d.Level, "caps": d.Caps,
		"inputs": map[string]any{"environment": in.Environment, "killSwitch": in.KillSwitch, "confidence": in.Confidence, "guards": guards},
	}); err != nil {
		return err
	}

	if err := v.tickets.Comment(ctx, inc.TicketRef, v.renderAdvice(adv, failures, d, offered, ledger, run)); err != nil {
		return err
	}
	return v.audit.Append(v.now(), "ticket.commented", inc.ID, map[string]string{"ref": inc.TicketRef, "kind": "advice"})
}

var levelMeaning = map[Level]string{
	L0: "observe: the Advice is recorded in the audit log only",
	L1: "suggest: carry out the steps below by hand; nothing runs on its own",
	L2: "approve: the Plan waits for approval of its exact version",
}

// renderAdvice is the Advice as a ticket comment: its level and every cap,
// each step with its citations and Gate 1 failures, and, at L1 for Advice that
// passed Gate 1, the steps to carry out by hand.
func (v *Investigator) renderAdvice(adv Advice, failures []stepFailure, d Decision, offered map[string][]action.Declaration, ledger []ledgerEntry, run *loopRun) string {
	var b strings.Builder
	b.WriteString("## Advice\n\n")
	fmt.Fprintf(&b, "**Autonomy level:** %s (%s)\n\n", d.Level, levelMeaning[d.Level])
	for _, c := range d.Caps {
		verb := "capped at"
		if c.Level > d.Level {
			verb = "allows up to"
		}
		fmt.Fprintf(&b, "- %s %s: %s\n", verb, c.Level, c.Reason)
	}
	b.WriteString("\n")
	if len(failures) > 0 {
		b.WriteString("**Not offered for approval:** the Advice fails Gate 1 (below). What happens to such Advice is not decided yet.\n\n")
	}
	reason := adv.Reason
	if reason == "" {
		reason = "_no reason given_"
	}
	if len(adv.Steps) == 0 {
		fmt.Fprintf(&b, "**Escalate:** %s\n", reason)
	} else {
		fmt.Fprintf(&b, "**Reason:** %s\n", reason)
	}

	tools := map[string]string{}
	for _, e := range ledger {
		tools[e.Ref.CallID] = e.Tool
	}
	cite := func(ids []string) string {
		var out []string
		for _, id := range ids {
			if t, ok := tools[id]; ok {
				out = append(out, fmt.Sprintf("`%s` (%s)", id, t))
			} else {
				out = append(out, id)
			}
		}
		if len(out) == 0 {
			return "nothing"
		}
		return strings.Join(out, ", ")
	}
	failed := map[int][]string{}
	for _, f := range failures {
		failed[f.Step] = append(failed[f.Step], f.Reason)
	}
	if len(adv.Steps) > 0 && d.Level > L0 {
		b.WriteString("\n### Steps\n\n")
		for i, s := range adv.Steps {
			args := string(s.Args)
			if args == "" {
				args = "{}"
			}
			target := "no Target"
			if s.Target != nil {
				target = "`" + s.Target.String() + "`"
			}
			fmt.Fprintf(&b, "%d. `%s` on %s by Executor `%s` · args `%s` · cites %s\n", i+1, s.Action, target, s.Executor, args, cite(s.Cites))
			for _, name := range sortedKeys(s.ArgCites) {
				fmt.Fprintf(&b, "   - %s from %s\n", name, cite(s.ArgCites[name]))
			}
			for _, r := range failed[i+1] {
				fmt.Fprintf(&b, "   - ⚠ fails Gate 1: %s\n", r)
			}
			if d.Level == L1 && len(failures) == 0 {
				dec, _ := declaration(offered, s) // it passed Gate 1
				fmt.Fprintf(&b, "   - By hand, in Location %s, once these hold: %s\n", s.Target.Location, strings.Join(dec.Preconditions, ", "))
				fmt.Fprintf(&b, "     `%s`\n", dec.ByHandFor(*s.Target, s.Args))
			}
		}
	}
	if run != nil {
		b.WriteString("\n### Evidence read for the Advice\n\n")
		if len(run.ledger) == 0 {
			b.WriteString("_No Evidence queries._\n")
		}
		for _, e := range run.ledger {
			outcome := "ok"
			if !e.OK {
				outcome = "unavailable"
			}
			fmt.Fprintf(&b, "- `%s`: %s on `%s`, %s\n", e.Ref.CallID, e.Tool, e.Ref.Target, outcome)
		}
		answered := slices.Compact(slices.Clone(run.models))
		fmt.Fprintf(&b, "\nModel `%s` (answered by %s) · prompt %s · %d steps, %d tokens · %s\n",
			v.model, codeList(answered), AdvicePromptVersion, run.steps, run.tokens, run.stopped)
	}
	return b.String()
}
