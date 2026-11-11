package controlplane

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/itsankoff/incident-copilot/internal/audit"
	"github.com/itsankoff/incident-copilot/internal/evidence"
	"github.com/itsankoff/incident-copilot/internal/llm"
	"github.com/itsankoff/incident-copilot/internal/redact"
	"github.com/itsankoff/incident-copilot/internal/triage"
)

// PromptVersion names the RCA loop's prompts and tools. Change it whenever
// they change, so investigations stay comparable.
const PromptVersion = "rca-v2"

// Budget bounds one investigation. Running out still writes an RCA.
type Budget struct {
	Steps  int           // LLM requests
	Tokens int           // input plus output, over all requests
	Time   time.Duration // wall clock
}

// Investigator runs the RCA loop for an Incident and writes its RCA into the
// Incident's ticket (ADR 0002 D7).
type Investigator struct {
	llm      llm.Client
	model    string // configured; a router may answer with another
	budget   Budget
	evidence *Evidence
	tickets  Ticketing
	audit    *audit.Log
	logger   *slog.Logger // milestones, with names and counts only
	now      func() time.Time
}

func NewInvestigator(c llm.Client, model string, b Budget, ev *Evidence, tickets Ticketing, log *audit.Log, logger *slog.Logger, now func() time.Time) *Investigator {
	return &Investigator{llm: c, model: model, budget: b, evidence: ev, tickets: tickets, audit: log, logger: logger, now: now}
}

const submitTool = "submit_rca"

// noArgs is the schema of a tool that takes no arguments: the Evidence tools
// always read the Incident's Target over the default window.
var noArgs = json.RawMessage(`{"type":"object","properties":{}}`)

var evidenceTools = []llm.ToolSpec{
	{Name: "workload_state", Description: "The Target's spec, status, conditions, and owned pods.", Parameters: noArgs},
	{Name: "events", Description: "Kubernetes events for the Target and its pods over the last hour.", Parameters: noArgs},
}

var claimSchema = `{"type":"object","required":["id","statement","kind","cites"],"properties":{` +
	`"id":{"type":"string"},"statement":{"type":"string"},"kind":{"type":"string","enum":["observed","inferred"]},` +
	`"cites":{"type":"array","items":{"type":"string"},"description":"observed: Evidence ref callIds; inferred: ids of other claims"}}}`

var submitSpec = llm.ToolSpec{
	Name:        submitTool,
	Description: "Submit the root-cause analysis. This ends the investigation.",
	Parameters: json.RawMessage(`{"type":"object","required":["summary","confidence"],"properties":{` +
		`"summary":{"type":"array","items":` + claimSchema + `},` +
		`"confidence":{"type":"object","required":["level","claims"],"properties":{` +
		`"level":{"type":"string","enum":["low","medium","high"]},"claims":{"type":"array","items":` + claimSchema + `}}}}}`),
}

// systemPrompt lives in a file named for PromptVersion (CONTRIBUTING.md).
//
//go:embed prompts/rca-v2.txt
var systemPrompt string

// incidentBrief is all the model learns about the Incident: identifiers only,
// never Alert annotations (ADR 0003 D4). The source link is redacted before it
// is encoded, because encoding hides a pre-signed URL's parameters from the
// pass over the whole request.
func incidentBrief(inc triage.Incident) string {
	type sig struct {
		Name     string    `json:"name"`
		Severity string    `json:"severity"`
		StartsAt time.Time `json:"startsAt"`
		Source   string    `json:"source"`
		Link     string    `json:"link,omitempty"`
	}
	brief := struct {
		Target  any   `json:"target"`
		Signals []sig `json:"signals"`
	}{Target: inc.Target}
	for _, m := range inc.Members {
		brief.Signals = append(brief.Signals, sig{m.Name, string(m.Severity), m.StartsAt.UTC(), m.Source.Kind + "/" + m.Source.Instance, redact.String(m.Link)})
	}
	b, _ := json.Marshal(brief) // strings and times always marshal
	return "Investigate this Incident:\n" + string(b)
}

