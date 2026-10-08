package server

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/tsengmingchun/quill/apps/agent/code/sessions"
	"github.com/tsengmingchun/quill/apps/agent/llm"
)

// TokenHeader carries the session token on every request to `quill serve`.
const TokenHeader = "X-Quill-Token"

// Info is what the desktop app needs to start: GET /info.
type Info struct {
	Cwd      string `json:"cwd"`
	ThreadID string `json:"thread_id"`
	// Model names the server default model, for display.
	Model       string `json:"model"`
	AutoApprove bool   `json:"auto_approve"`
	Version     string `json:"version"`
}

// AppOptions configures NewAppHandler.
type AppOptions struct {
	// API serves the agent routes (NewHandler).
	API   http.Handler
	Token string
	// Info answers GET /info; it runs per request so a reload picks the
	// newest thread.
	Info func(ctx context.Context) (Info, error)
	// SaveDefaultModel backs PUT /default-model (`/model --default`);
	// nil answers 501.
	SaveDefaultModel func(spec string) error
}

// NewToken returns a random session token.
func NewToken() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("server: token: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// NewAppHandler serves the API to the desktop app.
//
// Pages the agent opens in its browser run on localhost too, and a
// DNS-rebinding page can even look same-site, so every request must name a
// loopback Host and carry the token, and a browser Origin must match Host.
func NewAppHandler(opts AppOptions) (http.Handler, error) {
	if opts.API == nil || opts.Token == "" || opts.Info == nil {
		return nil, errors.New("server: API, Token and Info are required")
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /info", func(w http.ResponseWriter, r *http.Request) {
		info, err := opts.Info(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, info)
	})
	mux.HandleFunc("PUT /default-model", func(w http.ResponseWriter, r *http.Request) {
		saveDefaultModel(w, r, opts.SaveDefaultModel)
	})
	mux.Handle("/", opts.API)
	return requireLoopbackHost(requireToken(opts.Token, mux)), nil
}

// DefaultThread is the newest conversation with saved state in cwd, or a
// new thread ID. A failed first run may leave a catalog entry but no
// checkpoint.
func DefaultThread(ctx context.Context, srv Server, cwd string) (string, error) {
	list, err := srv.ListThreads(ctx, sessions.Filter{Cwd: cwd})
	if err != nil {
		return "", err
	}
	for _, item := range list {
		state, err := srv.GetThread(ctx, item.ThreadID)
		if err != nil {
			return "", err
		}
		if state != nil {
			return item.ThreadID, nil
		}
	}
	return sessions.NewThreadID(), nil
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
			http.Error(w, "quill only answers requests addressed to localhost", http.StatusForbidden)
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
			http.Error(w, "missing or wrong session token; see ~/.quill/agent.json", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}
