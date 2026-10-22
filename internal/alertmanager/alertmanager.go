// Package alertmanager is the Source adapter for Prometheus Alertmanager's
// webhook receiver. Each configured Alertmanager has its own bearer token (two
// during rotation), which also tells the adapter which Signal source sent a
// request.
package alertmanager

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/itsankoff/incident-copilot/internal/signal"
)

// Adapter implements signal.Adapter for Alertmanager.
type Adapter struct {
	sources []signal.Source
}

func New(sources []signal.Source) *Adapter {
	return &Adapter{sources: sources}
}

// Authenticate matches the request's bearer token against every source's tokens.
func (a *Adapter) Authenticate(r *http.Request) (signal.Source, error) {
	tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || tok == "" {
		return signal.Source{}, errors.New("missing bearer token")
	}
	for _, s := range a.sources {
		for _, want := range s.Tokens {
			if want != "" && subtle.ConstantTimeCompare([]byte(tok), []byte(want)) == 1 {
				return s, nil
			}
		}
	}
	return signal.Source{}, errors.New("unknown bearer token")
}

// Split returns the alerts of one webhook payload, and its truncatedAlerts:
// how many alerts the receiver's max_alerts left out.
func (a *Adapter) Split(body []byte) ([]json.RawMessage, int, error) {
	var p struct {
		Alerts          []json.RawMessage `json:"alerts"`
		TruncatedAlerts int               `json:"truncatedAlerts"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, 0, err
	}
	return p.Alerts, p.TruncatedAlerts, nil
}

type amAlert struct {
	Status       string            `json:"status"`
	Labels       map[string]string `json:"labels"`
	Annotations  map[string]string `json:"annotations"`
	StartsAt     time.Time         `json:"startsAt"`
	EndsAt       time.Time         `json:"endsAt"`
	GeneratorURL string            `json:"generatorURL"`
	Fingerprint  string            `json:"fingerprint"`
}

// Map normalises one Alertmanager alert.
func (a *Adapter) Map(src signal.Source, raw json.RawMessage, now time.Time) (signal.Signal, error) {
	var al amAlert
	if err := json.Unmarshal(raw, &al); err != nil {
		return signal.Signal{}, err
	}
	if al.Fingerprint == "" {
		return signal.Signal{}, errors.New("alert has no fingerprint")
	}
	name := al.Labels["alertname"]
	if name == "" {
		return signal.Signal{}, errors.New("alert has no alertname")
	}
	var status signal.Status
	switch al.Status {
	case "firing":
		status = signal.Firing
	case "resolved":
		status = signal.Resolved
	default:
		return signal.Signal{}, errors.New("unknown alert status " + al.Status)
	}

	s := signal.Signal{
		Source:      src.Ref(),
		Key:         src.Instance + "/" + al.Fingerprint,
		Name:        name,
		Status:      status,
		SourceState: al.Status,
		SeverityRaw: al.Labels["severity"],
		StartsAt:    al.StartsAt,
		ReceivedAt:  now,
		Labels:      al.Labels,
		Annotations: al.Annotations,
		Link:        al.GeneratorURL,
		Raw:         raw,
	}
	switch sev := signal.Severity(strings.ToLower(s.SeverityRaw)); sev {
	case signal.Critical, signal.Warning, signal.Info:
		s.Severity = sev
	default:
		s.Severity = signal.Unknown
	}
	// Alertmanager's endsAt on a firing alert is a timeout, not an end (ADR 0002 D3).
	if status == signal.Resolved {
		s.EndsAt = &al.EndsAt
	}
	return s, nil
}
