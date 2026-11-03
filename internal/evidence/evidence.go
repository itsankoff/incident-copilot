// Package evidence is the platform-neutral side of an Evidence query: what the
// Control plane asks, what a Sensor's tools answer, and the Evidence ref an
// answer carries (ADR 0002 D6, D7). Platform packages implement Tool and are
// wired in at main; the core never imports them.
package evidence

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/itsankoff/incident-copilot/internal/signal"
)

// CallDeadline bounds one Evidence query (ADR 0002 D7).
const CallDeadline = 20 * time.Second

// MaxAnswer is the largest answer a Sensor sends over its link, in bytes.
const MaxAnswer = 1 << 20

// ErrOffline means the Location's Sensor has no link to the Control plane.
var ErrOffline = errors.New("location offline")

// Window is the time range a query reads.
type Window struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

// Query is one read-only question about a Target.
type Query struct {
	Tool   string        `json:"tool"`
	Target signal.Target `json:"target"`
	Window Window        `json:"window"`
}

// Call is a Query on its way to a Sensor.
type Call struct {
	ID string `json:"id"`
	Query
}

// Answer is a Sensor's reply to a Call.
type Answer struct {
	Data  json.RawMessage `json:"data,omitempty"`
	Error string          `json:"error,omitempty"`
}

// Ref is an Evidence ref: it names one call and what it read, and is the audit
// log's key for that call.
type Ref struct {
	CallID   string        `json:"callId"`
	Source   string        `json:"source"`
	Location string        `json:"location"`
	Target   signal.Target `json:"target"`
	Window   Window        `json:"window"`
}

// Result is a successful query's data and the ref a Claim cites.
type Result struct {
	Ref  Ref             `json:"ref"`
	Data json.RawMessage `json:"data"`
}

// Tool is one read-only evidence tool a Sensor offers.
type Tool interface {
	Name() string
	Run(ctx context.Context, q Query) (json.RawMessage, error)
}
