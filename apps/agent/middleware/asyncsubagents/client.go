package asyncsubagents

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// APIKeyEnvVars are read, in order, for the x-api-key header when the
// agent's Headers do not set one (as the LangGraph SDK does).
var APIKeyEnvVars = []string{"LANGGRAPH_API_KEY", "LANGSMITH_API_KEY", "LANGCHAIN_API_KEY"}

// Run is an agent-server run.
type Run struct {
	RunID    string `json:"run_id"`
	ThreadID string `json:"thread_id"`
	Status   string `json:"status"`
	Error    any    `json:"error,omitempty"`
}

// Client calls the LangGraph agent-server API (threads and runs).
type Client struct {
	base    string
	headers map[string]string
	http    *http.Client
}

// NewClient returns a client for the server at baseURL; nil hc uses a
// client with a 30s timeout.
func NewClient(baseURL string, headers map[string]string, hc *http.Client) *Client {
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{base: strings.TrimRight(baseURL, "/"), headers: headers, http: hc}
}

// CreateThread starts an empty thread and returns its ID.
func (c *Client) CreateThread(ctx context.Context) (string, error) {
	var out struct {
		ThreadID string `json:"thread_id"`
	}
	if err := c.do(ctx, http.MethodPost, "/threads", map[string]any{}, &out); err != nil {
		return "", err
	}
	if out.ThreadID == "" {
		return "", fmt.Errorf("agent server returned no thread_id")
	}
	return out.ThreadID, nil
}

// CreateRun starts graphID on the thread with input; multitask is the
// multitask_strategy ("" leaves the server default).
func (c *Client) CreateRun(ctx context.Context, threadID, graphID string, input map[string]any, multitask string) (Run, error) {
	body := map[string]any{"assistant_id": graphID, "input": input}
	if multitask != "" {
		body["multitask_strategy"] = multitask
	}
	var run Run
	if err := c.do(ctx, http.MethodPost, "/threads/"+url.PathEscape(threadID)+"/runs", body, &run); err != nil {
		return Run{}, err
	}
	if run.RunID == "" {
		return Run{}, fmt.Errorf("agent server returned no run_id")
	}
	return run, nil
}

// GetRun reads a run's status.
func (c *Client) GetRun(ctx context.Context, threadID, runID string) (Run, error) {
	var run Run
	err := c.do(ctx, http.MethodGet, "/threads/"+url.PathEscape(threadID)+"/runs/"+url.PathEscape(runID), nil, &run)
	return run, err
}

// CancelRun interrupts a run without waiting for it to stop.
func (c *Client) CancelRun(ctx context.Context, threadID, runID string) error {
	return c.do(ctx, http.MethodPost, "/threads/"+url.PathEscape(threadID)+"/runs/"+url.PathEscape(runID)+"/cancel?wait=0&action=interrupt", nil, nil)
}

// ThreadValues returns the thread's current state values.
func (c *Client) ThreadValues(ctx context.Context, threadID string) (map[string]any, error) {
	var out struct {
		Values map[string]any `json:"values"`
	}
	err := c.do(ctx, http.MethodGet, "/threads/"+url.PathEscape(threadID), nil, &out)
	return out.Values, err
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("%s %s: HTTP %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(raw[:min(len(raw), 500)])))
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("%s %s: decode response: %w", method, path, err)
	}
	return nil
}

// resolveHeaders adds x-auth-scheme: langsmith and an x-api-key from the
// environment unless the agent sets them.
func resolveHeaders(a AsyncSubAgent, getenv func(string) string) map[string]string {
	h := map[string]string{}
	for k, v := range a.Headers {
		h[strings.ToLower(k)] = v
	}
	if _, ok := h["x-auth-scheme"]; !ok {
		h["x-auth-scheme"] = "langsmith"
	}
	if _, ok := h["x-api-key"]; !ok {
		for _, name := range APIKeyEnvVars {
			if v := strings.TrimSpace(getenv(name)); v != "" {
				h["x-api-key"] = v
				break
			}
		}
	}
	return h
}

// clientCache shares one Client per (url, headers).
type clientCache struct {
	mu      sync.Mutex
	clients map[string]*Client
	http    *http.Client
	getenv  func(string) string
}

func (c *clientCache) get(a AsyncSubAgent) *Client {
	headers := resolveHeaders(a, c.getenv)
	keys := make([]string, 0, len(headers))
	for k, v := range headers {
		keys = append(keys, k+"="+v)
	}
	sort.Strings(keys)
	key := a.URL + "\x00" + strings.Join(keys, "\x00")
	c.mu.Lock()
	defer c.mu.Unlock()
	if cl, ok := c.clients[key]; ok {
		return cl
	}
	cl := NewClient(a.URL, headers, c.http)
	c.clients[key] = cl
	return cl
}

func osGetenv(name string) string { return os.Getenv(name) }
