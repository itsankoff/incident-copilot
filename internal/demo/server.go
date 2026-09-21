package demo

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

const shutdownTimeout = 10 * time.Second

// Run starts the role described by cfg and blocks until ctx is cancelled,
// then drains in-flight requests and returns.
func Run(ctx context.Context, cfg Config, logger *slog.Logger, build BuildInfo) error {
	metrics := NewMetrics(string(cfg.Role), build)

	var ready atomic.Bool
	api := http.NewServeMux()
	api.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	api.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		if !ready.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	g := &group{cancel: cancel}

	switch cfg.Role {
	case RoleOrders:
		inv := NewHTTPInventory(cfg.InventoryURL, cfg.InventoryTimeout)
		registerOrders(api, inv, metrics, logger)
	case RoleInventory:
		registerInventory(api, metrics, logger)
	case RoleLoadgen:
		lg := newLoadgen(cfg, metrics, logger)
		g.Go(func() error { return lg.run(ctx) })
	default:
		return fmt.Errorf("unknown role %q", cfg.Role)
	}

	metricsMux := http.NewServeMux()
	metricsMux.Handle("GET /metrics", metrics.Handler())

	serve(ctx, g, &http.Server{Addr: cfg.ListenAddr, Handler: api, ReadHeaderTimeout: 5 * time.Second})
	serve(ctx, g, &http.Server{Addr: cfg.MetricsAddr, Handler: metricsMux, ReadHeaderTimeout: 5 * time.Second})

	ready.Store(true)
	logger.Info("started", "listen_addr", cfg.ListenAddr, "metrics_addr", cfg.MetricsAddr)

	g.wg.Wait()
	logger.Info("stopped")
	if err := context.Cause(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

// group runs goroutines and cancels the shared context with the first error.
type group struct {
	wg     sync.WaitGroup
	cancel context.CancelCauseFunc
}

func (g *group) Go(f func() error) {
	g.wg.Go(func() {
		if err := f(); err != nil {
			g.cancel(err)
		}
	})
}

// serve runs srv in g and shuts it down gracefully when ctx is cancelled.
func serve(ctx context.Context, g *group, srv *http.Server) {
	g.Go(func() error {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("serve %s: %w", srv.Addr, err)
		}
		return nil
	})
	g.Go(func() error {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	})
}

// requestID returns the caller's X-Request-Id, or a new random one.
func requestID(r *http.Request) string {
	if id := r.Header.Get("X-Request-Id"); id != "" {
		return id
	}
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
