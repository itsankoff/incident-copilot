// Package link is the stand-in for the Control plane ↔ Sensor and Executor
// link, whose protocol, mutual authentication, and enrolment are not yet
// specified (ADR 0002 D2). A Sensor or Executor dials out and long-polls over
// HTTP with its Location's static token; nothing connects into a Location.
//
// A Sensor's poll is GET /link/{location}/next, answered with the next Call
// or, after PollHold, 204. A Sensor replies with POST
// /link/{location}/replies/{id}, and sends a Detector Signal with POST
// /link/{location}/signals.
//
// An Executor's poll is POST /link/{location}/executor/next carrying its
// Action catalog, answered after PollHold with 204: nothing runs yet (#57).
// The Location's token admits both roles until enrolment is specified.
package link

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/itsankoff/incident-copilot/internal/action"
	"github.com/itsankoff/incident-copilot/internal/audit"
	"github.com/itsankoff/incident-copilot/internal/evidence"
)

const (
	// OfflineAfter is how long after its last poll a Location goes offline. It
	// must exceed the poll hold, so a healthy Sensor never crosses it.
	OfflineAfter = 30 * time.Second

	pollHold  = 20 * time.Second
	sweep     = time.Second
	maxReply  = evidence.MaxAnswer + 1<<10 // room for the envelope
	maxQueued = 16
	maxName   = 253 // a refused name is untrusted; the audit log keeps this much
)

type location struct {
	token    string
	online   bool
	lastPoll time.Time
	calls    chan evidence.Call
	pending  map[string]chan evidence.Answer
	executor executorLink
}

// executorLink is a Location's Executor as its last poll left it.
type executorLink struct {
	online   bool
	lastPoll time.Time
	catalog  []action.Declaration
}

// Hub is the Control plane's end of every Sensor link. It keeps no state
// across restarts: each Location starts offline until its Sensor polls.
type Hub struct {
	// PollHold is how long a poll waits for a Call.
	PollHold time.Duration
	// Signals takes in each Detector Signal a Sensor sends, once the link has
	// authenticated it as location's Sensor. Set it before serving.
	Signals func(w http.ResponseWriter, r *http.Request, location string)

	audit *audit.Log
	now   func() time.Time
	stop  chan struct{}

	mu        sync.Mutex
	locations map[string]*location
}

// NewHub returns a Hub for the configured Locations, given as name → token.
func NewHub(tokens map[string]string, log *audit.Log, now func() time.Time) *Hub {
	h := &Hub{PollHold: pollHold, audit: log, now: now, stop: make(chan struct{}), locations: map[string]*location{}}
	for name, token := range tokens {
		h.locations[name] = &location{
			token:   token,
			calls:   make(chan evidence.Call, maxQueued),
			pending: map[string]chan evidence.Answer{},
		}
	}
	return h
}

// Run marks silent Locations offline until ctx ends, then releases held polls
// so the server can shut down. It returns only an audit failure, or nil.
func (h *Hub) Run(ctx context.Context) error {
	defer close(h.stop)
	tick := time.NewTicker(sweep)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
			if err := h.Sweep(); err != nil {
				return err
			}
		}
	}
}

// Sweep marks offline every Sensor and Executor whose last poll is
// OfflineAfter old.
func (h *Hub) Sweep() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	for name, l := range h.locations {
		if l.online && now.Sub(l.lastPoll) >= OfflineAfter {
			if err := h.audit.Append(now, "location.offline", 0, map[string]string{"location": name}); err != nil {
				return err
			}
			l.online = false
		}
		if e := &l.executor; e.online && now.Sub(e.lastPoll) >= OfflineAfter {
			if err := h.audit.Append(now, "executor.offline", 0, map[string]string{"location": name}); err != nil {
				return err
			}
			e.online, e.catalog = false, nil
		}
	}
	return nil
}

// Catalog returns the Actions location's Executor advertised on its last
// poll. ok is false while that Executor is offline.
func (h *Hub) Catalog(name string) ([]action.Declaration, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	l, ok := h.locations[name]
	if !ok || !l.executor.online {
		return nil, false
	}
	return slices.Clone(l.executor.catalog), true
}

// Online reports whether location's Sensor holds a live link.
func (h *Hub) Online(name string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	l, ok := h.locations[name]
	return ok && l.online
}

// Call hands c to location's Sensor and waits for its Answer or for ctx.
func (h *Hub) Call(ctx context.Context, name string, c evidence.Call) (evidence.Answer, error) {
	reply := make(chan evidence.Answer, 1)
	h.mu.Lock()
	l, ok := h.locations[name]
	if !ok {
		h.mu.Unlock()
		return evidence.Answer{}, fmt.Errorf("unknown location %q", name)
	}
	if !l.online {
		h.mu.Unlock()
		return evidence.Answer{}, fmt.Errorf("%s: %w", name, evidence.ErrOffline)
	}
	l.pending[c.ID] = reply
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(l.pending, c.ID)
		h.mu.Unlock()
	}()

	select {
	case l.calls <- c:
	case <-ctx.Done():
		return evidence.Answer{}, ctx.Err()
	}
	select {
	case a := <-reply:
		return a, nil
	case <-ctx.Done():
		return evidence.Answer{}, ctx.Err()
	}
}

