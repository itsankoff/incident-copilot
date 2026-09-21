package demo

import (
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// BuildInfo identifies the running build. It is set with -ldflags.
type BuildInfo struct {
	Version string
	Commit  string
}

// NewLogger returns a JSON logger whose every line carries service, version,
// and commit, as the telemetry contract requires.
func NewLogger(service, version, commit string) *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stdout, nil)).With(
		"service", service, "version", version, "commit", commit,
	)
}

// Metrics holds the demo-svc metrics on a private registry.
type Metrics struct {
	registry        *prometheus.Registry
	requests        *prometheus.CounterVec
	requestDuration *prometheus.HistogramVec
	depRequests     *prometheus.CounterVec
	depDuration     *prometheus.HistogramVec
}

// NewMetrics registers the telemetry-contract metrics for service.
func NewMetrics(service string, build BuildInfo) *Metrics {
	reg := prometheus.NewRegistry()
	constLabels := prometheus.Labels{"service": service}
	m := &Metrics{
		registry: reg,
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "http_requests_total", Help: "HTTP requests served.", ConstLabels: constLabels,
		}, []string{"route", "code"}),
		requestDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "http_request_duration_seconds", Help: "HTTP request latency.", ConstLabels: constLabels,
			Buckets: prometheus.DefBuckets,
		}, []string{"route"}),
		depRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "dependency_requests_total", Help: "Calls to downstream dependencies.", ConstLabels: constLabels,
		}, []string{"dependency", "outcome"}),
		depDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "dependency_request_duration_seconds", Help: "Downstream call latency.", ConstLabels: constLabels,
			Buckets: prometheus.DefBuckets,
		}, []string{"dependency"}),
	}
	buildInfo := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "build_info", Help: "Build metadata; always 1.",
		ConstLabels: prometheus.Labels{"service": service, "version": build.Version, "commit": build.Commit},
	})
	buildInfo.Set(1)
	reg.MustRegister(
		m.requests, m.requestDuration, m.depRequests, m.depDuration, buildInfo,
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	return m
}

// Handler serves the registry in the Prometheus exposition format.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}

// ObserveDependency records one downstream call.
func (m *Metrics) ObserveDependency(dependency, outcome string, d time.Duration) {
	m.depRequests.WithLabelValues(dependency, outcome).Inc()
	m.depDuration.WithLabelValues(dependency).Observe(d.Seconds())
}

// instrument wraps h so each request is counted, timed, and logged under route.
func instrument(route string, h http.Handler, m *Metrics, logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		h.ServeHTTP(rec, r)
		elapsed := time.Since(start)

		m.requests.WithLabelValues(route, strconv.Itoa(rec.status)).Inc()
		m.requestDuration.WithLabelValues(route).Observe(elapsed.Seconds())

		level := slog.LevelInfo
		if rec.status >= 500 {
			level = slog.LevelError
		}
		logger.Log(r.Context(), level, "request",
			"request_id", requestID(r),
			"method", r.Method,
			"route", route,
			"status", rec.status,
			"duration_ms", elapsed.Milliseconds(),
		)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}
