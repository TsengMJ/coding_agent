package server

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/tsengmingchun/quill/apps/agent/code/auth"
)

// Metrics counts requests and runs and writes them in the Prometheus text
// format. The zero value is not usable; call NewMetrics.
type Metrics struct {
	version string

	mu         sync.Mutex
	requests   map[requestKey]uint64
	runs       map[string]uint64
	runSeconds float64
	costUSD    float64
	active     int
}

type requestKey struct{ method, route, code string }

func NewMetrics(version string) *Metrics {
	return &Metrics{version: version, requests: map[requestKey]uint64{}, runs: map[string]uint64{}}
}

// Run outcomes.
const (
	runOK          = "ok"
	runInterrupted = "interrupted"
	runFailed      = "error"
)

func (m *Metrics) runStarted() {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.active++
}

func (m *Metrics) runFinished(outcome string, d time.Duration, costUSD float64) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.active--
	m.runs[outcome]++
	m.runSeconds += d.Seconds()
	m.costUSD += costUSD
}

func (m *Metrics) request(method, route string, code int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests[requestKey{method, route, fmt.Sprint(code)}]++
}

// WriteTo writes the metrics in the Prometheus text exposition format.
func (m *Metrics) WriteTo(w io.Writer) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var b strings.Builder
	fmt.Fprintf(&b, "# HELP quill_build_info Running quill version.\n# TYPE quill_build_info gauge\nquill_build_info{version=%q} 1\n", m.version)

	b.WriteString("# HELP quill_http_requests_total HTTP requests by route pattern and status.\n# TYPE quill_http_requests_total counter\n")
	keys := make([]requestKey, 0, len(m.requests))
	for k := range m.requests {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, c := keys[i], keys[j]
		return a.route+a.method+a.code < c.route+c.method+c.code
	})
	for _, k := range keys {
		fmt.Fprintf(&b, "quill_http_requests_total{method=%q,route=%q,code=%q} %d\n", k.method, k.route, k.code, m.requests[k])
	}

	b.WriteString("# HELP quill_runs_total Finished agent runs by outcome.\n# TYPE quill_runs_total counter\n")
	for _, o := range []string{runOK, runInterrupted, runFailed} {
		fmt.Fprintf(&b, "quill_runs_total{outcome=%q} %d\n", o, m.runs[o])
	}
	fmt.Fprintf(&b, "# HELP quill_run_seconds_total Time spent in agent runs.\n# TYPE quill_run_seconds_total counter\nquill_run_seconds_total %g\n", m.runSeconds)
	fmt.Fprintf(&b, "# HELP quill_runs_active Agent runs in progress.\n# TYPE quill_runs_active gauge\nquill_runs_active %d\n", m.active)
	fmt.Fprintf(&b, "# HELP quill_model_cost_usd_total Estimated model spend of finished runs.\n# TYPE quill_model_cost_usd_total counter\nquill_model_cost_usd_total %g\n", m.costUSD)
	n, err := io.WriteString(w, b.String())
	return int64(n), err
}

// MetricsHandler serves GET /metrics.
func MetricsHandler(m *Metrics) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		_, _ = m.WriteTo(w)
	})
}

// Observe logs every request and counts it in m (when non-nil). Routes are
// labelled by their mux pattern, so thread IDs never become label values.
func Observe(m *Metrics, log *slog.Logger, next http.Handler) http.Handler {
	if log == nil {
		log = slog.Default()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, code: http.StatusOK}
		next.ServeHTTP(rec, r)
		route := r.Pattern
		if route == "" {
			route = "unmatched"
		}
		if m != nil {
			m.request(r.Method, route, rec.code)
		}
		attrs := []any{"method", r.Method, "route", route, "path", r.URL.Path, "status", rec.code,
			"duration_ms", time.Since(start).Milliseconds()}
		if u, ok := auth.UserFrom(r.Context()); ok {
			attrs = append(attrs, "user", u.ID)
		}
		log.Info("http request", attrs...)
	})
}

// statusRecorder remembers the response status; Unwrap keeps
// http.ResponseController (flushes, write deadlines) working.
type statusRecorder struct {
	http.ResponseWriter
	code        int
	wroteHeader bool
}

func (s *statusRecorder) WriteHeader(code int) {
	if !s.wroteHeader {
		s.code, s.wroteHeader = code, true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	s.wroteHeader = true
	return s.ResponseWriter.Write(b)
}

func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// readyTimeout bounds each dependency check of GET /readyz.
var readyTimeout = 3 * time.Second

// Ready checks the service's dependencies: Postgres, Redis and, with
// workspaces, the Docker daemon. A nil error means healthy.
func (s *Service) Ready(ctx context.Context) map[string]error {
	ctx, cancel := context.WithTimeout(ctx, readyTimeout)
	defer cancel()
	out := s.store.Ping(ctx)
	if s.workspaces != nil {
		out["docker"] = s.workspaces.Ping(ctx)
	}
	return out
}

// readinessChecker backs GET /readyz.
type readinessChecker interface {
	Ready(ctx context.Context) map[string]error
}

// serveReady answers 200 when every check passes, else 503. Only check
// names and "ok"/"unavailable" are returned; the errors go to the log.
func serveReady(w http.ResponseWriter, r *http.Request, rc readinessChecker) {
	checks := map[string]string{}
	status, code := "ready", http.StatusOK
	for name, err := range rc.Ready(r.Context()) {
		if err != nil {
			slog.Warn("readiness check failed", "check", name, "err", err)
			checks[name], status, code = "unavailable", "unavailable", http.StatusServiceUnavailable
			continue
		}
		checks[name] = "ok"
	}
	writeJSON(w, code, map[string]any{"status": status, "checks": checks})
}
