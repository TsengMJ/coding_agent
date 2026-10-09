package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/tsengmingchun/quill/apps/agent/code/audit"
	"github.com/tsengmingchun/quill/apps/agent/code/commands"
	"github.com/tsengmingchun/quill/apps/agent/code/sessions"
	"github.com/tsengmingchun/quill/apps/agent/code/trace"
	"github.com/tsengmingchun/quill/apps/agent/code/workspace"
)

// DefaultAddr is where `quill serve` listens by default.
const DefaultAddr = "127.0.0.1:8123"

// maxRequestBytes caps a POST /runs body.
const maxRequestBytes = 16 << 20

var (
	// eventWriteTimeout bounds writing one event to a /runs client.
	eventWriteTimeout = 30 * time.Second
	// shutdownGrace is how long in-flight runs may keep going after the
	// server is asked to stop, before they are cancelled.
	shutdownGrace = 30 * time.Second
)

// NewHandler exposes srv over HTTP:
//
//	GET  /healthz         -> {"status":"ok","model":default spec when known}
//	POST /runs            RunRequest -> NDJSON stream of Event, ending in done or error;
//	                      a client that disconnects cancels the run unless it posted ?detach=1
//	GET  /threads/{id}/events -> the running run's stream from its first event (204 when idle)
//	POST /threads/{id}/cancel -> 202, cancels the running run (404 when idle)
//	GET  /threads         ?cwd=&limit= -> []sessions.Info
//	GET  /threads/{id}    -> ThreadState (404 when unknown)
//	PATCH /threads/{id}    {"title":"..."} -> sessions.Info
//	GET  /threads/{id}/trace -> trace.Trace (model call summaries, diffs, subagent messages)
//	GET  /threads/{id}/trace/llm/{seq} -> trace.LLMCallDetail (404 when unknown)
//	POST /threads/{id}/compact -> CompactResult (404 unknown, 409 busy/pending/too short)
//	GET  /threads/{id}/usage -> ThreadUsage (404 when unknown)
//	GET  /commands        -> []commands.Command (quill slash commands, hidden ones omitted)
//	GET  /audit           ?limit= -> []audit.Event of the signed-in user, newest first
func NewHandler(srv Server) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		body := map[string]string{"status": "ok"}
		if d, ok := srv.(interface{ DefaultModel() string }); ok {
			body["model"] = d.DefaultModel()
		}
		writeJSON(w, http.StatusOK, body)
	})
	mux.HandleFunc("GET /commands", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, commands.Visible(commands.Quill))
	})
	runs := newLiveRuns()
	mux.HandleFunc("POST /runs", func(w http.ResponseWriter, r *http.Request) {
		handleRun(w, r, srv, runs)
	})
	authorize := func(w http.ResponseWriter, r *http.Request) bool {
		a, ok := srv.(threadAuthorizer)
		if !ok {
			return true
		}
		if err := a.AuthorizeThread(r.Context(), r.PathValue("id")); err != nil {
			writeError(w, statusFor(err), err)
			return false
		}
		return true
	}
	mux.HandleFunc("GET /threads/{id}/events", func(w http.ResponseWriter, r *http.Request) {
		if !authorize(w, r) {
			return
		}
		run := runs.get(r.PathValue("id"))
		if run == nil {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		streamRun(w, r, run)
	})
	mux.HandleFunc("POST /threads/{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		if !authorize(w, r) {
			return
		}
		run := runs.get(r.PathValue("id"))
		if run == nil {
			writeError(w, http.StatusNotFound, errors.New("thread has no running run"))
			return
		}
		run.cancel()
		w.WriteHeader(http.StatusAccepted)
	})
	mux.HandleFunc("GET /threads", func(w http.ResponseWriter, r *http.Request) {
		f := sessions.Filter{Cwd: r.URL.Query().Get("cwd")}
		if s := r.URL.Query().Get("limit"); s != "" {
			n, err := strconv.Atoi(s)
			if err != nil {
				writeError(w, http.StatusBadRequest, fmt.Errorf("limit: %w", err))
				return
			}
			f.Limit = n
		}
		list, err := srv.ListThreads(r.Context(), f)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if list == nil {
			list = []sessions.Info{}
		}
		writeJSON(w, http.StatusOK, list)
	})
	mux.HandleFunc("GET /threads/{id}", func(w http.ResponseWriter, r *http.Request) {
		ts, err := srv.GetThread(r.Context(), r.PathValue("id"))
		if err != nil {
			writeError(w, statusFor(err), err)
			return
		}
		if ts == nil {
			writeError(w, http.StatusNotFound, errors.New("thread not found"))
			return
		}
		writeJSON(w, http.StatusOK, ts)
	})
	mux.HandleFunc("GET /threads/{id}/history", func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		before, limit, err := historyParams(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		page, err := srv.GetHistory(r.Context(), r.PathValue("id"), before, limit)
		if err != nil {
			writeError(w, statusFor(err), err)
			return
		}
		if page == nil {
			writeError(w, http.StatusNotFound, errors.New("thread not found"))
			return
		}
		w.Header().Set("Server-Timing", fmt.Sprintf("history;dur=%.1f", float64(time.Since(start).Microseconds())/1000))
		writeJSONMeasured(w, http.StatusOK, page)
	})
	mux.HandleFunc("PATCH /threads/{id}", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Title string `json:"title"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("decode title: %w", err))
			return
		}
		info, err := srv.RenameThread(r.Context(), r.PathValue("id"), body.Title)
		if err != nil {
			writeError(w, statusFor(err), err)
			return
		}
		writeJSON(w, http.StatusOK, info)
	})
	mux.HandleFunc("GET /threads/{id}/usage", func(w http.ResponseWriter, r *http.Request) {
		u, err := srv.GetUsage(r.Context(), r.PathValue("id"))
		if err != nil {
			writeError(w, statusFor(err), err)
			return
		}
		if u == nil {
			writeError(w, http.StatusNotFound, errors.New("thread not found"))
			return
		}
		writeJSON(w, http.StatusOK, u)
	})
	mux.HandleFunc("GET /threads/{id}/trace", func(w http.ResponseWriter, r *http.Request) {
		t, err := srv.GetTrace(r.Context(), r.PathValue("id"))
		if err != nil {
			writeError(w, statusFor(err), err)
			return
		}
		writeJSONMeasured(w, http.StatusOK, t)
	})
	mux.HandleFunc("GET /threads/{id}/trace/summary", func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		t, err := srv.GetTraceSummary(r.Context(), r.PathValue("id"))
		if err != nil {
			writeError(w, statusFor(err), err)
			return
		}
		w.Header().Set("Server-Timing", fmt.Sprintf("trace-summary;dur=%.1f", float64(time.Since(start).Microseconds())/1000))
		writeJSON(w, http.StatusOK, t)
	})
	mux.HandleFunc("GET /threads/{id}/trace/llm/{seq}", func(w http.ResponseWriter, r *http.Request) {
		seq, err := strconv.Atoi(r.PathValue("seq"))
		if err != nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("seq: %w", err))
			return
		}
		d, err := srv.GetLLMCall(r.Context(), r.PathValue("id"), seq)
		if err != nil {
			writeError(w, statusFor(err), err)
			return
		}
		writeJSON(w, http.StatusOK, d)
	})
	mux.HandleFunc("POST /threads/{id}/compact", func(w http.ResponseWriter, r *http.Request) {
		res, err := srv.Compact(r.Context(), r.PathValue("id"))
		if err != nil {
			writeError(w, statusFor(err), err)
			return
		}
		writeJSON(w, http.StatusOK, res)
	})
	if ws, ok := srv.(WorkspaceServer); ok {
		addWorkspaceRoutes(mux, ws)
	}
	if al, ok := srv.(auditLister); ok {
		mux.HandleFunc("GET /audit", func(w http.ResponseWriter, r *http.Request) {
			limit := 100
			if s := r.URL.Query().Get("limit"); s != "" {
				n, err := strconv.Atoi(s)
				if err != nil || n < 1 || n > 1000 {
					writeError(w, http.StatusBadRequest, errors.New("limit must be between 1 and 1000"))
					return
				}
				limit = n
			}
			list, err := al.AuditLog(r.Context(), limit)
			if err != nil {
				writeError(w, statusFor(err), err)
				return
			}
			if list == nil {
				list = []audit.Event{}
			}
			writeJSON(w, http.StatusOK, list)
		})
	}
	return mux
}

// threadAuthorizer refuses a signed-in user another user's thread on
// routes that do not go through a Server method taking the thread.
type threadAuthorizer interface {
	AuthorizeThread(ctx context.Context, threadID string) error
}

// auditLister backs GET /audit: the signed-in user's own events.
type auditLister interface {
	AuditLog(ctx context.Context, limit int) ([]audit.Event, error)
}

// addWorkspaceRoutes exposes the workspace API:
//
//	POST   /workspaces           workspace.Spec -> 201 workspace.Workspace (clones the repository)
//	GET    /workspaces           -> []workspace.Workspace of the user
//	GET    /workspaces/{id}      -> workspace.Workspace
//	POST   /workspaces/{id}/stop -> 204, stops the container and keeps the files
//	DELETE /workspaces/{id}      -> 204, removes container and files
func addWorkspaceRoutes(mux *http.ServeMux, srv WorkspaceServer) {
	mux.HandleFunc("POST /workspaces", func(w http.ResponseWriter, r *http.Request) {
		var spec workspace.Spec
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&spec); err != nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("decode workspace spec: %w", err))
			return
		}
		ws, err := srv.CreateWorkspace(r.Context(), spec)
		if err != nil {
			writeError(w, statusFor(err), err)
			return
		}
		writeJSON(w, http.StatusCreated, ws)
	})
	mux.HandleFunc("GET /workspaces", func(w http.ResponseWriter, r *http.Request) {
		list, err := srv.ListWorkspaces(r.Context())
		if err != nil {
			writeError(w, statusFor(err), err)
			return
		}
		if list == nil {
			list = []workspace.Workspace{}
		}
		writeJSON(w, http.StatusOK, list)
	})
	mux.HandleFunc("GET /workspaces/{id}", func(w http.ResponseWriter, r *http.Request) {
		ws, err := srv.GetWorkspace(r.Context(), r.PathValue("id"))
		if err != nil {
			writeError(w, statusFor(err), err)
			return
		}
		writeJSON(w, http.StatusOK, ws)
	})
	mux.HandleFunc("POST /workspaces/{id}/stop", func(w http.ResponseWriter, r *http.Request) {
		if err := srv.StopWorkspace(r.Context(), r.PathValue("id")); err != nil {
			writeError(w, statusFor(err), err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("DELETE /workspaces/{id}", func(w http.ResponseWriter, r *http.Request) {
		if err := srv.DeleteWorkspace(r.Context(), r.PathValue("id")); err != nil {
			writeError(w, statusFor(err), err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

func historyParams(r *http.Request) (before, limit int, err error) {
	limit = 20
	if raw := r.URL.Query().Get("before"); raw != "" {
		before, err = strconv.Atoi(raw)
		if err != nil || before < 0 {
			return 0, 0, fmt.Errorf("before must be a non-negative integer")
		}
	}
	if raw := r.URL.Query().Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 50 {
			return 0, 0, fmt.Errorf("limit must be between 1 and 50")
		}
	}
	return before, limit, nil
}

// runScope is where runs started over HTTP live: ServeHandler cancels ctx
// after the shutdown grace and waits for wg.
type runScope struct {
	ctx context.Context
	wg  sync.WaitGroup
}

type runScopeKey struct{}

func handleRun(w http.ResponseWriter, r *http.Request, srv Server, runs *liveRuns) {
	var req RunRequest
	dec := json.NewDecoder(io.LimitReader(r.Body, maxRequestBytes))
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("decode run request: %w", err))
		return
	}
	ctx, wg := context.WithoutCancel(r.Context()), (*sync.WaitGroup)(nil)
	if scope, ok := r.Context().Value(runScopeKey{}).(*runScope); ok {
		ctx, wg = scope.ctx, &scope.wg
	}
	run, err := runs.start(ctx, srv, req, wg)
	if err != nil {
		writeError(w, statusFor(err), err)
		return
	}
	if r.URL.Query().Get("detach") != "1" {
		defer run.cancel()
	}
	streamRun(w, r, run)
}

// streamRun writes the run's events as NDJSON, from its first event, ending
// in done or error. A client that disconnects or stops reading only stops
// its own stream.
func streamRun(w http.ResponseWriter, r *http.Request, run *liveRun) {
	rc := http.NewResponseController(w)
	enc := json.NewEncoder(w)
	started := false
	send := func(ev Event) bool {
		if !started {
			w.Header().Set("Content-Type", "application/x-ndjson")
			w.WriteHeader(http.StatusOK)
			started = true
		}
		// A client that stops reading would otherwise hold this handler
		// forever.
		_ = rc.SetWriteDeadline(time.Now().Add(eventWriteTimeout))
		if err := enc.Encode(ev); err != nil {
			return false
		}
		err := rc.Flush()
		return err == nil || errors.Is(err, http.ErrNotSupported)
	}
	finished, err := run.follow(r.Context(), send)
	switch {
	case !finished:
	case err != nil && !started:
		writeError(w, statusFor(err), err)
	case err != nil:
		send(Event{Kind: EventError, Error: err.Error()})
	default:
		send(Event{Kind: EventDone})
	}
}

func statusFor(err error) int {
	switch {
	case errors.Is(err, ErrBadRequest), errors.Is(err, workspace.ErrInvalid):
		return http.StatusBadRequest
	case errors.Is(err, ErrForbidden), errors.Is(err, workspace.ErrForbidden):
		return http.StatusForbidden
	case errors.Is(err, ErrThreadNotFound), errors.Is(err, trace.ErrNotFound), errors.Is(err, workspace.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, ErrNoWorkspaces):
		return http.StatusNotImplemented
	case errors.Is(err, ErrQuotaExceeded):
		return http.StatusTooManyRequests
	case errors.Is(err, ErrThreadBusy), errors.Is(err, ErrThreadCwdMismatch),
		errors.Is(err, ErrPendingApproval), errors.Is(err, ErrNoPendingApproval),
		errors.Is(err, ErrNothingToCompact), errors.Is(err, workspace.ErrBusy), errors.Is(err, workspace.ErrQuota):
		return http.StatusConflict
	default:
		return http.StatusInternalServerError
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeJSONMeasured exposes the serialized payload size for local GUI
// diagnostics. It is intentionally a response header rather than telemetry.
func writeJSONMeasured(w http.ResponseWriter, status int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Quill-Response-Bytes", strconv.Itoa(len(b)))
	w.WriteHeader(status)
	_, _ = w.Write(append(b, '\n'))
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

// Serve listens on addr, which must be a loopback address, until ctx is done.
// ready, if non-nil, receives the bound address once listening.
//
// When ctx is done the server stops accepting requests and lets in-flight
// runs finish for up to shutdownGrace; runs still going after that are
// cancelled, so their checkpoints are saved before Serve returns.
func Serve(ctx context.Context, addr string, srv Server, ready func(net.Addr)) error {
	return ServeHandler(ctx, addr, NewHandler(srv), ready)
}

// ServeHandler is Serve for any handler, e.g. the GUI wrapping NewHandler.
func ServeHandler(ctx context.Context, addr string, h http.Handler, ready func(net.Addr)) error {
	if err := requireLoopback(addr); err != nil {
		return err
	}
	return serve(ctx, addr, h, ready)
}

func serve(ctx context.Context, addr string, h http.Handler, ready func(net.Addr)) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	// Read before ready so a caller that changes shutdownGrace after ready
	// does not race with this run of the server.
	grace := shutdownGrace
	runsCtx, cancelRuns := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelRuns()
	scope := &runScope{ctx: runsCtx}
	baseCtx := context.WithValue(runsCtx, runScopeKey{}, scope)
	hs := &http.Server{
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return baseCtx },
	}
	if ready != nil {
		ready(ln.Addr())
	}
	errc := make(chan error, 1)
	go func() { errc <- hs.Serve(ln) }()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}

	drainCtx, cancelDrain := context.WithTimeout(context.Background(), grace)
	defer cancelDrain()
	if err := hs.Shutdown(drainCtx); err == nil && waitGroup(drainCtx, &scope.wg) {
		return nil
	}
	cancelRuns()
	finalCtx, cancelFinal := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelFinal()
	if err := hs.Shutdown(finalCtx); err != nil {
		return hs.Close()
	}
	waitGroup(finalCtx, &scope.wg)
	return nil
}

// waitGroup waits for wg until ctx is done and reports whether wg finished.
func waitGroup(ctx context.Context, wg *sync.WaitGroup) bool {
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
		return true
	case <-ctx.Done():
		return false
	}
}

func requireLoopback(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("serve: address %q: %w", addr, err)
	}
	if host == "localhost" {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return nil
	}
	return fmt.Errorf("serve: refusing to listen on %q; only loopback addresses are allowed because the agent writes files and runs commands", addr)
}