// ledgerEntry is one Evidence query this investigation made. Each was audited
// under the Incident before the model saw its ref, so the ledger is this
// Incident's share of the audit log.
type ledgerEntry struct {
	Ref  evidence.Ref
	Tool string
	OK   bool
}

// investigation is what the loop leaves behind.
type investigation struct {
	rca       *RCA // nil when no answer came before a budget or an error ended it
	ledger    []ledgerEntry
	stopped   string
	exhausted string   // the budget that ran out: steps, tokens, or time
	models    []string // the model that answered each request
	steps     int
	tokens    int
}

// Run investigates inc, runs Gate 1 on the answer, and comments the RCA on
// inc's ticket. When ctx is cancelled, because the Incident closed, it stops,
// audits the cause, and writes nothing to the ticket. It returns only audit
// and ticketing failures.
func (v *Investigator) Run(ctx context.Context, inc triage.Incident) error {
	start := v.now()
	if inc.Target == nil {
		// The Evidence tools read a Target; with none there is nothing to read.
		const reason = "the Incident has no Target"
		if err := v.audit.Append(start, "investigation.skipped", inc.ID, map[string]string{"reason": reason}); err != nil {
			return err
		}
		v.logger.Info("investigation.skipped", "incident", inc.ID, "reason", reason)
		return nil
	}
	if err := v.audit.Append(start, "investigation.started", inc.ID, map[string]any{
		"model": v.model, "promptVersion": PromptVersion, "budget": v.budget,
		"tools": toolNames(evidenceTools),
	}); err != nil {
		return err
	}
	v.logger.Info("investigation.started", "incident", inc.ID, "model", v.model,
		slog.Group("budget", "steps", v.budget.Steps, "tokens", v.budget.Tokens, "time", v.budget.Time.String()))
	inv, err := v.loop(ctx, inc)
	if err != nil {
		return err
	}
	if ctx.Err() != nil {
		return v.audit.Append(v.now(), "investigation.cancelled", inc.ID, map[string]any{
			"reason": context.Cause(ctx).Error(), "steps": inv.steps, "tokens": inv.tokens,
		})
	}
	failures := gate1(inv.rca, inv.ledger)
	// What the ticket shows: the submitted RCA, its failures, and the
	// confidence after Gate 1.
	if err := v.audit.Append(v.now(), "gate1.rca", inc.ID, map[string]any{
		"passed": len(failures) == 0, "failures": failures, "rca": inv.rca, "confidence": confidence(inv, failures),
	}); err != nil {
		return err
	}
	if err := v.audit.Append(v.now(), "investigation.finished", inc.ID, map[string]any{
		"model": v.model, "answeredBy": inv.models, "promptVersion": PromptVersion,
		"stopped": inv.stopped, "steps": inv.steps, "tokens": inv.tokens,
	}); err != nil {
		return err
	}
	duration := v.now().Sub(start).Round(time.Millisecond).String()
	if inv.exhausted != "" {
		v.logger.Info("investigation.exhausted", "incident", inc.ID, "budget", inv.exhausted,
			"confidence", confidence(inv, failures), "steps", inv.steps, "duration", duration)
	} else {
		v.logger.Info("investigation.finished", "incident", inc.ID,
			"confidence", confidence(inv, failures), "steps", inv.steps, "duration", duration)
	}
	// The ticket's state wins: an RCA is never posted on a closed ticket.
	closed, reason, err := v.tickets.State(ctx, inc.TicketRef)
	if err == nil && closed {
		return v.audit.Append(v.now(), "investigation.cancelled", inc.ID, map[string]any{
			"reason": fmt.Sprintf("ticket %s was closed by a human (%s) before the RCA was posted", inc.TicketRef, reason),
			"steps":  inv.steps, "tokens": inv.tokens,
		})
	}
	if err == nil {
		err = v.tickets.Comment(ctx, inc.TicketRef, v.render(inv, failures))
	}
	if err != nil {
		if ctx.Err() != nil {
			return v.audit.Append(v.now(), "investigation.cancelled", inc.ID, map[string]any{
				"reason": context.Cause(ctx).Error(), "steps": inv.steps, "tokens": inv.tokens,
			})
		}
		return err
	}
	if err := v.audit.Append(v.now(), "ticket.commented", inc.ID, map[string]string{"ref": inc.TicketRef, "kind": "rca"}); err != nil {
		return err
	}
	v.logger.Info("rca.posted", "incident", inc.ID, "ref", inc.TicketRef)
	ready := []any{"incident", inc.ID, "ref", inc.TicketRef}
	if url := v.tickets.URL(inc.TicketRef); url != "" {
		ready = append(ready, "url", url)
	}
	v.logger.Info("ticket.ready", ready...)
	return nil
}

