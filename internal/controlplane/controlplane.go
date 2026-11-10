// Package controlplane is the Control plane's Signal path: it authenticates
// Alerts, turns them into Signals, runs Triage, keeps Incidents in the store,
// writes one ticket per Incident and keeps it in step with the Incident's
// lifecycle, investigates each new Incident, and audits every step. The
// ticket is the source of truth for human-facing state: a ticket a human
// closed closes its Incident and stops its work.
package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/itsankoff/incident-copilot/internal/audit"
	"github.com/itsankoff/incident-copilot/internal/signal"
	"github.com/itsankoff/incident-copilot/internal/store"
	"github.com/itsankoff/incident-copilot/internal/triage"
)

// Ticketing writes and reads tickets. A ticket's reference is whatever Create
// returns.
type Ticketing interface {
	Create(ctx context.Context, title, body string) (ref string, err error)
	Comment(ctx context.Context, ref, body string) error
	// State reports whether ticket ref is closed, by anyone, and the
	// ticketing system's reason.
	State(ctx context.Context, ref string) (closed bool, reason string, err error)
	Close(ctx context.Context, ref string) error
	SetTitle(ctx context.Context, ref, title string) error
	// Name names the adapter in the Control plane's log.
	Name() string
	// URL is where a human reads ticket ref, or "" when the adapter cannot say.
	URL(ref string) string
}

// Store is the Control plane's state; *store.Store implements it.
type Store interface {
	Incidents(ctx context.Context) ([]triage.Incident, error)
	Begin(ctx context.Context) (*store.Tx, error)
}

type Server struct {
	alertmanager signal.Adapter
	store        Store
	tickets      Ticketing
	audit        *audit.Log
	logger       *slog.Logger // milestones for humans; the audit log is the record
	now          func() time.Time
	investigator *Investigator // nil: Incidents are not investigated

	mu             sync.Mutex // serialises Triage over the stored Incidents
	investigations sync.WaitGroup

	workMu sync.Mutex
	work   map[int64]*work // the running work of each Incident
}

// work is an Incident's running investigation, which stopWork cancels.
type work struct{ cancel context.CancelCauseFunc }

func New(alertmanager signal.Adapter, st Store, tickets Ticketing, log *audit.Log, logger *slog.Logger, now func() time.Time, inv *Investigator) *Server {
	return &Server{alertmanager: alertmanager, store: st, tickets: tickets, audit: log, logger: logger, now: now, investigator: inv,
		work: map[int64]*work{}}
}

// Wait returns once every investigation started so far has finished.
func (s *Server) Wait() { s.investigations.Wait() }

// SweepEvery is how often Sweep should run.
const SweepEvery = time.Minute

// startWork runs fn in the background for Incident id, under a context that
// stopWork cancels.
func (s *Server) startWork(id int64, fn func(ctx context.Context)) {
	ctx, cancel := context.WithCancelCause(context.Background())
	w := &work{cancel: cancel}
	s.workMu.Lock()
	s.work[id] = w
	s.workMu.Unlock()
	s.investigations.Add(1)
	go func() {
		defer s.investigations.Done()
		fn(ctx)
		s.workMu.Lock()
		if s.work[id] == w {
			delete(s.work, id)
		}
		s.workMu.Unlock()
		cancel(nil)
	}()
}

// stopWork cancels the work running for Incident id, if any, with cause.
func (s *Server) stopWork(id int64, cause error) {
	s.workMu.Lock()
	w := s.work[id]
	delete(s.work, id)
	s.workMu.Unlock()
	if w != nil {
		w.cancel(cause)
	}
}

