package server

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tsengmingchun/quill/apps/agent/code/storage"
	"github.com/tsengmingchun/quill/apps/agent/code/workspace"
	"github.com/tsengmingchun/quill/apps/agent/llm"
)

func scrape(t *testing.T, m *Metrics) string {
	t.Helper()
	rec := httptest.NewRecorder()
	MetricsHandler(m).ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	if !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain; version=0.0.4") {
		t.Fatalf("content type = %q", rec.Header().Get("Content-Type"))
	}
	return rec.Body.String()
}

func TestObserveLabelsRequestsByRoutePattern(t *testing.T) {
	m := NewMetrics("v-test")
	var logs bytes.Buffer
	mux := http.NewServeMux()
	mux.HandleFunc("GET /threads/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	h := Observe(m, slog.New(slog.NewJSONHandler(&logs, nil)), mux)
	for _, p := range []string{"/threads/a", "/threads/b", "/nope"} {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", p, nil))
	}
	out := scrape(t, m)
	for _, want := range []string{
		`quill_build_info{version="v-test"} 1`,
		`quill_http_requests_total{method="GET",route="GET /threads/{id}",code="404"} 2`,
		`quill_http_requests_total{method="GET",route="unmatched",code="404"} 1`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("metrics lack %s:\n%s", want, out)
		}
	}
	if strings.Contains(out, "/threads/a") {
		t.Fatalf("thread IDs leaked into labels:\n%s", out)
	}
	if !strings.Contains(logs.String(), `"path":"/threads/a"`) || !strings.Contains(logs.String(), `"status":404`) {
		t.Fatalf("access log = %s", logs.String())
	}
}

func TestMetricsCountRuns(t *testing.T) {
	m := NewMetrics("v")
	svc, err := New(context.Background(), Config{
		Storage: storage.NewMemory(), Model: llm.NewFakeModel(llm.NewAssistantMessage("hi")), NoVerifyGate: true, Metrics: m,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	if _, err := collect(t, svc, userReq("t1", t.TempDir(), "hi")); err != nil {
		t.Fatal(err)
	}
	out := scrape(t, m)
	for _, want := range []string{`quill_runs_total{outcome="ok"} 1`, `quill_runs_total{outcome="error"} 0`, "quill_runs_active 0"} {
		if !strings.Contains(out, want) {
			t.Fatalf("metrics lack %s:\n%s", want, out)
		}
	}
}

func TestReadyz(t *testing.T) {
	old := readyTimeout
	readyTimeout = time.Minute
	t.Cleanup(func() { readyTimeout = old })
	ready := NewHandler(newWorkspaceService(t, true))
	rec := httptest.NewRecorder()
	ready.ServeHTTP(rec, httptest.NewRequest("GET", "/readyz", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"docker":"ok"`) {
		t.Fatalf("readyz = %d %s", rec.Code, rec.Body)
	}

	store := storage.NewMemory()
	m, err := workspace.NewManager(workspace.Config{
		Root: t.TempDir(), Store: store.Workspaces, Docker: workspace.CLIDocker{Binary: "/nonexistent/docker"},
	})
	if err != nil {
		t.Fatal(err)
	}
	svc, err := New(context.Background(), Config{Storage: store, Model: llm.NewFakeModel(), NoVerifyGate: true, Workspaces: m})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	rec = httptest.NewRecorder()
	NewHandler(svc).ServeHTTP(rec, httptest.NewRequest("GET", "/readyz", nil))
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), `"docker":"unavailable"`) {
		t.Fatalf("readyz without docker = %d %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "nonexistent") {
		t.Fatalf("readyz leaks error details: %s", rec.Body)
	}
}