// loop talks to the model until it submits an RCA or a budget runs out. It
// returns an error only when the audit log cannot be written.
func (v *Investigator) loop(ctx context.Context, inc triage.Incident) (investigation, error) {
	ctx, cancel := context.WithTimeout(ctx, v.budget.Time)
	defer cancel()
	var inv investigation
	msgs := []llm.Message{{Role: llm.System, Content: systemPrompt}, {Role: llm.User, Content: incidentBrief(inc)}}
	tools := append(slices.Clone(evidenceTools), submitSpec)

	for inv.rca == nil {
		switch {
		case inv.steps >= v.budget.Steps:
			inv.stopped = fmt.Sprintf("step budget of %d ran out", v.budget.Steps)
			inv.exhausted = "steps"
			return inv, nil
		case inv.tokens >= v.budget.Tokens:
			inv.stopped = fmt.Sprintf("token budget of %d ran out", v.budget.Tokens)
			inv.exhausted = "tokens"
			return inv, nil
		}
		inv.steps++
		req := llm.Request{Model: v.model, Messages: redacted(msgs), Tools: tools}
		if err := v.audit.Append(v.now(), "llm.request", inc.ID, map[string]any{
			"step": inv.steps, "model": v.model, "promptVersion": PromptVersion, "messages": auditMessages(req.Messages), "tools": toolNames(tools),
		}); err != nil {
			return inv, err
		}
		resp, err := v.llm.Complete(ctx, req)
		if err != nil {
			if aerr := v.audit.Append(v.now(), "llm.error", inc.ID, map[string]any{"step": inv.steps, "error": err.Error()}); aerr != nil {
				return inv, aerr
			}
			v.logger.Info("investigation.step", "incident", inc.ID, "step", inv.steps, "llm", v.model, "ok", false)
			inv.stopped = "the LLM request failed: " + err.Error()
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				inv.stopped = fmt.Sprintf("time budget of %s ran out", v.budget.Time)
				inv.exhausted = "time"
			}
			return inv, nil
		}
		if err := v.audit.Append(v.now(), "llm.response", inc.ID, map[string]any{
			"step": inv.steps, "model": v.model, "answeredBy": resp.Model, "finishReason": resp.FinishReason, "usage": resp.Usage, "message": resp.Message,
		}); err != nil {
			return inv, err
		}
		v.logger.Info("investigation.step", "incident", inc.ID, "step", inv.steps, "llm", resp.Model, "ok", true)
		inv.models = append(inv.models, resp.Model)
		inv.tokens += resp.Usage.InputTokens + resp.Usage.OutputTokens
		resp.Message.Role = llm.Assistant
		msgs = append(msgs, resp.Message)

		if len(resp.Message.ToolCalls) == 0 {
			msgs = append(msgs, llm.Message{Role: llm.User, Content: "Call submit_rca with your root-cause analysis."})
			continue
		}
		for _, tc := range resp.Message.ToolCalls {
			content, err := v.toolResult(ctx, inc, tc, &inv)
			if err != nil {
				return inv, err
			}
			msgs = append(msgs, llm.Message{Role: llm.Tool, ToolCallID: tc.ID, Content: content})
		}
	}
	inv.stopped = "the model submitted its RCA"
	return inv, nil
}