// maxBody is the largest request body a Signal source may send (ADR 0002 D3).
const maxBody = 1 << 20

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

		body, ok := s.readBody(w, r, ref, now)
		if !ok {
			return
		}
		alerts, truncated, err := a.Split(body)
		if err != nil {
			if err := s.audit.Append(now, "request.rejected", 0, map[string]any{"source": ref, "reason": err.Error()}); err != nil {
				http.Error(w, "audit", http.StatusInternalServerError)
				return
			}
			http.Error(w, "unparseable body", http.StatusBadRequest)
			return
		}
		if truncated > 0 {
			if err := s.audit.Append(now, "request.truncated", 0, map[string]any{"source": ref, "truncatedAlerts": truncated}); err != nil {
				http.Error(w, "audit", http.StatusInternalServerError)
				return
			}
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
			sig = signal.Bound(sig)
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

// SensorSignal takes in one Detector Signal from location's Sensor, which the
// link has authenticated. The Signal is accepted only if its Target is in that
// Location (ADR 0003 D5); the Control plane sets its source, key, and
// receivedAt.
func (s *Server) SensorSignal(w http.ResponseWriter, r *http.Request, location string) {
	now := s.now()
	ref := signal.SourceRef{Kind: "sensor", Instance: location}
	body, ok := s.readBody(w, r, ref, now)
	if !ok {
		return
	}
	var sig signal.Signal
	if err := json.Unmarshal(body, &sig); err != nil {
		if err := s.audit.Append(now, "request.rejected", 0, map[string]any{"source": ref, "reason": err.Error()}); err != nil {
			http.Error(w, "audit", http.StatusInternalServerError)
			return
		}
		http.Error(w, "unparseable body", http.StatusBadRequest)
		return
	}
	if sig.Target == nil || sig.Target.Location != location {
		const reason = "Target in another Location than the Sensor's"
		if err := s.audit.Append(now, "signal.rejected", 0, map[string]any{"source": ref, "reason": reason, "key": sig.Key, "name": sig.Name, "target": sig.Target}); err != nil {
			http.Error(w, "audit", http.StatusInternalServerError)
			return
		}
		http.Error(w, reason, http.StatusForbidden)
		return
	}
	sig.Source, sig.ReceivedAt, sig.Raw = ref, now, body
	sig.Key = signal.DetectorKey(sig.Name, *sig.Target)
	if err := s.ingest(r.Context(), signal.Bound(sig), now); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// readBody reads a Signal source's request body of at most 1 MiB. On failure
// it has answered the request, and audited a body over the bound.
func (s *Server) readBody(w http.ResponseWriter, r *http.Request, ref signal.SourceRef, now time.Time) ([]byte, bool) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if errors.As(err, new(*http.MaxBytesError)) {
		if err := s.audit.Append(now, "request.rejected", 0, map[string]any{"source": ref, "reason": "body over 1 MiB"}); err != nil {
			http.Error(w, "audit", http.StatusInternalServerError)
			return nil, false
		}
		http.Error(w, "body over 1 MiB", http.StatusRequestEntityTooLarge)
		return nil, false
	}
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return nil, false
	}
	return body, true
}

// ingest triages one Signal, stores the result, and brings its ticket up to date.
func (s *Server) ingest(ctx context.Context, sig signal.Signal, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	incs, err := s.store.Incidents(ctx)
	if err != nil {
		return err
	}
	d := triage.Triage(incs, sig, now)
	if d.Kind != triage.Drop && d.Incident.ID != 0 {
		// The ticket's state wins: re-read it before acting on the Incident.
		i := index(incs, d.Incident.ID)
		closed, err := s.closeIfTicketClosed(ctx, incs[i], now)
		if err != nil {
			return err
		}
		if closed != nil {
			incs[i] = *closed
			d = triage.Triage(incs, sig, now)
		}
	}
	return s.apply(ctx, incs, d, &sig, now)
}

// Sweep is the Control plane's clock-driven decision point. It re-reads the
// ticket of every Incident that is not closed, closing each Incident whose
// ticket a human closed, then applies what the passing of time decides:
// expired Signals and idle closes. An Incident whose ticket cannot be read or
// written is skipped until the next Sweep.
func (s *Server) Sweep(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()

	incs, err := s.store.Incidents(ctx)
	if err != nil {
		return err
	}
	var errs []error
	failed := map[int64]bool{}
	for i, inc := range incs {
		if inc.State == triage.StateClosed {
			continue
		}
		closed, err := s.closeIfTicketClosed(ctx, inc, now)
		if err != nil {
			errs = append(errs, fmt.Errorf("incident %d: %w", inc.ID, err))
			failed[inc.ID] = true
			continue
		}
		if closed != nil {
			incs[i] = *closed
		}
	}
	for _, d := range triage.Tick(incs, now) {
		if failed[d.Incident.ID] {
			continue
		}
		if err := s.apply(ctx, incs, d, nil, now); err != nil {
			errs = append(errs, fmt.Errorf("incident %d: %w", d.Incident.ID, err))
			failed[d.Incident.ID] = true
			continue
		}
		incs[index(incs, d.Incident.ID)] = d.Incident
	}
	return errors.Join(errs...)
}

