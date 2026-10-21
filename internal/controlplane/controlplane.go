// Package controlplane is the Control plane's Signal path: it authenticates
// Alerts, turns them into Signals, runs Triage, keeps Incidents in the store,
// writes one ticket per Incident, and audits every step.
package controlplane

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/itsankoff/incident-copilot/internal/audit"
	"github.com/itsankoff/incident-copilot/internal/signal"
	"github.com/itsankoff/incident-copilot/internal/store"
	"github.com/itsankoff/incident-copilot/internal/triage"
)

// Ticketing writes tickets. A ticket's reference is whatever Create returns.
type Ticketing interface {
	Create(ctx context.Context, title, body string) (ref string, err error)
	Comment(ctx context.Context, ref, body string) error
}

type Server struct {
	alertmanager signal.Adapter
	store        *store.Store
	tickets      Ticketing
	audit        *audit.Log
	now          func() time.Time

	mu sync.Mutex // serialises Triage over the stored Incidents
}

func New(alertmanager signal.Adapter, st *store.Store, tickets Ticketing, log *audit.Log, now func() time.Time) *Server {
	return &Server{alertmanager: alertmanager, store: st, tickets: tickets, audit: log, now: now}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("POST /webhook/alertmanager", s.intake(s.alertmanager))
	return mux
}

// intake serves one Source adapter's webhook.
func (s *Server) intake(a signal.Adapter) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		now := s.now()
		src, err := a.Authenticate(r)
		if err != nil {
			// The presented credential is never recorded. A failed audit write
			// still answers 401: nothing in the request is processed either way.
			_ = s.audit.Append(now, "auth.rejected", 0, map[string]string{
				"path": r.URL.Path, "remote": r.RemoteAddr, "reason": err.Error(),
			})
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		ref := src.Ref()

		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read body", http.StatusBadRequest)
			return
		}
		alerts, err := a.Split(body)
		if err != nil {
			if err := s.audit.Append(now, "request.rejected", 0, map[string]any{"source": ref, "reason": err.Error()}); err != nil {
				http.Error(w, "audit", http.StatusInternalServerError)
				return
			}
			http.Error(w, "unparseable body", http.StatusBadRequest)
			return
		}
		for _, raw := range alerts {
			sig, err := a.Map(src, raw, now)
			if err != nil {
				if err := s.audit.Append(now, "signal.dropped", 0, map[string]any{"source": ref, "reason": err.Error()}); err != nil {
					http.Error(w, "audit", http.StatusInternalServerError)
					return
				}
				continue
			}
			// The Control plane resolves an Alert's Target from its labels (ADR 0002 D4).
			sig.Target = src.Mapping.Resolve(src.Location, sig.Labels)
			if err := s.ingest(ctx, sig, now); err != nil {
				// The failed Signal was rolled back, so the source may safely re-send.
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
		}
	}
}

// ingest triages one Signal, stores the result, and brings its ticket up to date.
func (s *Server) ingest(ctx context.Context, sig signal.Signal, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	open, err := s.store.OpenIncidents(ctx)
	if err != nil {
		return err
	}
	d := triage.Triage(open, sig, now)
	decision := map[string]any{"kind": d.Kind, "key": sig.Key, "startsAt": sig.StartsAt}
	if d.Reason != "" {
		decision["reason"] = d.Reason
	}
	if d.Kind == triage.Drop {
		return s.auditDecision(sig, decision, 0, now)
	}

	// One transaction per Signal: if the ticket write fails, nothing is stored,
	// and the source's retry makes the same decision again.
	tx, err := s.store.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	inc := d.Incident
	if inc.ID, err = tx.Save(ctx, inc); err != nil {
		return err
	}
	if err := s.auditDecision(sig, decision, inc.ID, now); err != nil {
		return err
	}

	switch d.Kind {
	case triage.Open:
		ref, err := s.tickets.Create(ctx, title(inc), body(inc))
		if err != nil {
			return err
		}
		if err := tx.SetTicket(ctx, inc.ID, ref); err != nil {
			return err
		}
		if err := s.audit.Append(now, "ticket.created", inc.ID, map[string]string{"ref": ref}); err != nil {
			return err
		}
	case triage.Join:
		if err := s.tickets.Comment(ctx, inc.TicketRef, "Signal joined: "+memberLine(d.Member)); err != nil {
			return err
		}
		if err := s.audit.Append(now, "ticket.commented", inc.ID, map[string]string{"ref": inc.TicketRef, "key": d.Member.Key}); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Server) auditDecision(sig signal.Signal, decision map[string]any, incident int64, now time.Time) error {
	if err := s.audit.Append(now, "signal.received", incident, sig); err != nil {
		return err
	}
	return s.audit.Append(now, "triage.decision", incident, decision)
}

// The ticket shows identifiers only, never Alert annotations.

func title(inc triage.Incident) string {
	if inc.Target == nil {
		return fmt.Sprintf("%s (%s)", inc.Members[0].Name, inc.Key)
	}
	return fmt.Sprintf("%s on %s", inc.Members[0].Name, inc.Target)
}

func body(inc triage.Incident) string {
	var b strings.Builder
	if inc.Target == nil {
		fmt.Fprintf(&b, "**Target:** none. The Signal names no workload; key `%s`.\n\n", inc.Key)
	} else {
		fmt.Fprintf(&b, "**Target:** `%s` (%s)\n\n", inc.Target, inc.Target.Platform)
	}
	b.WriteString("**Signals:**\n\n")
	for _, m := range inc.Members {
		b.WriteString("- " + memberLine(m) + "\n")
	}
	fmt.Fprintf(&b, "\nincident-copilot Incident %d\n", inc.ID)
	return b.String()
}

func memberLine(m triage.Member) string {
	s := fmt.Sprintf("`%s` · %s · started %s · from %s/%s", m.Name, m.Severity, m.StartsAt.UTC().Format(time.RFC3339), m.Source.Kind, m.Source.Instance)
	if m.Link != "" {
		s += " · [source](" + m.Link + ")"
	}
	return s
}