// Handler serves the Sensors' side of the link.
func (h *Hub) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /link/{location}/next", h.next)
	mux.HandleFunc("POST /link/{location}/replies/{id}", h.reply)
	mux.HandleFunc("POST /link/{location}/executor/next", h.pollExecutor)
	mux.HandleFunc("POST /link/{location}/signals", h.signals)
	return mux
}

// executorPoll is the body of an Executor's poll.
type executorPoll struct {
	Actions []action.Declaration `json:"actions"`
}

func (h *Hub) pollExecutor(w http.ResponseWriter, r *http.Request) {
	l, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	var p executorPoll
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxReply)).Decode(&p); err != nil {
		http.Error(w, "unparseable body", http.StatusBadRequest)
		return
	}
	if err := h.executorPolled(r.PathValue("location"), l, p.Actions); err != nil {
		http.Error(w, "audit", http.StatusInternalServerError)
		return
	}
	hold := time.NewTimer(h.PollHold)
	defer hold.Stop()
	select {
	case <-hold.C:
	case <-h.stop:
	case <-r.Context().Done():
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// executorPolled records an Executor's poll and the catalog it carried,
// auditing the catalog whenever the Executor comes online or changes it.
func (h *Hub) executorPolled(name string, l *location, actions []action.Declaration) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	e := &l.executor
	if !e.online || !reflect.DeepEqual(e.catalog, actions) {
		names := make([]string, len(actions))
		for i, a := range actions {
			names[i] = a.Name
		}
		if err := h.audit.Append(now, "executor.online", 0, map[string]any{"location": name, "actions": names}); err != nil {
			return err
		}
	}
	e.online, e.lastPoll, e.catalog = true, now, actions
	return nil
}

func (h *Hub) next(w http.ResponseWriter, r *http.Request) {
	l, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	if err := h.polled(r.PathValue("location"), l); err != nil {
		http.Error(w, "audit", http.StatusInternalServerError)
		return
	}
	hold := time.NewTimer(h.PollHold)
	defer hold.Stop()
	for {
		select {
		case c := <-l.calls:
			if !h.waiting(l, c.ID) {
				continue // its caller gave up while it was queued
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(c)
		case <-hold.C:
			w.WriteHeader(http.StatusNoContent)
		case <-h.stop:
			w.WriteHeader(http.StatusNoContent)
		case <-r.Context().Done():
		}
		return
	}
}

func (h *Hub) waiting(l *location, id string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	_, ok := l.pending[id]
	return ok
}

// polled records a poll, bringing the Location online if it was not.
func (h *Hub) polled(name string, l *location) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	if !l.online {
		if err := h.audit.Append(now, "location.online", 0, map[string]string{"location": name}); err != nil {
			return err
		}
		l.online = true
	}
	l.lastPoll = now
	return nil
}

func (h *Hub) reply(w http.ResponseWriter, r *http.Request) {
	l, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	var a evidence.Answer
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxReply)).Decode(&a); err != nil {
		http.Error(w, "unparseable body", http.StatusBadRequest)
		return
	}
	h.mu.Lock()
	pending, ok := l.pending[r.PathValue("id")]
	h.mu.Unlock()
	if !ok {
		// Late, or a Call this Location never received. Only this Location's
		// pending Calls can be answered, so one Sensor cannot speak for another.
		_ = h.audit.Append(h.now(), "link.reply_dropped", 0, map[string]string{"location": r.PathValue("location"), "call": r.PathValue("id")})
		http.Error(w, "no such call", http.StatusNotFound)
		return
	}
	select {
	case pending <- a:
	default: // already answered
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Hub) signals(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.authenticate(w, r); ok {
		h.Signals(w, r, r.PathValue("location"))
	}
}

// authenticate checks the Location exists and the token is its own. Both
// refusals look the same to the caller and differ only in the audit log.
func (h *Hub) authenticate(w http.ResponseWriter, r *http.Request) (*location, bool) {
	h.mu.Lock()
	l, known := h.locations[r.PathValue("location")]
	h.mu.Unlock()
	reason := "location not in config"
	if known {
		reason = "bad token"
		tok, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if tok != "" && subtle.ConstantTimeCompare([]byte(tok), []byte(l.token)) == 1 {
			return l, true
		}
	}
	// The presented credential is never recorded. A failed audit write still
	// refuses: nothing in the request is processed either way.
	h.refuse(r, reason)
	http.Error(w, "unauthorized", http.StatusUnauthorized)
	return nil, false
}

func (h *Hub) refuse(r *http.Request, reason string) {
	name := r.PathValue("location")
	if len(name) > maxName {
		name = name[:maxName]
	}
	_ = h.audit.Append(h.now(), "link.refused", 0, map[string]string{"location": name, "remote": r.RemoteAddr, "reason": reason})
}

// ErrRefused means the Control plane turned the Sensor away.
var ErrRefused = errors.New("refused by the Control plane")
