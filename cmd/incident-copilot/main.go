// Command incident-copilot is the incident responder. One binary runs one of
// three roles: control-plane, sensor, or executor. The executor only
// advertises its Actions so far; it runs none of them yet.
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
	"github.com/itsankoff/incident-copilot/internal/executor"
	"github.com/itsankoff/incident-copilot/internal/github"
	"github.com/itsankoff/incident-copilot/internal/kubernetes"
	"github.com/itsankoff/incident-copilot/internal/link"
	"github.com/itsankoff/incident-copilot/internal/openrouter"
	"github.com/itsankoff/incident-copilot/internal/sensor"
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
	case "sensor":
		err = runSensor(ctx, logger)
	case "executor":
		err = runExecutor(ctx, logger)
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
	tokens := map[string]string{}
	for _, l := range cfg.Locations {
		tokens[l.Name] = l.Token
	}
	hub := link.NewHub(tokens, log, time.Now)
	ev := controlplane.NewEvidence(cfg.Locations, hub, log, time.Now)

	var inv *controlplane.Investigator
	model := ""
	if c := cfg.LLM; c != nil {
		// Free models can take a while to answer; the RCA loop's time budget
		// bounds the whole investigation.
		client := openrouter.New(openrouter.APIURL, c.Key, &http.Client{Timeout: 2 * time.Minute})
		rem := controlplane.Remediation{Executors: hub, Reviewed: kubernetes.Actions(), Policy: cfg.Policy, KillSwitch: st}
		inv = controlplane.NewInvestigator(client, c.Model, c.Budget, ev, rem, tickets, log, time.Now)
		model = c.Model
	} else {
		logger.Warn("OPENROUTER_API_KEY unset: Incidents get tickets but are not investigated")
	}
	cp := controlplane.New(alertmanager.New([]signal.Source{cfg.Alertmanager}), st, tickets, log, time.Now, inv)
	// Deferred after the store and audit log, so it runs before they close: an
	// investigation in flight finishes (within its time budget, unless the pod
	// is killed first), though its Evidence queries fail once the hub stops.
	defer cp.Wait()

	mux := http.NewServeMux()
	mux.Handle("/link/", hub.Handler())
	mux.Handle("/", cp.Handler())
	srv := &http.Server{Addr: cfg.ListenAddr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	debugMux := http.NewServeMux()
	debugMux.Handle("/debug/kill-switch", controlplane.KillSwitchHandler(st, log, time.Now))
	debugMux.Handle("/", ev.DebugHandler())
	debug := &http.Server{Addr: cfg.DebugAddr, Handler: debugMux, ReadHeaderTimeout: 5 * time.Second}
	errc := make(chan error, 3)
	go func() { errc <- srv.ListenAndServe() }()
	go func() { errc <- debug.ListenAndServe() }()
	go func() {
		// Expiry, idle closes, and tickets closed by a human are noticed here.
		t := time.NewTicker(controlplane.SweepEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := cp.Sweep(ctx); err != nil {
					logger.Error("sweep: " + err.Error())
				}
			}
		}
	}()
	hubDone := make(chan struct{})
	go func() {
		defer close(hubDone)
		if err := hub.Run(ctx); err != nil {
			errc <- err
		}
	}()
	if p := cfg.Policy.Problem; p != "" {
		logger.Warn("the Environment policy " + p + ": every ceiling is L1")
	}
	logger.Info("control plane listening", "addr", cfg.ListenAddr, "source", cfg.Alertmanager.Instance, "repo", repo, "locations", len(cfg.Locations), "model", model)

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	<-hubDone // releases held polls, which Shutdown would otherwise wait for
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return errors.Join(srv.Shutdown(shutdown), debug.Shutdown(shutdown))
}

func runSensor(ctx context.Context, logger *slog.Logger) error {
	cfg, err := sensor.LoadConfig(os.Getenv)
	if err != nil {
		return err
	}
	tools, err := kubernetes.InCluster()
	if err != nil {
		return err
	}
	logger.Info("sensor dialing", "location", cfg.Location, "controlPlane", cfg.ControlPlaneURL)
	sensor.New(link.NewClient(cfg.ControlPlaneURL, cfg.Location, cfg.Token), tools, logger).Run(ctx)
	return nil
}

func runExecutor(ctx context.Context, logger *slog.Logger) error {
	cfg, err := executor.LoadConfig(os.Getenv)
	if err != nil {
		return err
	}
	logger.Info("executor dialing", "location", cfg.Location, "controlPlane", cfg.ControlPlaneURL)
	executor.New(link.NewClient(cfg.ControlPlaneURL, cfg.Location, cfg.Token), kubernetes.Actions(), logger).Run(ctx)
	return nil
}

// logTickets is the fake ticketing adapter: it logs each ticket instead of
// writing it anywhere, and its tickets are never closed by a human.
// References count up from log-1 per process.
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

func (l *logTickets) State(context.Context, string) (bool, string, error) { return false, "", nil }

func (l *logTickets) Close(_ context.Context, ref string) error {
	l.logger.Info("ticket closed", "ref", ref)
	return nil
}

func (l *logTickets) SetTitle(_ context.Context, ref, title string) error {
	l.logger.Info("ticket retitled", "ref", ref, "title", title)
	return nil
}