// toolResult runs one tool call and returns what the model reads back. It
// returns an error only when the audit log cannot be written.
func (v *Investigator) toolResult(ctx context.Context, inc triage.Incident, tc llm.ToolCall, inv *investigation) (string, error) {
	logStep := func(tool string, ok bool) {
		v.logger.Info("investigation.step", "incident", inc.ID, "step", inv.steps, "tool", tool, "ok", ok)
	}
	if tc.Name == submitTool {
		var r RCA
		if err := json.Unmarshal([]byte(tc.Arguments), &r); err != nil {
			logStep(submitTool, false)
			// The decoder's message names the field that did not match.
			b, _ := json.Marshal(map[string]string{"error": "arguments do not match the submit_rca schema: " + err.Error()})
			return string(b), nil
		}
		logStep(submitTool, true)
		inv.rca = &r
		return `{"ok":true}`, nil
	}
	if !slices.ContainsFunc(evidenceTools, func(t llm.ToolSpec) bool { return t.Name == tc.Name }) {
		logStep("(not offered)", false) // the name is the model's text, so it stays out
		return `{"error":"tool not offered"}`, nil
	}
	res, err := v.evidence.Query(ctx, inc.ID, evidence.Query{Tool: tc.Name, Target: *inc.Target})
	if errors.Is(err, errAudit) {
		return "", err
	}
	logStep(tc.Name, err == nil)
	inv.ledger = append(inv.ledger, ledgerEntry{Ref: res.Ref, Tool: tc.Name, OK: err == nil})
	if err != nil {
		// Redacted before framing, so a cut-off private key cannot take the
		// frame's end with it in the pass over the whole request.
		return frame(res.Ref, map[string]string{"error": redact.String(err.Error())}), nil
	}
	return frame(res.Ref, map[string]json.RawMessage{"data": res.Data}), nil
}

// frame is a tool result as the model reads it: the trusted Evidence ref,
// then what the tool read, labelled as untrusted data (ADR 0003 D4). The
// framed part is JSON from json.Marshal, which writes '<' as \u003c, so
// nothing inside can close the frame.
func frame(ref evidence.Ref, read any) string {
	r, _ := json.Marshal(map[string]evidence.Ref{"ref": ref}) // a Ref always marshals
	b, _ := json.Marshal(read)                                // so does redacted JSON
	return fmt.Sprintf("%s\n<untrusted-data>\n%s\n</untrusted-data>", r, b)
}

// redacted is msgs with the redactor run over every message's text and tool
// call arguments: the Control plane's pass before every LLM call (ADR 0003 D8).
func redacted(msgs []llm.Message) []llm.Message {
	out := slices.Clone(msgs)
	for i := range out {
		out[i].Content = redact.String(out[i].Content)
		out[i].ToolCalls = slices.Clone(out[i].ToolCalls)
		for j := range out[i].ToolCalls {
			// Arguments are JSON text, so they get the JSON pass, which keeps
			// them valid: some providers refuse a request whose arguments are not.
			out[i].ToolCalls[j].Arguments = string(redact.JSON(json.RawMessage(out[i].ToolCalls[j].Arguments)))
		}
	}
	return out
}

// auditMessages is msgs with each tool result replaced by its digest, since
// evidence.query already holds what the tool read.
func auditMessages(msgs []llm.Message) []llm.Message {
	out := slices.Clone(msgs)
	for i, m := range out {
		if m.Role == llm.Tool {
			sum := sha256.Sum256([]byte(m.Content))
			out[i].Content = fmt.Sprintf("sha256:%s bytes:%d", hex.EncodeToString(sum[:]), len(m.Content))
		}
	}
	return out
}

