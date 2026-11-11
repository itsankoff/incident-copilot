// Package executor is the Executor role: it dials out to the Control plane and
// advertises the Actions its Location offers. It is the only role that will
// change state (#57); it never calls an LLM and has no access to ticketing
// (ADR 0003 D1). In this version it runs nothing.
package executor

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/itsankoff/incident-copilot/internal/action"
)

// Config is the Executor's configuration.
type Config struct {
	Location        string
	ControlPlaneURL string
	Token           string
}

// LoadConfig builds a Config from environment variables read through getenv.
func LoadConfig(getenv func(string) string) (Config, error) {
	cfg := Config{Location: getenv("IC_EXECUTOR_LOCATION"), ControlPlaneURL: getenv("IC_CONTROL_PLANE_URL"), Token: getenv("IC_EXECUTOR_TOKEN")}
	for key, v := range map[string]string{"IC_EXECUTOR_LOCATION": cfg.Location, "IC_CONTROL_PLANE_URL": cfg.ControlPlaneURL, "IC_EXECUTOR_TOKEN": cfg.Token} {
		if v == "" {
			return Config{}, fmt.Errorf("%s: required", key)
		}
	}
	return cfg, nil
}

// Link is the Executor's end of its link; *link.Client is the HTTP stand-in.
type Link interface {
	// Poll advertises the catalog and waits for the Control plane.
	Poll(ctx context.Context, catalog []action.Declaration) error
}

// Executor keeps its Location's catalog advertised.
type Executor struct {
	// Retry is the wait after a failed or refused poll.
	Retry time.Duration

	link    Link
	catalog []action.Declaration
	logger  *slog.Logger
}

func New(l Link, catalog []action.Declaration, logger *slog.Logger) *Executor {
	return &Executor{Retry: 5 * time.Second, link: l, catalog: catalog, logger: logger}
}

// Run keeps the link up until ctx ends. Every poll carries the catalog, so a
// restarted Control plane learns it on the next poll.
func (e *Executor) Run(ctx context.Context) {
	for ctx.Err() == nil {
		if err := e.link.Poll(ctx, e.catalog); err != nil {
			if ctx.Err() == nil {
				e.logger.Warn("link down: " + err.Error())
			}
			select {
			case <-ctx.Done():
			case <-time.After(e.Retry):
			}
		}
	}
}
