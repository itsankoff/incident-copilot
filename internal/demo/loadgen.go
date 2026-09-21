package demo

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"time"
)

// loadgen sends a constant mix of reads and writes to the orders service.
type loadgen struct {
	cfg     Config
	client  *http.Client
	metrics *Metrics
	logger  *slog.Logger
}

func newLoadgen(cfg Config, m *Metrics, logger *slog.Logger) *loadgen {
	return &loadgen{cfg: cfg, client: &http.Client{Timeout: 5 * time.Second}, metrics: m, logger: logger}
}

func (l *loadgen) run(ctx context.Context) error {
	tick := time.NewTicker(time.Duration(float64(time.Second) / l.cfg.RPS))
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
			go l.fire(ctx)
		}
	}
}

// fire sends one request and records its outcome as a dependency call on
// "orders", which gives the client-side view of the service.
func (l *loadgen) fire(ctx context.Context) {
	id := rand.IntN(1000)
	var req *http.Request
	var err error
	if rand.Float64() < l.cfg.ReadRatio {
		req, err = http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/orders/%d", l.cfg.TargetURL, id), nil)
	} else {
		body := fmt.Sprintf(`{"sku":"sku-%d"}`, id)
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, l.cfg.TargetURL+"/orders", bytes.NewBufferString(body))
		if req != nil {
			req.Header.Set("Content-Type", "application/json")
		}
	}
	if err != nil {
		l.logger.Error("build request: " + err.Error())
		return
	}

	start := time.Now()
	resp, err := l.client.Do(req)
	elapsed := time.Since(start)
	switch {
	case err != nil && ctx.Err() != nil:
		return // shutting down
	case err != nil && isTimeout(err):
		l.metrics.ObserveDependency("orders", "timeout", elapsed)
	case err != nil:
		l.metrics.ObserveDependency("orders", "error", elapsed)
	default:
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		outcome := "ok"
		if resp.StatusCode >= 500 {
			outcome = "error"
		}
		l.metrics.ObserveDependency("orders", outcome, elapsed)
	}
}
