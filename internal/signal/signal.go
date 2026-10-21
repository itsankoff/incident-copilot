// Package signal defines the Signal, incident-copilot's source-neutral form of
// an Alert or a Detector finding (ADR 0002 D3), the Signal sources that send
// them, and how a Signal's labels resolve to a Target.
package signal

import (
	"encoding/json"
	"net/http"
	"time"
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
	Target      *Target           `json:"target,omitempty"` // nil when the labels name no workload
	Raw         json.RawMessage   `json:"raw"`
}

// Source is one configured sender of Signals.
type Source struct {
	Kind     string  `json:"kind"`
	Instance string  `json:"instance"`
	Token    string  `json:"token"`
	Location string  `json:"location"`
	Mapping  Mapping `json:"mapping"`
}

// Adapter turns one kind of Signal source's payload into Signals, after
// checking that the sender is who it claims to be.
type Adapter interface {
	// Authenticate returns the configured Source that sent r.
	Authenticate(r *http.Request) (Source, error)
	// Split splits a request body into its alerts.
	Split(body []byte) ([]json.RawMessage, error)
	// Map normalises one alert from src into a Signal received at now.
	Map(src Source, alert json.RawMessage, now time.Time) (Signal, error)
}
