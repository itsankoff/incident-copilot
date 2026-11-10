// Package sensor is the Sensor role: it dials out to the Control plane, keeps
// the link up, answers Evidence queries with the Location's tools, and runs
// the Location's Detectors, sending their Signals over the link. It keeps no
// state across restarts, calls no LLM, and changes nothing (ADR 0002 D2).
package sensor

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/itsankoff/incident-copilot/internal/evidence"
	"github.com/itsankoff/incident-copilot/internal/redact"
	"github.com/itsankoff/incident-copilot/internal/signal"
)

// Config is the Sensor's configuration.
type Config struct {
	Location        string
	ControlPlaneURL string
	Token           string
	Namespaces      []string // the namespace allowlist the Detectors watch
}

// LoadConfig builds a Config from environment variables read through getenv.
func LoadConfig(getenv func(string) string) (Config, error) {
	cfg := Config{Location: getenv("IC_SENSOR_LOCATION"), ControlPlaneURL: getenv("IC_CONTROL_PLANE_URL"), Token: getenv("IC_SENSOR_TOKEN")}
	namespaces := getenv("IC_SENSOR_NAMESPACES") // comma-separated
	for key, v := range map[string]string{"IC_SENSOR_LOCATION": cfg.Location, "IC_CONTROL_PLANE_URL": cfg.ControlPlaneURL, "IC_SENSOR_TOKEN": cfg.Token, "IC_SENSOR_NAMESPACES": namespaces} {
		if v == "" {
			return Config{}, fmt.Errorf("%s: required", key)
		}
	}
	for ns := range strings.SplitSeq(namespaces, ",") {
		// An empty name would list every namespace.
		if ns = strings.TrimSpace(ns); ns == "" {
			return Config{}, fmt.Errorf("IC_SENSOR_NAMESPACES: empty namespace in %q", namespaces)
		}
		cfg.Namespaces = append(cfg.Namespaces, ns)
	}
	return cfg, nil
}

// Link is the Sensor's end of its link; *link.Client is the HTTP stand-in.
type Link interface {
	Next(ctx context.Context) (evidence.Call, bool, error)
	Reply(ctx context.Context, id string, a evidence.Answer) error
	Send(ctx context.Context, s signal.Signal) error
}

// Detector is one built-in rule a Sensor runs against its Location (ADR 0002
// D5). Platform packages implement it; the Sensor owns sending its Signals.
type Detector interface {
	// Check reads the Location at now and returns the Signals that started
	// firing or resolved since the last Check, at most one per Target, each
	// with its Target set. On start it rebuilds what is firing from the
	// Location's current state.
	Check(ctx context.Context, now time.Time) ([]signal.Signal, error)
}

const (
	// resend is how often a firing Detector Signal is sent again, so a dead
	// Sensor's Signals expire instead of hanging open (ADR 0002 D5).
	resend = 5 * time.Minute
	// detectEvery is how often the Detectors run.
	detectEvery = 10 * time.Second
	// checkDeadline bounds one Detector's Check.
	checkDeadline = 30 * time.Second
)

// Sensor answers Calls from its Link and sends its Detectors' Signals over it.
type Sensor struct {
	// Retry is the wait after a failed or refused poll.
	Retry time.Duration

	link      Link
	tools     map[string]evidence.Tool
	detectors []Detector
	logger    *slog.Logger

	signals map[string]outgoing // by key: firing, or resolved and not yet sent
}

type outgoing struct {
	signal signal.Signal
	sentAt time.Time // zero until sent
}

func New(l Link, tools []evidence.Tool, detectors []Detector, logger *slog.Logger) *Sensor {
	s := &Sensor{Retry: 5 * time.Second, link: l, tools: map[string]evidence.Tool{},
		detectors: detectors, logger: logger, signals: map[string]outgoing{}}
	for _, t := range tools {
		s.tools[t.Name()] = t
	}
	return s
}

// Detect runs every Detector at now and sends what is due: each Signal that
// changed, and each firing Signal last sent 5 minutes ago. A failed send is
// retried at the next Detect. Run calls it every 10 seconds.
func (s *Sensor) Detect(ctx context.Context, now time.Time) {
	for _, d := range s.detectors {
		cctx, cancel := context.WithTimeout(ctx, checkDeadline)
		sigs, err := d.Check(cctx, now)
		cancel()
		if err != nil {
			s.logger.Warn("detector failed: " + err.Error())
			continue
		}
		for _, sig := range sigs {
			s.signals[sig.Key] = outgoing{signal: sig}
		}
	}
	for _, key := range slices.Sorted(maps.Keys(s.signals)) {
		o := s.signals[key]
		if !o.sentAt.IsZero() && now.Sub(o.sentAt) < resend {
			continue
		}
		if err := s.link.Send(ctx, o.signal); err != nil {
			s.logger.Warn("signal send failed: "+err.Error(), "key", key)
			continue
		}
		if o.signal.Status != signal.Firing {
			delete(s.signals, key)
			continue
		}
		o.sentAt = now
		s.signals[key] = o
	}
}

// Run keeps the link up and runs the Detectors until ctx ends. Calls run
// concurrently, so a slow one never stops the polls that keep the Location
// online. It logs link.up on the first poll that gets through after start or
// after a failure, and link.down on every failed poll.
func (s *Sensor) Run(ctx context.Context) {
	var running sync.WaitGroup
	defer running.Wait()
	if len(s.detectors) > 0 {
		running.Go(func() { s.detect(ctx) })
	}
	up := false
	for ctx.Err() == nil {
		call, ok, err := s.link.Next(ctx)
		if err != nil {
			up = false
			if ctx.Err() == nil {
				s.logger.Warn("link.down", "error", err.Error())
			}
			select {
			case <-ctx.Done():
			case <-time.After(s.Retry):
			}
			continue
		}
		if !up {
			up = true
			s.logger.Info("link.up")
		}
		if ok {
			running.Go(func() { s.answer(ctx, call) })
		}
	}
}

func (s *Sensor) detect(ctx context.Context) {
	tick := time.NewTicker(detectEvery)
	defer tick.Stop()
	for {
		s.Detect(ctx, time.Now())
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func (s *Sensor) answer(ctx context.Context, c evidence.Call) {
	ctx, cancel := context.WithTimeout(ctx, evidence.CallDeadline)
	defer cancel()
	if err := s.link.Reply(ctx, c.ID, s.run(ctx, c)); err != nil {
		s.logger.Warn("reply failed: "+err.Error(), "call", c.ID)
	}
}

// run answers c. The Sensor is the edge that fetches the data, so it
// redacts the answer, error text included, before it leaves the Location;
// the Control plane redacts it again (ADR 0003 D8).
func (s *Sensor) run(ctx context.Context, c evidence.Call) evidence.Answer {
	t, ok := s.tools[c.Tool]
	if !ok {
		return evidence.Answer{Error: "no such tool"}
	}
	data, err := t.Run(ctx, c.Query)
	if err != nil {
		return evidence.Answer{Error: redact.String(err.Error())}
	}
	data = redact.JSON(data)
	if len(data) > evidence.MaxAnswer {
		return evidence.Answer{Error: "answer over 1 MiB"}
	}
	return evidence.Answer{Data: data}
}