func toolNames(tools []llm.ToolSpec) []string {
	out := make([]string, len(tools))
	for i, t := range tools {
		out[i] = t.Name
	}
	return out
}

// render is the RCA as a ticket comment, with every Claim's citations.
func (v *Investigator) render(inv investigation, failures []gateFailure) string {
	var b strings.Builder
	b.WriteString("## Root-cause analysis\n\n")
	failed := map[string][]string{}
	for _, f := range failures {
		failed[f.Claim] = append(failed[f.Claim], f.Reason)
	}
	tools := map[string]string{}
	for _, e := range inv.ledger {
		tools[e.Ref.CallID] = e.Tool
	}
	claims := func(cs []Claim) {
		if len(cs) == 0 {
			b.WriteString("_No claims._\n")
		}
		for _, c := range cs {
			var cites []string
			for _, id := range c.Cites {
				if t, ok := tools[id]; ok && c.Kind == Observed {
					cites = append(cites, fmt.Sprintf("`%s` (%s)", id, t))
				} else {
					cites = append(cites, id)
				}
			}
			cited := "cites nothing"
			if len(cites) > 0 {
				cited = "cites " + strings.Join(cites, ", ")
			}
			fmt.Fprintf(&b, "- **%s** (%s): %s · %s\n", c.ID, c.Kind, c.Statement, cited)
			for _, r := range failed[c.ID] {
				fmt.Fprintf(&b, "  - ⚠ fails Gate 1: %s\n", r)
			}
		}
	}

	switch {
	case inv.rca == nil:
		fmt.Fprintf(&b, "**No RCA:** the investigation stopped before the model answered: %s.\n\n**Confidence:** low\n\n", inv.stopped)
	case len(failures) > 0:
		fmt.Fprintf(&b, "**Confidence:** low (capped from %q: claims below fail Gate 1)\n\n", inv.rca.Confidence.Level)
	default:
		fmt.Fprintf(&b, "**Confidence:** %s\n\n", inv.rca.Confidence.Level)
	}
	if inv.rca != nil {
		b.WriteString("### Summary\n\n")
		claims(inv.rca.Summary)
		b.WriteString("\n### Confidence\n\n")
		claims(inv.rca.Confidence.Claims)
		for _, r := range failed[""] {
			fmt.Fprintf(&b, "- ⚠ fails Gate 1: %s\n", r)
		}
	}
	b.WriteString("\n### Evidence\n\n")
	if len(inv.ledger) == 0 {
		b.WriteString("_No Evidence queries._\n")
	}
	for _, e := range inv.ledger {
		outcome := "ok"
		if !e.OK {
			outcome = "unavailable"
		}
		fmt.Fprintf(&b, "- `%s`: %s on `%s`, %s to %s, %s\n", e.Ref.CallID, e.Tool, e.Ref.Target,
			e.Ref.Window.Start.UTC().Format(time.RFC3339), e.Ref.Window.End.UTC().Format(time.RFC3339), outcome)
	}
	answered := slices.Compact(slices.Clone(inv.models))
	fmt.Fprintf(&b, "\nModel `%s` (answered by %s) · prompt %s · %d steps, %d tokens · %s\n",
		v.model, codeList(answered), PromptVersion, inv.steps, inv.tokens, inv.stopped)
	return b.String()
}

// confidence is the RCA's level after Gate 1: low without an answer or when
// any check fails.
func confidence(inv investigation, failures []gateFailure) string {
	if inv.rca == nil || len(failures) > 0 {
		return "low"
	}
	return inv.rca.Confidence.Level
}

func codeList(ss []string) string {
	if len(ss) == 0 {
		return "none"
	}
	return "`" + strings.Join(ss, "`, `") + "`"
}
