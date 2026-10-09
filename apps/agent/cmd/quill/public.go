package main

import (
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/tsengmingchun/quill/apps/agent"
	"github.com/tsengmingchun/quill/apps/agent/code/audit"
	"github.com/tsengmingchun/quill/apps/agent/code/auth"
	"github.com/tsengmingchun/quill/apps/agent/code/server"
)

// publicFlags configure `quill serve -public`, the team server.
type publicFlags struct {
	enabled       bool
	dailyLimitUSD float64
	origins       string
	metricsAddr   string
	logFormat     string

	metrics *server.Metrics
}

func (p *publicFlags) register(fs *flag.FlagSet) {
	fs.BoolVar(&p.enabled, "public", false,
		"serve remote users: listen on any address, require GitHub login ($QUILL_GITHUB_CLIENT_ID, $QUILL_ALLOWED_USERS, $"+auth.SecretKeyEnvVar+") and run only in -workspaces")
	limit, _ := strconv.ParseFloat(os.Getenv("QUILL_DAILY_LIMIT_USD"), 64)
	fs.Float64Var(&p.dailyLimitUSD, "daily-limit-usd", limit, "with -public: model spend per user per UTC day before runs are refused ($QUILL_DAILY_LIMIT_USD, 0 = no limit)")
	fs.StringVar(&p.origins, "allowed-origins", os.Getenv("QUILL_ALLOWED_ORIGINS"), "with -public: comma-separated browser origins allowed to call the API ($QUILL_ALLOWED_ORIGINS)")
	fs.StringVar(&p.metricsAddr, "metrics-addr", os.Getenv("QUILL_METRICS_ADDR"), "with -public: serve Prometheus GET /metrics on this separate address, e.g. :9090 ($QUILL_METRICS_ADDR)")
	fs.StringVar(&p.logFormat, "log-format", cmp.Or(os.Getenv("QUILL_LOG_FORMAT"), "json"), "with -public: request and server log format on stderr, json or text ($QUILL_LOG_FORMAT)")
}

// check validates the flags against the workspace flags and marks
// workspaces required.
func (p *publicFlags) check(ws *workspaceFlags) error {
	if !p.enabled {
		if p.dailyLimitUSD != 0 {
			return errors.New("-daily-limit-usd needs -public")
		}
		return nil
	}
	if p.logFormat != "json" && p.logFormat != "text" {
		return fmt.Errorf("-log-format %q: want json or text", p.logFormat)
	}
	if ws.root == "" {
		return errors.New("-public needs -workspaces: remote users may only run in workspace containers")
	}
	if ws.localRepos {
		return errors.New("-public cannot be combined with -workspace-local-repos")
	}
	if p.dailyLimitUSD < 0 {
		return errors.New("-daily-limit-usd must not be negative")
	}
	ws.require = true
	return nil
}

func (p *publicFlags) allowedOrigins() []string {
	var out []string
	for _, o := range strings.Split(p.origins, ",") {
		if o = strings.TrimSpace(o); o != "" {
			out = append(out, o)
		}
	}
	return out
}

// servePublic runs the team server until ctx is done.
func servePublic(ctx context.Context, e env, addr, model string, ws *workspaceFlags, pub *publicFlags, stderr io.Writer) error {
	var handler slog.Handler = slog.NewJSONHandler(stderr, nil)
	if pub.logFormat == "text" {
		handler = slog.NewTextHandler(stderr, nil)
	}
	log := slog.New(handler)
	slog.SetDefault(log)
	pub.metrics = server.NewMetrics(quill.Version)
	svc, err := newService(ctx, e, model, ws, pub)
	if err != nil {
		return fmt.Errorf("start agent: %w", err)
	}
	defer svc.Close()
	a, err := authService(svc)
	if err != nil {
		return err
	}
	h, err := server.NewPublicHandler(server.PublicOptions{
		API:            server.NewHandler(svc),
		Auth:           a,
		AllowedOrigins: pub.allowedOrigins(),
		Info: func(context.Context) (server.PublicInfo, error) {
			return server.PublicInfo{
				Model: svc.DefaultModel(), Version: quill.Version,
				Workspaces: true, DailyLimitUSD: pub.dailyLimitUSD,
			}, nil
		},
	})
	if err != nil {
		return err
	}
	if pub.metricsAddr != "" {
		go serveMetrics(ctx, pub.metricsAddr, pub.metrics, log)
	}
	context.AfterFunc(ctx, func() {
		log.Info("stopping; running turns get up to 30s to finish")
	})
	return server.ServePublic(ctx, addr, server.Observe(pub.metrics, log, h), func(a net.Addr) {
		log.Info("quill serve -public listening", "addr", a.String(), "model", svc.DefaultModel(),
			"storage", svc.Storage(), "workspaces", ws.root, "version", quill.Version)
	})
}

// serveMetrics serves GET /metrics until ctx is done. It runs on its own
// address so the public listener never exposes it.
func serveMetrics(ctx context.Context, addr string, m *server.Metrics, log *slog.Logger) {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", server.MetricsHandler(m))
	hs := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	context.AfterFunc(ctx, func() { _ = hs.Close() })
	log.Info("metrics listening", "addr", addr)
	if err := hs.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("metrics server", "err", err)
	}
}

// authService reads the GitHub OAuth app and allow list from the
// environment; login and refusal events go to the audit log.
func authService(svc *server.Service) (*auth.Service, error) {
	key := os.Getenv(auth.SecretKeyEnvVar)
	if key == "" {
		return nil, fmt.Errorf("-public needs $%s to encrypt GitHub tokens (e.g. `openssl rand -base64 32`)", auth.SecretKeyEnvVar)
	}
	cipher, err := auth.NewCipher(key)
	if err != nil {
		return nil, err
	}
	var allowed []string
	for _, u := range strings.Split(os.Getenv("QUILL_ALLOWED_USERS"), ",") {
		if u = strings.TrimSpace(u); u != "" {
			allowed = append(allowed, u)
		}
	}
	a := &auth.Service{
		GitHub:  &auth.GitHub{ClientID: os.Getenv("QUILL_GITHUB_CLIENT_ID")},
		Store:   svc.Users(),
		Cipher:  cipher,
		Allowed: allowed,
	}
	if log := svc.Audit(); log != nil {
		a.OnEvent = func(ctx context.Context, user, action string) {
			if err := log.Record(ctx, audit.Event{User: user, Action: action}); err != nil {
				slog.Warn("audit record", "action", action, "user", user, "err", err)
			}
		}
	}
	if err := a.Validate(); err != nil {
		return nil, err
	}
	return a, nil
}
