// Package gui serves the dcode web UI: the embedded single-page app plus
// the agent API under /api, guarded so that only the browser tab dcode
// opened can drive the agent.
package gui

import (
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os/exec"
	"path"
	"runtime"
	"strings"

	"github.com/tsengmingchun/deepagents_golang/llm"
)

//go:embed all:dist
var dist embed.FS

// TokenHeader carries the session token on every API request.
const TokenHeader = "X-Dcode-Token"

// Info is what the page needs to start: GET /api/info.
type Info struct {
	Cwd      string `json:"cwd"`
	ThreadID string `json:"thread_id"`
	// Model names the server default model, for display.
	Model string `json:"model"`
	// ModelOverride is sent with every run until /model changes it (dcode -model).
	ModelOverride string `json:"model_override,omitempty"`
	AutoApprove   bool   `json:"auto_approve"`
	Version       string `json:"version"`
}

// Options configures Handler.
type Options struct {
	// API serves the agent routes (server.NewHandler or RemoteAPI); it sees
	// paths with the /api prefix removed.
	API   http.Handler
	Token string
	Info  Info
	// SaveDefaultModel backs PUT /api/default-model (`/model --default`);
	// nil answers 501.
	SaveDefaultModel func(spec string) error
}

// NewToken returns a random session token.
func NewToken() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("gui: token: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// Handler serves the app and the API.
//
// A web page on another site can make the browser send requests to
// 127.0.0.1, and a DNS-rebinding page can even make them look same-site,
// so every request must name a loopback Host, and API requests must carry
// the token and, when the browser sends one, a matching Origin.
func Handler(opts Options) (http.Handler, error) {
	if opts.API == nil || opts.Token == "" {
		return nil, errors.New("gui: API and Token are required")
	}
	static, err := fs.Sub(dist, "dist")
	if err != nil {
		return nil, fmt.Errorf("gui: embedded app: %w", err)
	}
	api := http.NewServeMux()
	api.HandleFunc("GET /info", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(opts.Info)
	})
	api.HandleFunc("PUT /default-model", func(w http.ResponseWriter, r *http.Request) {
		saveDefaultModel(w, r, opts.SaveDefaultModel)
	})
	api.Handle("/", opts.API)

	mux := http.NewServeMux()
	mux.Handle("/api/", http.StripPrefix("/api", requireToken(opts.Token, api)))
	mux.Handle("/", spa(static))
	return requireLoopbackHost(mux), nil
}

func saveDefaultModel(w http.ResponseWriter, r *http.Request, save func(string) error) {
	if save == nil {
		http.Error(w, "saving the default model is not available", http.StatusNotImplemented)
		return
	}
	var body struct {
		Model string `json:"model"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		http.Error(w, "want {\"model\": \"provider:model\"}", http.StatusBadRequest)
		return
	}
	if err := llm.ValidateModelSpec(body.Model); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := save(body.Model); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func requireLoopbackHost(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isLoopbackHost(r.Host) {
			http.Error(w, "dcode GUI only answers requests addressed to localhost", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func isLoopbackHost(hostport string) bool {
	host, _, err := net.SplitHostPort(hostport)
	if err != nil {
		host = hostport
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

func requireToken(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); origin != "" {
			u, err := url.Parse(origin)
			if err != nil || u.Host != r.Host {
				http.Error(w, "cross-origin request refused", http.StatusForbidden)
				return
			}
		}
		got := r.Header.Get(TokenHeader)
		if subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
			http.Error(w, "missing or wrong session token; open the URL dcode printed", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// spa serves files from static and falls back to index.html so client-side
// routes load the app.
func spa(static fs.FS) http.Handler {
	files := http.FileServerFS(static)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if name == "" {
			name = "index.html"
		}
		if _, err := fs.Stat(static, name); err != nil {
			r = r.Clone(r.Context())
			r.URL.Path = "/"
			name = "index.html"
		}
		if strings.HasPrefix(name, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		files.ServeHTTP(w, r)
	})
}

// RemoteAPI forwards API requests to a `dcode serve` at base, streaming
// run events as they arrive.
func RemoteAPI(base string) (http.Handler, error) {
	u, err := url.Parse(strings.TrimRight(base, "/"))
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("gui: server URL %q: want http://host:port", base)
	}
	p := httputil.NewSingleHostReverseProxy(u)
	p.FlushInterval = -1
	director := p.Director
	p.Director = func(r *http.Request) {
		director(r)
		r.Host = u.Host
		r.Header.Del(TokenHeader)
		r.Header.Del("Origin")
	}
	return p, nil
}

// OpenBrowser opens url in the default browser.
func OpenBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("open browser: %w", err)
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
