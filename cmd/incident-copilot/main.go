// Command incident-copilot is the incident responder. One binary runs one of
// three roles: control-plane, sensor, or executor. Only control-plane exists yet.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	ossignal "os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/itsankoff/incident-copilot/internal/alertmanager"
	"github.com/itsankoff/incident-copilot/internal/audit"
	"github.com/itsankoff/incident-copilot/internal/controlplane"
	"github.com/itsankoff/incident-copilot/internal/github"
	"github.com/itsankoff/incident-copilot/internal/signal"
	"github.com/itsankoff/incident-copilot/internal/store"
)

// Set with -ldflags at build time.
var (
	version = "dev"
	commit  = "unknown"
)

func main() {
	role := flag.String("role", "", "role: control-plane | sensor | executor")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("role", *role, "version", version, "commit", commit)
	ctx, stop := ossignal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	var err error
	switch *role {
	case "control-plane":
		err = runControlPlane(ctx, logger)
	case "sensor", "executor":
		err = fmt.Errorf("role %s: not implemented", *role)
	default:
		err = fmt.Errorf("role: want control-plane | sensor | executor, got %q", *role)
	}
	if err != nil {
		logger.Error("fatal: " + err.Error())
		os.Exit(1)
	}
}

func runControlPlane(ctx context.Context, logger *slog.Logger) error {
	cfg, err := controlplane.LoadConfig(os.Getenv)
	if err != nil {
		return err
	}

	st, err := store.Open(cfg.StatePath)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	log, err := audit.Open(cfg.AuditPath)
	if err != nil {
		return err
	}
	defer func() { _ = log.Close() }()

	var tickets controlplane.Ticketing
	repo := ""
	if gh := cfg.GitHub; gh != nil {
		tickets = github.New(gh.APIURL, gh.Repo, gh.Token, &http.Client{Timeout: 30 * time.Second})
		repo = gh.Repo
	} else {
		tickets = &logTickets{logger: logger}
		logger.Warn("IC_GITHUB_REPO and IC_GITHUB_TOKEN unset: using the fake ticketing adapter, which only logs tickets")
	}
	cp := controlplane.New(alertmanager.New([]signal.Source{cfg.Alertmanager}), st, tickets, log, time.Now)

	srv := &http.Server{Addr: cfg.ListenAddr, Handler: cp.Handler(), ReadHeaderTimeout: 5 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	logger.Info("control plane listening", "addr", cfg.ListenAddr, "source", cfg.Alertmanager.Instance, "repo", repo)

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdown); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// logTickets is the fake ticketing adapter: it logs each ticket instead of
// writing it anywhere. References count up from log-1 per process.
type logTickets struct {
	logger *slog.Logger
	mu     sync.Mutex
	n      int
}

func (l *logTickets) Create(_ context.Context, title, body string) (string, error) {
	l.mu.Lock()
	l.n++
	ref := fmt.Sprintf("log-%d", l.n)
	l.mu.Unlock()
	l.logger.Info("ticket created", "ref", ref, "title", title, "body", body)
	return ref, nil
}

func (l *logTickets) Comment(_ context.Context, ref, body string) error {
	l.logger.Info("ticket comment", "ref", ref, "body", body)
	return nil
}
