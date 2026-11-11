// Package demorun is the logic of the make demo runner (cmd/demo): it renders
// the Control plane's and the Sensor's milestone log lines, reports what the
// demo cluster and its Alertmanager do, and follows one Incident to its RCA.
package demorun

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Milestone is one step of an Incident's life, or of the Sensor link, as the
// Control plane or the Sensor logs it: a JSON slog line whose msg is the
// matching audit event type (#89).
type Milestone struct {
	Time     time.Time
	Msg      string
	Incident int64
	Fields   map[string]any
}

// milestones are the msg values the runner renders.
var milestones = map[string]bool{
	"signal.received": true, "triage.decision": true,
	"ticket.created": true, "ticket.commented": true,
	"investigation.started": true, "investigation.step": true,
	"investigation.finished": true, "investigation.exhausted": true,
	"rca.posted": true, "ticket.ready": true,
	"link.up": true, "link.down": true,
}

// ParseMilestone parses one log line, and reports false for anything that is
// not a milestone.
func ParseMilestone(line []byte) (Milestone, bool) {
	var fields map[string]any
	if json.Unmarshal(line, &fields) != nil {
		return Milestone{}, false
	}
	msg, _ := fields["msg"].(string)
	if !milestones[msg] {
		return Milestone{}, false
	}
	m := Milestone{Msg: msg, Fields: fields}
	if s, ok := fields["time"].(string); ok {
		m.Time, _ = time.Parse(time.RFC3339Nano, s)
	}
	if n, ok := fields["incident"].(float64); ok {
		m.Incident = int64(n)
	}
	return m, true
}

// Str returns field k as text, or "".
func (m Milestone) Str(k string) string {
	switch v := m.Fields[k].(type) {
	case nil:
		return ""
	case string:
		return v
	case float64:
		return fmt.Sprint(int64(v))
	default:
		return fmt.Sprint(v)
	}
}

// String is the line the runner shows for m.
func (m Milestone) String() string {
	switch m.Msg {
	case "signal.received":
		return fmt.Sprintf("Control plane received Signal %s from %s about %s", m.Str("name"), m.Str("source"), m.Str("target"))
	case "triage.decision":
		if m.Str("decision") == "join" {
			return fmt.Sprintf("Triage joined the Signal to open Incident %d", m.Incident)
		}
		return fmt.Sprintf("Triage opened Incident %d", m.Incident)
	case "ticket.created":
		return fmt.Sprintf("Ticket %s created (%s ticketing)", m.Str("ref"), m.Str("adapter"))
	case "ticket.commented":
		return fmt.Sprintf("Ticket %s commented: %s (%s ticketing)", m.Str("ref"), m.Str("kind"), m.Str("adapter"))
	case "investigation.started":
		return fmt.Sprintf("Investigation started with %s (budget: %s)", m.Str("model"), m.budget())
	case "investigation.step":
		return m.step()
	case "investigation.finished":
		return fmt.Sprintf("Investigation finished: confidence %s after %s steps in %s", m.Str("confidence"), m.Str("steps"), m.duration("duration"))
	case "investigation.exhausted":
		return fmt.Sprintf("Investigation ran out of budget (%s)", m.Str("reason"))
	case "rca.posted":
		return "RCA written to ticket " + m.Str("ref")
	case "ticket.ready":
		return fmt.Sprintf("Ticket %s is ready for review", m.Str("ref"))
	case "link.up":
		return "Sensor link up: the Sensor dialed out to the Control plane"
	case "link.down":
		return fmt.Sprintf("Sensor link down (%s)", m.Str("error"))
	}
	return m.Msg
}

func (m Milestone) step() string {
	verdict := func(ok, failed string) string {
		if v, _ := m.Fields["ok"].(bool); v {
			return ok
		}
		return failed
	}
	prefix := "step " + m.Str("step") + ": "
	if tool := m.Str("tool"); tool != "" {
		return prefix + "Evidence query " + tool + " " + verdict("ok", "failed")
	}
	if model, ok := m.Fields["llm"].(string); ok && model != "" {
		return prefix + "LLM " + model + " " + verdict("answered", "failed")
	}
	return prefix + "LLM " + verdict("answered", "request failed")
}

// budget renders the Control plane's Budget, which slog writes as an object.
func (m Milestone) budget() string {
	b, ok := m.Fields["budget"].(map[string]any)
	if !ok {
		return m.Str("budget")
	}
	sub := Milestone{Fields: b}
	return fmt.Sprintf("%s steps, %s tokens, %s", sub.Str("Steps"), sub.Str("Tokens"), sub.duration("Time"))
}

// duration renders a duration that slog writes as nanoseconds, or as text.
func (m Milestone) duration(k string) string {
	if n, ok := m.Fields[k].(float64); ok {
		return time.Duration(n).Round(100 * time.Millisecond).String()
	}
	return strings.TrimSpace(m.Str(k))
}