// closeIfTicketClosed re-reads inc's ticket. If a human closed it, it closes
// inc, stops its work, and returns the closed Incident; otherwise it returns nil.
func (s *Server) closeIfTicketClosed(ctx context.Context, inc triage.Incident, now time.Time) (*triage.Incident, error) {
	if inc.TicketRef == "" {
		return nil, nil
	}
	closed, reason, err := s.tickets.State(ctx, inc.TicketRef)
	if err != nil || !closed {
		return nil, err
	}
	d := triage.TicketClosed(inc, now)
	tx, err := s.store.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.Save(ctx, d.Incident); err != nil {
		return nil, err
	}
	if err := s.audit.Append(now, "triage.decision", inc.ID, map[string]any{
		"kind": d.Kind, "reason": d.Reason, "ref": inc.TicketRef, "ticketReason": reason,
	}); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	s.logDecision(ctx, nil, d, inc.ID)
	s.stopWork(inc.ID, fmt.Errorf("incident %d closed: %s (ticket %s, %s)", inc.ID, d.Reason, inc.TicketRef, reason))
	return &d.Incident, nil
}

// apply stores d and brings the ticket up to date, in one transaction: if a
// ticket write fails, nothing is stored, and the source's retry or the next
// Sweep makes the same decision again. incs is what d was decided over, and
// sig the Signal it was decided for, or nil when the decision is the Sweep's.
func (s *Server) apply(ctx context.Context, incs []triage.Incident, d triage.Decision, sig *signal.Signal, now time.Time) error {
	inc := d.Incident
	decision := map[string]any{"kind": d.Kind}
	switch {
	case sig != nil:
		decision["key"], decision["startsAt"] = sig.Key, sig.StartsAt
	case d.Member.Key != "":
		decision["key"], decision["startsAt"] = d.Member.Key, d.Member.StartsAt
	}
	if d.Reason != "" {
		decision["reason"] = d.Reason
	}
	if inc.Outcome != "" {
		decision["outcome"] = inc.Outcome
	}
	if d.Kind == triage.Open && inc.RecurrenceOf != 0 {
		decision["recurrenceOf"] = inc.RecurrenceOf
	}
	if d.Kind == triage.Drop {
		if err := s.auditDecision(sig, decision, 0, now); err != nil {
			return err
		}
		s.logDecision(ctx, sig, d, 0)
		return nil
	}

	tx, err := s.store.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if inc.ID, err = tx.Save(ctx, inc); err != nil {
		return err
	}
	if err := s.auditDecision(sig, decision, inc.ID, now); err != nil {
		return err
	}

	comment := ""
	switch d.Kind {
	case triage.Open:
		recurrenceRef := ""
		if inc.RecurrenceOf != 0 {
			recurrenceRef = incs[index(incs, inc.RecurrenceOf)].TicketRef
		}
		ref, err := s.tickets.Create(ctx, title(inc), body(inc, recurrenceRef))
		if err != nil {
			return err
		}
		if err := tx.SetTicket(ctx, inc.ID, ref); err != nil {
			return err
		}
		inc.TicketRef = ref
		if err := s.audit.Append(now, "ticket.created", inc.ID, map[string]string{"ref": ref}); err != nil {
			return err
		}
	case triage.Join:
		comment = "Signal joined: " + memberLine(d.Member)
	case triage.End:
		comment = endLine(d.Member)
	case triage.Resolve:
		comment = fmt.Sprintf("%s\n\n**Resolved.** Every Signal has ended; outcome: **%s**. "+
			"The ticket stays open: a new Signal for this Incident reopens it here. "+
			"incident-copilot closes the ticket after 24 hours with no activity.", endLine(d.Member), inc.Outcome)
	case triage.Reopen:
		comment = "**Reopened.** Signal fired: " + memberLine(d.Member)
	case triage.Close:
		comment = "**Closing:** no activity for 24 hours since the Incident resolved. " +
			"A later Signal for it opens a new Incident and ticket, linked to this one."
	}
	if comment != "" {
		if err := s.tickets.Comment(ctx, inc.TicketRef, comment); err != nil {
			return err
		}
		data := map[string]string{"ref": inc.TicketRef, "kind": string(d.Kind)}
		if d.Member.Key != "" {
			data["key"] = d.Member.Key
		}
		if err := s.audit.Append(now, "ticket.commented", inc.ID, data); err != nil {
			return err
		}
	}
	if d.Kind == triage.Close {
		if err := s.tickets.Close(ctx, inc.TicketRef); err != nil {
			return err
		}
		if err := s.audit.Append(now, "ticket.closed", inc.ID, map[string]string{"ref": inc.TicketRef, "reason": d.Reason}); err != nil {
			return err
		}
	}
	// The title shows the highest severity among the firing Signals.
	if i := index(incs, inc.ID); i >= 0 && title(inc) != title(incs[i]) {
		if err := s.tickets.SetTitle(ctx, inc.TicketRef, title(inc)); err != nil {
			return err
		}
		if err := s.audit.Append(now, "ticket.retitled", inc.ID, map[string]string{"ref": inc.TicketRef, "severity": string(inc.Severity())}); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	// Logged once stored, so a Signal the source must re-send logs nothing.
	s.logDecision(ctx, sig, d, inc.ID)
	if d.Kind == triage.Open {
		s.logger.Info("ticket.created", "incident", inc.ID, "adapter", s.tickets.Name(), "ref", inc.TicketRef, "kind", "incident")
	}
	if comment != "" {
		s.logger.Info("ticket.commented", "incident", inc.ID, "adapter", s.tickets.Name(), "ref", inc.TicketRef, "kind", string(d.Kind))
	}
	if d.Kind == triage.Close {
		s.logger.Info("ticket.closed", "incident", inc.ID, "adapter", s.tickets.Name(), "ref", inc.TicketRef)
	}

	switch {
	case d.Kind == triage.Close:
		s.stopWork(inc.ID, fmt.Errorf("incident %d closed: %s", inc.ID, d.Reason))
	case d.Kind == triage.Open && s.investigator != nil:
		// The investigation outlives the request that opened the Incident,
		// and stops when the Incident closes.
		s.startWork(inc.ID, func(ctx context.Context) {
			if err := s.investigator.Run(ctx, inc); err != nil {
				_ = s.audit.Append(s.now(), "investigation.failed", inc.ID, map[string]string{"error": err.Error()})
				s.logger.Error("investigation.failed", "incident", inc.ID, "error", err.Error())
			}
		})
	}
	return nil
}

// auditDecision audits the decision, after the Signal it was made for, if any.
func (s *Server) auditDecision(sig *signal.Signal, decision map[string]any, incident int64, now time.Time) error {
	if sig != nil {
		if err := s.audit.Append(now, "signal.received", incident, sig); err != nil {
			return err
		}
	}
	return s.audit.Append(now, "triage.decision", incident, decision)
}

// index is the position of Incident id in incs, or -1.
func index(incs []triage.Incident, id int64) int {
	return slices.IndexFunc(incs, func(inc triage.Incident) bool { return inc.ID == id })
}

// logDecision logs a Triage decision, after the Signal it was made for, if
// any, with names only. A repeat notice or a dropped Signal changes nothing a
// reader follows, so it logs at debug level.
func (s *Server) logDecision(ctx context.Context, sig *signal.Signal, d triage.Decision, incident int64) {
	level := slog.LevelInfo
	if d.Kind == triage.Repeat || d.Kind == triage.Drop {
		level = slog.LevelDebug
	}
	if sig != nil {
		target := ""
		if sig.Target != nil {
			target = sig.Target.String()
		}
		s.logger.Log(ctx, level, "signal.received", "incident", incident, "source", sig.Source.Kind+"/"+sig.Source.Instance, "name", sig.Name, "target", target)
	}
	args := []any{"incident", incident, "decision", string(d.Kind)}
	if d.Reason != "" {
		args = append(args, "reason", d.Reason)
	}
	s.logger.Log(ctx, level, "triage.decision", args...)
}

// The ticket shows identifiers only, never Alert annotations.

// title leads with the highest severity among the firing Signals, and has
// none once every Signal has ended.
func title(inc triage.Incident) string {
	t := fmt.Sprintf("%s on %s", inc.Members[0].Name, inc.Target)
	if inc.Target == nil {
		t = fmt.Sprintf("%s from %s (no Target)", inc.Members[0].Name, inc.Members[0].Source.Instance)
	}
	if sev := inc.Severity(); sev != "" {
		t = "[" + string(sev) + "] " + t
	}
	return t
}

// body is the ticket's opening text; recurrenceRef is the ticket of the
// Incident it is a recurrence of, if any.
func body(inc triage.Incident, recurrenceRef string) string {
	var b strings.Builder
	if inc.RecurrenceOf != 0 {
		fmt.Fprintf(&b, "**Recurrence of:** Incident %d, ticket #%s\n\n", inc.RecurrenceOf, recurrenceRef)
	}
	if inc.Target == nil {
		fmt.Fprintf(&b, "**Target:** none. No Signal names a workload; grouped by Signal name, key `%s`.\n\n", inc.Key)
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

func endLine(m triage.Member) string {
	if m.End == triage.OutcomeExpired {
		return "Signal expired, with no firing notice for 10 minutes: " + memberLine(m)
	}
	return "Signal resolved: " + memberLine(m)
}

func memberLine(m triage.Member) string {
	s := fmt.Sprintf("`%s` · %s · started %s · from %s/%s", m.Name, m.Severity, m.StartsAt.UTC().Format(time.RFC3339), m.Source.Kind, m.Source.Instance)
	if m.Link != "" {
		s += " · [source](" + m.Link + ")"
	}
	return s
}
