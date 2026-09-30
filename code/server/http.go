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
	"time"

	"github.com/tsengmingchun/deepagents_golang/code/commands"
	"github.com/tsengmingchun/deepagents_golang/code/sessions"
	"github.com/tsengmingchun/deepagents_golang/code/trace"
)

// DefaultAddr is where `dcode serve` listens by default.
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
//	POST /runs            RunRequest -> NDJSON stream of Event, ending in done or error
//	GET  /threads         ?cwd=&limit= -> []sessions.Info
//	GET  /threads/{id}    -> ThreadState (404 when unknown)
//	GET  /threads/{id}/trace -> trace.Trace (model call summaries, diffs, subagent messages)
//	GET  /threads/{id}/trace/llm/{seq} -> trace.LLMCallDetail (404 when unknown)
//	POST /threads/{id}/compact -> CompactResult (404 unknown, 409 busy/pending/too short)
//	GET  /commands        -> []commands.Command (dcode slash commands, hidden ones omitted)
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
		writeJSON(w, http.StatusOK, commands.Visible(commands.DCode))
	})
	mux.HandleFunc("POST /runs", func(w http.ResponseWriter, r *http.Request) {
		handleRun(w, r, srv)
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
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if ts == nil {
			writeError(w, http.StatusNotFound, errors.New("thread not found"))
			return
		}
		writeJSON(w, http.StatusOK, ts)
	})
	mux.HandleFunc("GET /threads/{id}/trace", func(w http.ResponseWriter, r *http.Request) {
		t, err := srv.GetTrace(r.Context(), r.PathValue("id"))
		if err != nil {
			writeError(w, statusFor(err), err)
			return
		}
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
	return mux
}

func handleRun(w http.ResponseWriter, r *http.Request, srv Server) {
	var req RunRequest
	dec := json.NewDecoder(io.LimitReader(r.Body, maxRequestBytes))
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("decode run request: %w", err))
		return
	}
	rc := http.NewResponseController(w)
	enc := json.NewEncoder(w)
	started := false
	send := func(ev Event) bool {
		if !started {
			w.Header().Set("Content-Type", "application/x-ndjson")
			w.WriteHeader(http.StatusOK)
			started = true
		}
		// A client that stops reading would otherwise block the run, and
		// keep its thread busy, forever.
		_ = rc.SetWriteDeadline(time.Now().Add(eventWriteTimeout))
		if err := enc.Encode(ev); err != nil {
			return false
		}
		err := rc.Flush()
		return err == nil || errors.Is(err, http.ErrNotSupported)
	}
	for ev, err := range srv.Run(r.Context(), req) {
		if err != nil {
			if !started {
				writeError(w, statusFor(err), err)
				return
			}
			send(Event{Kind: EventError, Error: err.Error()})
			return
		}
		if !send(ev) {
			return
		}
	}
	send(Event{Kind: EventDone})
}

func statusFor(err error) int {
	switch {
	case errors.Is(err, ErrBadRequest):
		return http.StatusBadRequest
	case errors.Is(err, ErrThreadNotFound), errors.Is(err, trace.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, ErrThreadBusy), errors.Is(err, ErrThreadCwdMismatch),
		errors.Is(err, ErrPendingApproval), errors.Is(err, ErrNoPendingApproval),
		errors.Is(err, ErrNothingToCompact):
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
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	runsCtx, cancelRuns := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelRuns()
	hs := &http.Server{
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return runsCtx },
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

	drainCtx, cancelDrain := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancelDrain()
	if err := hs.Shutdown(drainCtx); err == nil {
		return nil
	}
	cancelRuns()
	finalCtx, cancelFinal := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelFinal()
	if err := hs.Shutdown(finalCtx); err != nil {
		return hs.Close()
	}
	return nil
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
