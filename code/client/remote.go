package client

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/tsengmingchun/deepagents_golang/code/config"
	"github.com/tsengmingchun/deepagents_golang/code/server"
	"github.com/tsengmingchun/deepagents_golang/code/sessions"
)

// ServerEnvVar names the `dcode serve` URL clients connect to.
const ServerEnvVar = config.ServerEnvVar

// maxEventBytes caps one NDJSON line (a message with large tool output).
const maxEventBytes = 64 << 20

// Remote talks to `dcode serve` over HTTP.
type Remote struct {
	base *url.URL
	hc   *http.Client
}

var _ server.Server = (*Remote)(nil)

// NewRemote returns a client for the server at baseURL (e.g. http://127.0.0.1:8123).
func NewRemote(baseURL string) (*Remote, error) {
	u, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil {
		return nil, fmt.Errorf("server URL %q: %w", baseURL, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" || u.Host == "" {
		return nil, fmt.Errorf("server URL %q: want http://host:port", baseURL)
	}
	return &Remote{base: u, hc: &http.Client{}}, nil
}

// Ping checks that the server is reachable.
func (r *Remote) Ping(ctx context.Context) error {
	resp, err := r.do(ctx, http.MethodGet, "/healthz", nil, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return checkStatus(resp)
}

func (r *Remote) Run(ctx context.Context, req server.RunRequest) iter.Seq2[server.Event, error] {
	return func(yield func(server.Event, error) bool) {
		body, err := json.Marshal(req)
		if err != nil {
			yield(server.Event{}, err)
			return
		}
		resp, err := r.do(ctx, http.MethodPost, "/runs", nil, body)
		if err != nil {
			yield(server.Event{}, err)
			return
		}
		defer resp.Body.Close()
		if err := checkStatus(resp); err != nil {
			yield(server.Event{}, err)
			return
		}
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 64<<10), maxEventBytes)
		for sc.Scan() {
			var ev server.Event
			if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
				yield(server.Event{}, fmt.Errorf("decode server event: %w", err))
				return
			}
			switch ev.Kind {
			case server.EventDone:
				return
			case server.EventError:
				yield(server.Event{}, errors.New(ev.Error))
				return
			}
			if !yield(ev, nil) {
				return
			}
		}
		if err := ctx.Err(); err != nil {
			yield(server.Event{}, err)
			return
		}
		if err := sc.Err(); err != nil {
			yield(server.Event{}, fmt.Errorf("read server stream: %w", err))
			return
		}
		yield(server.Event{}, errors.New("server closed the stream before the run finished"))
	}
}

func (r *Remote) GetThread(ctx context.Context, threadID string) (*server.ThreadState, error) {
	resp, err := r.do(ctx, http.MethodGet, "/threads/"+url.PathEscape(threadID), nil, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if err := checkStatus(resp); err != nil {
		return nil, err
	}
	var ts server.ThreadState
	if err := json.NewDecoder(resp.Body).Decode(&ts); err != nil {
		return nil, fmt.Errorf("decode thread: %w", err)
	}
	return &ts, nil
}

func (r *Remote) ListThreads(ctx context.Context, f sessions.Filter) ([]sessions.Info, error) {
	q := url.Values{}
	if f.Cwd != "" {
		q.Set("cwd", f.Cwd)
	}
	if f.Limit > 0 {
		q.Set("limit", strconv.Itoa(f.Limit))
	}
	resp, err := r.do(ctx, http.MethodGet, "/threads", q, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := checkStatus(resp); err != nil {
		return nil, err
	}
	var list []sessions.Info
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return nil, fmt.Errorf("decode threads: %w", err)
	}
	return list, nil
}

func (r *Remote) Compact(ctx context.Context, threadID string) (*server.CompactResult, error) {
	resp, err := r.do(ctx, http.MethodPost, "/threads/"+url.PathEscape(threadID)+"/compact", nil, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := checkStatus(resp); err != nil {
		return nil, err
	}
	var res server.CompactResult
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return nil, fmt.Errorf("decode compact result: %w", err)
	}
	return &res, nil
}

func (r *Remote) Close() error { return nil }

func (r *Remote) do(ctx context.Context, method, path string, q url.Values, body []byte) (*http.Response, error) {
	u := *r.base
	u.Path += path
	u.RawQuery = q.Encode()
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), rd)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := r.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("dcode server %s: %w", r.base, err)
	}
	return resp, nil
}

func checkStatus(resp *http.Response) error {
	if resp.StatusCode < 300 {
		return nil
	}
	var body struct {
		Error string `json:"error"`
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if json.Unmarshal(raw, &body) == nil && body.Error != "" {
		return fmt.Errorf("server: %s (HTTP %d)", body.Error, resp.StatusCode)
	}
	return fmt.Errorf("server: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
}
