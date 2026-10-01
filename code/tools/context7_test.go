package tools

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/tsengmingchun/deepagents_golang/code/cache"
	"github.com/tsengmingchun/deepagents_golang/tool"
)

func newContext7(t *testing.T, h http.HandlerFunc) (resolve, query tool.Tool, hits *atomic.Int32) {
	t.Helper()
	hits = &atomic.Int32{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if got := r.Header.Get("Authorization"); got != "Bearer ctx7sk-test" {
			t.Errorf("authorization = %q", got)
		}
		h(w, r)
	}))
	t.Cleanup(srv.Close)
	tools := NewContext7Tools(Context7Options{APIKey: "ctx7sk-test", BaseURL: srv.URL + "/api", Client: srv.Client(), Cache: cache.NewMemory()})
	return tools[0], tools[1], hits
}

func TestResolveLibraryIDListsMatches(t *testing.T) {
	resolve, _, _ := newContext7(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/libs/search" || r.URL.Query().Get("libraryName") != "react" || r.URL.Query().Get("query") != "manage state" {
			t.Errorf("request = %s", r.URL)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"results":[{"id":"/facebook/react","title":"React","description":"UI library","totalSnippets":2500,"trustScore":10,"benchmarkScore":95.5,"versions":["v18.2.0","v17.0.2"]}]}`))
	})
	if resolve.Name() != ResolveLibraryIDName {
		t.Fatalf("name = %q", resolve.Name())
	}
	out, isErr := run(t, resolve, map[string]any{"library_name": "react", "query": "manage state"})
	if isErr {
		t.Fatal(out)
	}
	for _, want := range []string{"React", "/facebook/react", "UI library", "2500", "v18.2.0", "/facebook/react@v18.2.0"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestResolveLibraryIDNoMatches(t *testing.T) {
	resolve, _, _ := newContext7(t, func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte(`{"results":[]}`)) })
	out, isErr := run(t, resolve, map[string]any{"library_name": "nope", "query": "x"})
	if isErr || !strings.Contains(out, "No Context7 library matched") {
		t.Fatalf("%v %s", isErr, out)
	}
}

func TestQueryDocsReturnsTextAndCaches(t *testing.T) {
	_, query, hits := newContext7(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.URL.Path != "/api/v2/context" || q.Get("libraryId") != "/vercel/next.js@v15.1.8" || q.Get("query") != "middleware auth" || q.Get("type") != "txt" {
			t.Errorf("request = %s", r.URL)
		}
		w.Write([]byte("### Middleware\n\nUse middleware.ts ..."))
	})
	args := map[string]any{"library_id": "/vercel/next.js@v15.1.8", "query": "middleware auth"}
	out, isErr := run(t, query, args)
	if isErr || !strings.Contains(out, "Use middleware.ts") || !strings.Contains(out, "/vercel/next.js@v15.1.8") {
		t.Fatalf("%v %s", isErr, out)
	}
	if again, _ := run(t, query, args); again != out || hits.Load() != 1 {
		t.Fatalf("not cached: hits = %d", hits.Load())
	}
}

func TestQueryDocsValidatesLibraryID(t *testing.T) {
	_, query, hits := newContext7(t, func(http.ResponseWriter, *http.Request) {})
	out, isErr := run(t, query, map[string]any{"library_id": "react", "query": "x"})
	if !isErr || !strings.Contains(out, "resolve_library_id") || hits.Load() != 0 {
		t.Fatalf("%v %s", isErr, out)
	}
}

func TestContext7ErrorsAreActionable(t *testing.T) {
	cases := map[int]string{
		http.StatusUnauthorized:    "CONTEXT7_API_KEY",
		http.StatusPaymentRequired: "spending limit",
		http.StatusNotFound:        "resolve_library_id",
		http.StatusTooManyRequests: "retry after 30s",
		http.StatusAccepted:        "still being processed",
	}
	for code, want := range cases {
		_, query, _ := newContext7(t, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Retry-After", "30")
			w.WriteHeader(code)
			w.Write([]byte(`{"error":"x","message":"detail"}`))
		})
		out, isErr := run(t, query, map[string]any{"library_id": "/a/b", "query": "q"})
		if !isErr || !strings.Contains(out, want) {
			t.Errorf("%d: %v %s", code, isErr, out)
		}
	}
}

func TestContext7RedirectNamesNewID(t *testing.T) {
	_, query, _ := newContext7(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusMovedPermanently)
		w.Write([]byte(`{"error":"library_redirected","message":"moved","redirectUrl":"/new/repo"}`))
	})
	out, isErr := run(t, query, map[string]any{"library_id": "/old/repo", "query": "q"})
	if !isErr || !strings.Contains(out, "/new/repo") {
		t.Fatalf("%v %s", isErr, out)
	}
}
