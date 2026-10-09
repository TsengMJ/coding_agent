package server

import (
	"context"
	"errors"
	"net"
	"net/http"
	"slices"

	"github.com/tsengmingchun/quill/apps/agent/code/auth"
)

// PublicInfo is GET /info of the team server.
type PublicInfo struct {
	Model   string    `json:"model"`
	Version string    `json:"version"`
	User    auth.User `json:"user"`
	// Workspaces is true: runs need a workspace (a server-side clone).
	Workspaces    bool    `json:"workspaces"`
	DailyLimitUSD float64 `json:"daily_limit_usd,omitempty"`
}

// PublicOptions configures NewPublicHandler.
type PublicOptions struct {
	// API serves the agent routes (NewHandler).
	API  http.Handler
	Auth *auth.Service
	// Info answers GET /info for the signed-in user.
	Info func(ctx context.Context) (PublicInfo, error)
	// AllowedOrigins are the browser origins (e.g. the desktop renderer)
	// allowed to call the API cross-origin. Requests without an Origin
	// header (the desktop main process, curl) are not affected.
	AllowedOrigins []string
}

// NewPublicHandler serves the API to remote users: GET /healthz and the
// /auth/* login routes are open, every other route needs a session from
// the GitHub device login.
func NewPublicHandler(opts PublicOptions) (http.Handler, error) {
	if opts.API == nil || opts.Auth == nil || opts.Info == nil {
		return nil, errors.New("server: API, Auth and Info are required")
	}
	if err := opts.Auth.Validate(); err != nil {
		return nil, err
	}
	private := http.NewServeMux()
	private.HandleFunc("GET /info", func(w http.ResponseWriter, r *http.Request) {
		info, err := opts.Info(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		info.User, _ = auth.UserFrom(r.Context())
		writeJSON(w, http.StatusOK, info)
	})
	private.Handle("/", opts.API)

	mux := http.NewServeMux()
	mux.Handle("GET /healthz", opts.API)
	opts.Auth.Routes(mux)
	mux.Handle("/", opts.Auth.Require(private))
	return checkOrigin(opts.AllowedOrigins, mux), nil
}

// checkOrigin refuses browser requests from other origins and answers CORS
// for the allowed ones. Sessions travel in the Authorization header, which
// browsers never attach on their own, so this is defense in depth.
func checkOrigin(allowed []string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin == "" {
			next.ServeHTTP(w, r)
			return
		}
		if !slices.Contains(allowed, origin) {
			http.Error(w, "cross-origin request refused", http.StatusForbidden)
			return
		}
		h := w.Header()
		h.Set("Access-Control-Allow-Origin", origin)
		h.Add("Vary", "Origin")
		if r.Method == http.MethodOptions {
			h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE")
			h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
			h.Set("Access-Control-Max-Age", "600")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ServePublic is ServeHandler for the team server: it may listen on any
// address. h must authenticate every route that runs the agent
// (NewPublicHandler), and runs must be confined to workspaces.
func ServePublic(ctx context.Context, addr string, h http.Handler, ready func(net.Addr)) error {
	return serve(ctx, addr, h, ready)
}
