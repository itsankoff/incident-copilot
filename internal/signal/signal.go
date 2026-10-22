// Package signal defines the Signal, incident-copilot's source-neutral form of
// an Alert or a Detector finding (ADR 0002 D3), the Signal sources that send
// them, and how a Signal's labels resolve to a Target.
package signal

import (
	"encoding/json"
	"maps"
	"net/http"
	"time"
	"unicode/utf8"
)

type Status string

const (
	Firing   Status = "firing"
	Resolved Status = "resolved"
)

type Severity string

const (
	Critical Severity = "critical"
	Warning  Severity = "warning"
	Info     Severity = "info"
	Unknown  Severity = "unknown"
)

// SourceRef names the Signal source a Signal came from.
type SourceRef struct {
	Kind     string `json:"kind"`     // e.g. alertmanager
	Instance string `json:"instance"` // e.g. prod-am
}

// Signal is one Alert or Detector finding, normalised.
type Signal struct {
	Source      SourceRef         `json:"source"`
	Key         string            `json:"key"` // <source instance>/<series id>
	Name        string            `json:"name"`
	Status      Status            `json:"status"`
	SourceState string            `json:"sourceState"`
	Severity    Severity          `json:"severity"`
	SeverityRaw string            `json:"severityRaw,omitempty"`
	StartsAt    time.Time         `json:"startsAt"`
	EndsAt      *time.Time        `json:"endsAt,omitempty"`
	ReceivedAt  time.Time         `json:"receivedAt"` // the Control plane's clock
	Labels      map[string]string `json:"labels"`
	Annotations map[string]string `json:"annotations,omitempty"` // never reaches the LLM or a ticket
	Link        string            `json:"link,omitempty"`
	Target      *Target           `json:"target,omitempty"` // set by Detectors, or resolved by the Control plane from labels; nil when none
	Raw         json.RawMessage   `json:"raw"`
	Truncated   bool              `json:"truncated,omitempty"` // a value or raw was cut to the ingest bounds
}

// Ingest bounds (ADR 0002 D3), in bytes.
const (
	MaxValue = 4 << 10  // a label or annotation value
	MaxRaw   = 64 << 10 // raw
)

// Bound cuts s to the ingest bounds and marks it truncated if anything was cut.
// An oversized raw becomes a JSON string holding as much of it as fits, so it
// stays valid JSON.
func Bound(s Signal) Signal {
	var cutL, cutA bool
	s.Labels, cutL = cutValues(s.Labels)
	s.Annotations, cutA = cutValues(s.Annotations)
	s.Truncated = cutL || cutA
	if len(s.Name) > MaxValue { // the name is a label value
		s.Name = cut(s.Name, MaxValue)
	}
	if len(s.Raw) > MaxRaw {
		n := MaxRaw
		for {
			b, _ := json.Marshal(cut(string(s.Raw), n)) // a string always marshals
			if len(b) <= MaxRaw {
				s.Raw = b
				break
			}
			n = n * MaxRaw / len(b) // escaping made it longer; shrink in proportion
		}
		s.Truncated = true
	}
	return s
}

// cutValues returns m with every value over MaxValue cut, copying m only if
// one is.
func cutValues(m map[string]string) (map[string]string, bool) {
	var out map[string]string
	for k, v := range m {
		if len(v) <= MaxValue {
			continue
		}
		if out == nil {
			out = maps.Clone(m)
		}
		out[k] = cut(v, MaxValue)
	}
	if out == nil {
		return m, false
	}
	return out, true
}

// cut returns at most the first n bytes of s (n < len(s)), never splitting a
// UTF-8 sequence.
func cut(s string, n int) string {
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// Source is one configured sender of Signals.
type Source struct {
	Kind     string
	Instance string
	Tokens   []string // one, or two during rotation (ADR 0003 D9)
	Location string
	Mapping  Mapping
}

func (s Source) Ref() SourceRef { return SourceRef{Kind: s.Kind, Instance: s.Instance} }

// Adapter turns one kind of Signal source's payload into Signals, after
// checking that the sender is who it claims to be.
type Adapter interface {
	// Authenticate returns the configured Source that sent r.
	Authenticate(r *http.Request) (Source, error)
	// Split splits a request body into its alerts. truncated is how many
	// alerts the source says it left out of the body.
	Split(body []byte) (alerts []json.RawMessage, truncated int, err error)
	// Map normalises one alert from src into a Signal received at now.
	Map(src Source, alert json.RawMessage, now time.Time) (Signal, error)
}
