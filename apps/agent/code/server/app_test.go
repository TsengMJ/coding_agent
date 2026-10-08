package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testToken = "secret"

func newAppHandler(t *testing.T, opts AppOptions) http.Handler {
	t.Helper()
	if opts.API == nil {
		opts.API = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, "api:"+r.URL.Path)
		})
	}
	if opts.Token == "" {
		opts.Token = testToken
	}
	if opts.Info == nil {
		opts.Info = func(context.Context) (Info, error) {
			return Info{Cwd: "/proj", ThreadID: "t1", Model: "fake:m"}, nil
		}
	}
	h, err := NewAppHandler(opts)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func serveApp(h http.Handler, method, target, body string, headers map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	r.Host = "127.0.0.1:8123"
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

func TestAppRequiresToken(t *testing.T) {
	h := newAppHandler(t, AppOptions{})
	if w := serveApp(h, "GET", "/threads", "", nil); w.Code != http.StatusForbidden {
		t.Fatalf("no token: %d", w.Code)
	}
	if w := serveApp(h, "GET", "/threads", "", map[string]string{TokenHeader: "wrong"}); w.Code != http.StatusForbidden {
		t.Fatalf("wrong token: %d", w.Code)
	}
	w := serveApp(h, "GET", "/threads", "", map[string]string{TokenHeader: testToken})
	if w.Code != http.StatusOK || w.Body.String() != "api:/threads" {
		t.Fatalf("with token: %d %q", w.Code, w.Body.String())
	}
}

func TestAppRefusesForeignOrigin(t *testing.T) {
	h := newAppHandler(t, AppOptions{})
	w := serveApp(h, "POST", "/runs", "", map[string]string{TokenHeader: testToken, "Origin": "http://localhost:3000"})
	if w.Code != http.StatusForbidden {
		t.Fatalf("page on another localhost port: %d", w.Code)
	}
	w = serveApp(h, "POST", "/runs", "", map[string]string{TokenHeader: testToken, "Origin": "http://127.0.0.1:8123"})
	if w.Code != http.StatusOK {
		t.Fatalf("same origin: %d", w.Code)
	}
}

func TestAppRefusesNonLoopbackHost(t *testing.T) {
	h := newAppHandler(t, AppOptions{})
	for _, host := range []string{"evil.example:8123", "10.0.0.2:8123"} {
		if w := serveApp(h, "GET", "/threads", "", map[string]string{"Host": host, TokenHeader: testToken}); w.Code != http.StatusForbidden {
			t.Errorf("host %s: %d", host, w.Code)
		}
	}
	for _, host := range []string{"localhost:8123", "[::1]:8123"} {
		if w := serveApp(h, "GET", "/threads", "", map[string]string{"Host": host, TokenHeader: testToken}); w.Code != http.StatusOK {
			t.Errorf("host %s: %d", host, w.Code)
		}
	}
}

func TestAppInfo(t *testing.T) {
	h := newAppHandler(t, AppOptions{})
	w := serveApp(h, "GET", "/info", "", map[string]string{TokenHeader: testToken})
	var info Info
	if err := json.Unmarshal(w.Body.Bytes(), &info); err != nil || info.Cwd != "/proj" || info.ThreadID != "t1" {
		t.Fatalf("info = %+v, %v (%s)", info, err, w.Body.String())
	}
}

func TestAppSaveDefaultModel(t *testing.T) {
	var saved string
	h := newAppHandler(t, AppOptions{SaveDefaultModel: func(spec string) error {
		saved = spec
		return nil
	}})
	put := func(body string) *httptest.ResponseRecorder {
		return serveApp(h, "PUT", "/default-model", body, map[string]string{TokenHeader: testToken})
	}
	if w := put(`{"model":"openai:gpt-x"}`); w.Code != http.StatusNoContent || saved != "openai:gpt-x" {
		t.Fatalf("save: %d %q saved=%q", w.Code, w.Body.String(), saved)
	}
	if w := put(`{"model":"nope:x"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("bad provider: %d", w.Code)
	}
}

func TestAppSaveDefaultModelUnavailable(t *testing.T) {
	h := newAppHandler(t, AppOptions{})
	w := serveApp(h, "PUT", "/default-model", `{"model":"openai:x"}`, map[string]string{TokenHeader: testToken})
	if w.Code != http.StatusNotImplemented {
		t.Fatalf("got %d", w.Code)
	}
}

func TestNewAppHandlerNeedsAPITokenAndInfo(t *testing.T) {
	info := func(context.Context) (Info, error) { return Info{}, nil }
	if _, err := NewAppHandler(AppOptions{Token: "x", Info: info}); err == nil {
		t.Fatal("missing API accepted")
	}
	if _, err := NewAppHandler(AppOptions{API: http.NotFoundHandler(), Info: info}); err == nil {
		t.Fatal("missing token accepted")
	}
	if _, err := NewAppHandler(AppOptions{API: http.NotFoundHandler(), Token: "x"}); err == nil {
		t.Fatal("missing info accepted")
	}
}

type fakeBrowserControl struct {
	endpoint string
}

func (f *fakeBrowserControl) SetBrowserEndpoint(u string) error {
	if u != "" && !strings.HasPrefix(u, "http://127.0.0.1:") {
		return errors.New("not loopback")
	}
	f.endpoint = u
	return nil
}

func (f *fakeBrowserControl) BrowserStatus() BrowserStatus {
	return BrowserStatus{Connected: f.endpoint != "", Default: "playwright"}
}

func TestAppRegistersTheBrowserPanel(t *testing.T) {
	bc := &fakeBrowserControl{}
	h := newAppHandler(t, AppOptions{Browser: bc})
	auth := map[string]string{TokenHeader: testToken}
	if w := serveApp(h, "PUT", "/browser", `{"cdp_url":"http://127.0.0.1:9333"}`, auth); w.Code != http.StatusNoContent || bc.endpoint != "http://127.0.0.1:9333" {
		t.Fatalf("put: %d %q endpoint=%q", w.Code, w.Body.String(), bc.endpoint)
	}
	w := serveApp(h, "GET", "/browser", "", auth)
	var st BrowserStatus
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil || !st.Connected || st.Default != "playwright" {
		t.Fatalf("status = %+v, %v", st, err)
	}
	if w := serveApp(h, "PUT", "/browser", `{"cdp_url":"http://example.com:1"}`, auth); w.Code != http.StatusBadRequest {
		t.Fatalf("remote endpoint: %d", w.Code)
	}
	if w := serveApp(h, "PUT", "/browser", `{"cdp_url":""}`, auth); w.Code != http.StatusNoContent || bc.endpoint != "" {
		t.Fatalf("clear: %d endpoint=%q", w.Code, bc.endpoint)
	}
}

func TestAppBrowserUnavailable(t *testing.T) {
	h := newAppHandler(t, AppOptions{})
	if w := serveApp(h, "PUT", "/browser", `{"cdp_url":""}`, map[string]string{TokenHeader: testToken}); w.Code != http.StatusNotImplemented {
		t.Fatalf("got %d", w.Code)
	}
}
