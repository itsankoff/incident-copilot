// Package sensor is the Sensor role: it dials out to the Control plane, keeps
// the link up, and answers Evidence queries with the Location's tools. It keeps
// no state, calls no LLM, and changes nothing (ADR 0002 D2).
package sensor

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/itsankoff/incident-copilot/internal/evidence"
)

// Config is the Sensor's configuration.
type Config struct {
	Location        string
	ControlPlaneURL string
	Token           string
}

// LoadConfig builds a Config from environment variables read through getenv.
func LoadConfig(getenv func(string) string) (Config, error) {
	cfg := Config{Location: getenv("IC_SENSOR_LOCATION"), ControlPlaneURL: getenv("IC_CONTROL_PLANE_URL"), Token: getenv("IC_SENSOR_TOKEN")}
	for key, v := range map[string]string{"IC_SENSOR_LOCATION": cfg.Location, "IC_CONTROL_PLANE_URL": cfg.ControlPlaneURL, "IC_SENSOR_TOKEN": cfg.Token} {
		if v == "" {
			return Config{}, fmt.Errorf("%s: required", key)
		}
	}
	return cfg, nil
}

// Link is the Sensor's end of its link; *link.Client is the HTTP stand-in.
type Link interface {
	Next(ctx context.Context) (evidence.Call, bool, error)
	Reply(ctx context.Context, id string, a evidence.Answer) error
}

// Sensor answers Calls from its Link.
type Sensor struct {
	// Retry is the wait after a failed or refused poll.
	Retry time.Duration

	link   Link
	tools  map[string]evidence.Tool
	logger *slog.Logger
}

func New(l Link, tools []evidence.Tool, logger *slog.Logger) *Sensor {
	s := &Sensor{Retry: 5 * time.Second, link: l, tools: map[string]evidence.Tool{}, logger: logger}
	for _, t := range tools {
		s.tools[t.Name()] = t
	}
	return s
}

// Run keeps the link up until ctx ends. Calls run concurrently, so a slow one
// never stops the polls that keep the Location online.
func (s *Sensor) Run(ctx context.Context) {
	var running sync.WaitGroup
	defer running.Wait()
	for ctx.Err() == nil {
		call, ok, err := s.link.Next(ctx)
		if err != nil {
			if ctx.Err() == nil {
				s.logger.Warn("link down: " + err.Error())
			}
			select {
			case <-ctx.Done():
			case <-time.After(s.Retry):
			}
			continue
		}
		if ok {
			running.Go(func() { s.answer(ctx, call) })
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

func (s *Sensor) run(ctx context.Context, c evidence.Call) evidence.Answer {
	t, ok := s.tools[c.Tool]
	if !ok {
		return evidence.Answer{Error: "no such tool"}
	}
	data, err := t.Run(ctx, c.Query)
	if err != nil {
		return evidence.Answer{Error: err.Error()}
	}
	if len(data) > evidence.MaxAnswer {
		return evidence.Answer{Error: "answer over 1 MiB"}
	}
	return evidence.Answer{Data: data}
}
