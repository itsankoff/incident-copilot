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
	"os/signal"
	"syscall"
	"time"

	"github.com/itsankoff/incident-copilot/internal/alertmanager"
	"github.com/itsankoff/incident-copilot/internal/audit"
	"github.com/itsankoff/incident-copilot/internal/controlplane"
	"github.com/itsankoff/incident-copilot/internal/github"
	"github.com/itsankoff/incident-copilot/internal/store"
)

// Set with -ldflags at build time.
var (
	version = "dev"
	commit  = "unknown"
)

func main() {
	role := flag.String("role", "", "role: control-plane | sensor | executor")
	config := flag.String("config", "", "path to the control-plane JSON config file")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("role", *role, "version", version, "commit", commit)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	var err error
	switch *role {
	case "control-plane":
		err = runControlPlane(ctx, *config, logger)
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

func runControlPlane(ctx context.Context, path string, logger *slog.Logger) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	cfg, err := controlplane.ParseConfig(f)
	_ = f.Close()
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

	tickets := github.New(cfg.GitHub.APIURL, cfg.GitHub.Repo, cfg.GitHub.Token, &http.Client{Timeout: 30 * time.Second})
	cp := controlplane.New(alertmanager.New(cfg.Sources), st, tickets, log, time.Now)

	srv := &http.Server{Addr: cfg.Listen, Handler: cp.Handler(), ReadHeaderTimeout: 5 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	logger.Info("control plane listening", "addr", cfg.Listen, "sources", len(cfg.Sources), "repo", cfg.GitHub.Repo)

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
