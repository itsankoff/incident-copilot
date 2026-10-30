package controlplane

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/itsankoff/incident-copilot/internal/audit"
	"github.com/itsankoff/incident-copilot/internal/evidence"
)

// Link is how the Control plane reaches Sensors. *link.Hub is the HTTP
// stand-in; the real protocol replaces it behind this interface.
type Link interface {
	// Call carries one Call to location's Sensor and waits for its Answer.
	Call(ctx context.Context, location string, c evidence.Call) (evidence.Answer, error)
	Online(location string) bool
}

// Evidence runs Evidence queries: it routes each to the Sensor of its Target's
// Location and audits it under its Evidence ref.
type Evidence struct {
	locations []Location
	link      Link
	audit     *audit.Log
	now       func() time.Time
}

func NewEvidence(locations []Location, l Link, log *audit.Log, now func() time.Time) *Evidence {
	return &Evidence{locations: locations, link: l, audit: log, now: now}
}

// defaultWindow is what a query reads when it names no window.
const defaultWindow = time.Hour

// Query runs q for incident (0 outside any Incident). The audit entry holds
// the ref and a digest of the answer, never the answer: nothing redacts it yet
// (#49), and secrets never reach the audit log.
func (e *Evidence) Query(ctx context.Context, incident int64, q evidence.Query) (evidence.Result, error) {
	now := e.now()
	if q.Window.End.IsZero() {
		q.Window.End = now
	}
	if q.Window.Start.IsZero() {
		q.Window.Start = q.Window.End.Add(-defaultWindow)
	}
	ref := evidence.Ref{CallID: rand.Text(), Source: "sensor", Location: q.Target.Location, Target: q.Target, Window: q.Window}

	ctx, cancel := context.WithTimeout(ctx, evidence.CallDeadline)
	defer cancel()
	answer, err := e.link.Call(ctx, ref.Location, evidence.Call{ID: ref.CallID, Query: q})
	if err == nil && answer.Error != "" {
		err = fmt.Errorf("%s: %s", q.Tool, answer.Error)
	}

	entry := map[string]any{"ref": ref, "tool": q.Tool, "outcome": "ok"}
	if err != nil {
		entry["outcome"], entry["error"] = "error", err.Error()
	} else {
		sum := sha256.Sum256(answer.Data)
		entry["bytes"], entry["sha256"] = len(answer.Data), hex.EncodeToString(sum[:])
	}
	if aerr := e.audit.Append(e.now(), "evidence.query", incident, entry); aerr != nil {
		return evidence.Result{}, aerr
	}
	if err != nil {
		return evidence.Result{}, err
	}
	return evidence.Result{Ref: ref, Data: answer.Data}, nil
}

// LocationStatus is a configured Location and whether its Sensor is linked.
type LocationStatus struct {
	Name        string `json:"name"`
	Environment string `json:"environment"`
	Online      bool   `json:"online"`
}

// Locations reports every configured Location, in configuration order.
func (e *Evidence) Locations() []LocationStatus {
	out := make([]LocationStatus, 0, len(e.locations))
	for _, l := range e.locations {
		out = append(out, LocationStatus{l.Name, l.Environment, e.link.Online(l.Name)})
	}
	return out
}

// DebugHandler lets an operator run Evidence queries by hand until the RCA
// loop does (#48). It has no authentication, so main serves it on loopback
// only, away from the port Signal sources and Sensors reach.
func (e *Evidence) DebugHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /debug/evidence", func(w http.ResponseWriter, r *http.Request) {
		var q evidence.Query
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody)).Decode(&q); err != nil {
			http.Error(w, "unparseable body", http.StatusBadRequest)
			return
		}
		res, err := e.Query(r.Context(), 0, q)
		switch {
		case errors.Is(err, evidence.ErrOffline):
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
		case err != nil:
			http.Error(w, err.Error(), http.StatusBadGateway)
		default:
			writeJSON(w, res)
		}
	})
	mux.HandleFunc("GET /debug/locations", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, e.Locations())
	})
	return mux
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
