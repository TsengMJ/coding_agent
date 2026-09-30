package gui

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const token = "secret"

func newHandler(t *testing.T, api http.Handler) http.Handler {
	t.Helper()
	if api == nil {
		api = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, "api:"+r.URL.Path)
		})
	}
	h, err := Handler(Options{API: api, Token: token, Info: Info{Cwd: "/proj", ThreadID: "t1", Model: "fake:m"}})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func serve(h http.Handler, method, target string, headers map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target, nil)
	r.Host = "127.0.0.1:5000"
	for k, v := range headers {
		if k == "Host" {
			r.Host = v
			continue
		}
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestAPIRequiresToken(t *testing.T) {
	h := newHandler(t, nil)
	if w := serve(h, "GET", "/api/threads", nil); w.Code != http.StatusForbidden {
		t.Fatalf("no token: %d", w.Code)
	}
	if w := serve(h, "GET", "/api/threads", map[string]string{TokenHeader: "wrong"}); w.Code != http.StatusForbidden {
		t.Fatalf("wrong token: %d", w.Code)
	}
	w := serve(h, "GET", "/api/threads", map[string]string{TokenHeader: token})
	if w.Code != http.StatusOK || w.Body.String() != "api:/threads" {
		t.Fatalf("with token: %d %q", w.Code, w.Body.String())
	}
}

func TestAPIRefusesForeignOrigin(t *testing.T) {
	h := newHandler(t, nil)
	w := serve(h, "POST", "/api/runs", map[string]string{TokenHeader: token, "Origin": "http://evil.example"})
	if w.Code != http.StatusForbidden {
		t.Fatalf("foreign origin: %d", w.Code)
	}
	w = serve(h, "POST", "/api/runs", map[string]string{TokenHeader: token, "Origin": "http://127.0.0.1:5000"})
	if w.Code != http.StatusOK {
		t.Fatalf("same origin: %d", w.Code)
	}
}

func TestNonLoopbackHostIsRefused(t *testing.T) {
	h := newHandler(t, nil)
	for _, host := range []string{"evil.example:5000", "10.0.0.2:5000"} {
		if w := serve(h, "GET", "/", map[string]string{"Host": host}); w.Code != http.StatusForbidden {
			t.Errorf("host %s: %d", host, w.Code)
		}
	}
	for _, host := range []string{"localhost:5000", "[::1]:5000"} {
		if w := serve(h, "GET", "/", map[string]string{"Host": host}); w.Code != http.StatusOK {
			t.Errorf("host %s: %d", host, w.Code)
		}
	}
}

func TestInfoEndpoint(t *testing.T) {
	h := newHandler(t, nil)
	w := serve(h, "GET", "/api/info", map[string]string{TokenHeader: token})
	var info Info
	if err := json.Unmarshal(w.Body.Bytes(), &info); err != nil || info.Cwd != "/proj" || info.ThreadID != "t1" {
		t.Fatalf("info = %+v, %v (%s)", info, err, w.Body.String())
	}
}

func TestStaticFallsBackToIndex(t *testing.T) {
	h := newHandler(t, nil)
	for _, p := range []string{"/", "/threads/abc"} {
		w := serve(h, "GET", p, nil)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "<html") {
			t.Errorf("GET %s = %d %.60q", p, w.Code, w.Body.String())
		}
	}
}

func TestRemoteAPIForwardsWithoutToken(t *testing.T) {
	var gotPath, gotToken string
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotToken = r.URL.Path, r.Header.Get(TokenHeader)
		_, _ = io.WriteString(w, "remote")
	}))
	defer remote.Close()
	api, err := RemoteAPI(remote.URL)
	if err != nil {
		t.Fatal(err)
	}
	h := newHandler(t, api)
	w := serve(h, "GET", "/api/threads/x/trace", map[string]string{TokenHeader: token})
	if w.Body.String() != "remote" || gotPath != "/threads/x/trace" || gotToken != "" {
		t.Fatalf("body=%q path=%q token=%q", w.Body.String(), gotPath, gotToken)
	}
}

func TestHandlerNeedsAPIAndToken(t *testing.T) {
	if _, err := Handler(Options{Token: "x"}); err == nil {
		t.Fatal("missing API accepted")
	}
	if _, err := Handler(Options{API: http.NotFoundHandler()}); err == nil {
		t.Fatal("missing token accepted")
	}
}
