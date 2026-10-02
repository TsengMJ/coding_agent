// Package langsmith sends agent run trees to LangSmith. It implements
// tracing.Tracer: runs are queued in memory and posted in batches to
// `POST {endpoint}/runs/batch` by a background goroutine, so tracing never
// blocks or fails the agent. Delivery errors are logged.
//
// Tracing is off unless enabled: FromEnv returns nil unless LANGSMITH_TRACING
// (or the legacy LANGCHAIN_TRACING_V2) is "true".
package langsmith

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/tsengmingchun/deepagents_golang/tracing"
)

// DefaultEndpoint is the LangSmith API used when none is configured.
const DefaultEndpoint = "https://api.smith.langchain.com"

// DefaultProject receives runs when no project is configured.
const DefaultProject = "default"

// Environment variables, each with its legacy LANGCHAIN_ alias.
var (
	TracingEnvVars  = []string{"LANGSMITH_TRACING", "LANGCHAIN_TRACING_V2"}
	APIKeyEnvVars   = []string{"LANGSMITH_API_KEY", "LANGCHAIN_API_KEY"}
	EndpointEnvVars = []string{"LANGSMITH_ENDPOINT", "LANGCHAIN_ENDPOINT"}
	ProjectEnvVars  = []string{"LANGSMITH_PROJECT", "LANGCHAIN_PROJECT"}
)

// Config configures a Client.
type Config struct {
	APIKey   string
	Endpoint string
	Project  string
	// HTTPClient defaults to a client with a 30s timeout.
	HTTPClient *http.Client
	// FlushInterval is how often queued runs are sent; default 1s.
	FlushInterval time.Duration
	// RetryDelay is the first backoff after a failed batch (doubled once);
	// default 500ms.
	RetryDelay time.Duration
	// Logf reports delivery errors; default log.Printf.
	Logf func(format string, args ...any)
}

// Client is a tracing.Tracer that delivers runs to LangSmith.
type Client struct {
	cfg  Config
	mu   sync.Mutex
	post []*runPayload
	// open indexes queued posts by run ID, so an End that arrives before
	// the post is sent is merged into it instead of becoming a patch.
	open  map[string]*runPayload
	patch []*runPayload
	wake  chan struct{}
	stop  chan struct{}
	done  chan struct{}
	send  sync.Mutex
	close sync.Once
}

// batchSize triggers an early flush.
const batchSize = 100

// New starts a client; Close it to deliver the remaining runs.
func New(cfg Config) (*Client, error) {
	if strings.TrimSpace(cfg.APIKey) == "" {
		return nil, errors.New("langsmith: tracing is on but no API key is set; set LANGSMITH_API_KEY or turn tracing off")
	}
	if cfg.Endpoint == "" {
		cfg.Endpoint = DefaultEndpoint
	}
	cfg.Endpoint = strings.TrimRight(cfg.Endpoint, "/")
	if cfg.Project == "" {
		cfg.Project = DefaultProject
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 30 * time.Second}
	}
	if cfg.FlushInterval <= 0 {
		cfg.FlushInterval = time.Second
	}
	if cfg.RetryDelay <= 0 {
		cfg.RetryDelay = 500 * time.Millisecond
	}
	if cfg.Logf == nil {
		cfg.Logf = log.Printf
	}
	c := &Client{cfg: cfg, open: map[string]*runPayload{}, wake: make(chan struct{}, 1), stop: make(chan struct{}), done: make(chan struct{})}
	go c.loop()
	return c, nil
}

// TracingEnabled reports whether the environment turns tracing on.
func TracingEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(firstEnv(TracingEnvVars)))
	return v == "true" || v == "1"
}

// ConfigFromEnv reads the API key, endpoint and project from the environment.
func ConfigFromEnv() Config {
	return Config{APIKey: firstEnv(APIKeyEnvVars), Endpoint: firstEnv(EndpointEnvVars), Project: firstEnv(ProjectEnvVars)}
}

// FromEnv returns a client when the environment turns tracing on, nil when
// it is off, and an error when it is on without an API key.
func FromEnv() (*Client, error) {
	if !TracingEnabled() {
		return nil, nil
	}
	return New(ConfigFromEnv())
}

var (
	defaultOnce   sync.Once
	defaultClient *Client
	defaultErr    error
)

// Default is FromEnv evaluated once per process and shared, so agents built
// without an explicit tracer do not each start a client. Callers that use
// it should Flush it before exiting.
func Default() (*Client, error) {
	defaultOnce.Do(func() { defaultClient, defaultErr = FromEnv() })
	return defaultClient, defaultErr
}

func firstEnv(names []string) string {
	for _, n := range names {
		if v := os.Getenv(n); v != "" {
			return v
		}
	}
	return ""
}

// Project is the project runs are sent to.
func (c *Client) Project() string { return c.cfg.Project }

type parentKey struct{}

type parentRun struct {
	id, traceID, dotted string
}

// runPayload is one run as LangSmith's batch API expects it.
type runPayload struct {
	ID          string          `json:"id"`
	TraceID     string          `json:"trace_id"`
	DottedOrder string          `json:"dotted_order"`
	ParentRunID string          `json:"parent_run_id,omitempty"`
	Name        string          `json:"name,omitempty"`
	RunType     string          `json:"run_type,omitempty"`
	SessionName string          `json:"session_name,omitempty"`
	StartTime   string          `json:"start_time,omitempty"`
	EndTime     string          `json:"end_time,omitempty"`
	Inputs      json.RawMessage `json:"inputs,omitempty"`
	Outputs     json.RawMessage `json:"outputs,omitempty"`
	Extra       json.RawMessage `json:"extra,omitempty"`
	Tags        []string        `json:"tags,omitempty"`
	Error       string          `json:"error,omitempty"`
}

// Start implements tracing.Tracer.
func (c *Client) Start(ctx context.Context, info tracing.RunInfo) (context.Context, tracing.Run) {
	start := info.Start
	if start.IsZero() {
		start = time.Now()
	}
	start = start.UTC()
	id := newRunID()
	self := parentRun{id: id, traceID: id, dotted: dottedSegment(start, id)}
	p := &runPayload{
		ID: id, Name: info.Name, RunType: string(info.Type), SessionName: c.cfg.Project,
		StartTime: start.Format(time.RFC3339Nano), Tags: info.Tags,
		Inputs: encode(c, encodeInputs(info.Type, info.Inputs)),
		Extra:  encode(c, map[string]any{"metadata": info.Metadata, "runtime": map[string]any{"sdk": "deepagents-go", "language": "go"}}),
	}
	if parent, ok := ctx.Value(parentKey{}).(parentRun); ok {
		self.traceID = parent.traceID
		self.dotted = parent.dotted + "." + self.dotted
		p.ParentRunID = parent.id
	}
	p.TraceID, p.DottedOrder = self.traceID, self.dotted
	c.mu.Lock()
	c.post = append(c.post, p)
	c.open[id] = p
	c.mu.Unlock()
	c.nudge()
	return context.WithValue(ctx, parentKey{}, self), &run{c: c, p: p, typ: info.Type}
}

type run struct {
	c    *Client
	p    *runPayload
	typ  tracing.RunType
	once sync.Once
}

func (r *run) End(outputs map[string]any, err error) {
	r.once.Do(func() { r.c.end(r.p, r.typ, outputs, err) })
}

func (c *Client) end(p *runPayload, typ tracing.RunType, outputs map[string]any, err error) {
	end := time.Now().UTC().Format(time.RFC3339Nano)
	out := encode(c, encodeOutputs(typ, outputs))
	var msg string
	if err != nil {
		msg = err.Error()
	}
	c.mu.Lock()
	if queued, ok := c.open[p.ID]; ok {
		queued.EndTime, queued.Outputs, queued.Error = end, out, msg
		delete(c.open, p.ID)
	} else {
		c.patch = append(c.patch, &runPayload{
			ID: p.ID, TraceID: p.TraceID, DottedOrder: p.DottedOrder, ParentRunID: p.ParentRunID,
			EndTime: end, Outputs: out, Error: msg,
		})
	}
	c.mu.Unlock()
	c.nudge()
}

func (c *Client) nudge() {
	c.mu.Lock()
	n := len(c.post) + len(c.patch)
	c.mu.Unlock()
	if n < batchSize {
		return
	}
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

func (c *Client) loop() {
	defer close(c.done)
	t := time.NewTicker(c.cfg.FlushInterval)
	defer t.Stop()
	for {
		select {
		case <-c.stop:
			return
		case <-t.C:
		case <-c.wake:
		}
		_ = c.Flush(context.Background())
	}
}

// Flush sends every queued run and returns the first delivery error.
func (c *Client) Flush(ctx context.Context) error {
	c.send.Lock()
	defer c.send.Unlock()
	c.mu.Lock()
	post, patch := c.post, c.patch
	c.post, c.patch = nil, nil
	// Runs still open are posted now; their End becomes a patch.
	for _, p := range post {
		delete(c.open, p.ID)
	}
	c.mu.Unlock()
	if len(post) == 0 && len(patch) == 0 {
		return nil
	}
	var first error
	for _, b := range batches(post, patch) {
		if err := c.sendBatch(ctx, b); err != nil {
			c.cfg.Logf("langsmith: dropped %d run updates: %v", len(b.Post)+len(b.Patch), err)
			if first == nil {
				first = err
			}
		}
	}
	return first
}

// Close stops the background flush and sends what is left.
func (c *Client) Close() error {
	var err error
	c.close.Do(func() {
		close(c.stop)
		<-c.done
		err = c.Flush(context.Background())
	})
	return err
}

type batch struct {
	Post  []*runPayload `json:"post,omitempty"`
	Patch []*runPayload `json:"patch,omitempty"`
}

// maxBatchBytes keeps a request well under LangSmith's payload limit.
const maxBatchBytes = 8 << 20

// batches splits runs so each request stays under maxBatchBytes; posts go
// before patches so a run is created before it is updated.
func batches(post, patch []*runPayload) []batch {
	var out []batch
	cur, size := batch{}, 0
	add := func(p *runPayload, isPost bool) {
		n := len(p.Inputs) + len(p.Outputs) + len(p.Extra) + 512
		if size > 0 && size+n > maxBatchBytes {
			out = append(out, cur)
			cur, size = batch{}, 0
		}
		if isPost {
			cur.Post = append(cur.Post, p)
		} else {
			cur.Patch = append(cur.Patch, p)
		}
		size += n
	}
	for _, p := range post {
		add(p, true)
	}
	for _, p := range patch {
		add(p, false)
	}
	if size > 0 {
		out = append(out, cur)
	}
	return out
}

func (c *Client) sendBatch(ctx context.Context, b batch) error {
	body, err := json.Marshal(b)
	if err != nil {
		return fmt.Errorf("encode batch: %w", err)
	}
	delay := c.cfg.RetryDelay
	var last error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delay):
			}
			delay *= 2
		}
		retry, err := c.postOnce(ctx, body)
		if err == nil {
			return nil
		}
		last = err
		if !retry {
			break
		}
	}
	return last
}

// postOnce sends one request; retry reports whether the failure is worth retrying.
func (c *Client) postOnce(ctx context.Context, body []byte) (retry bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.Endpoint+"/runs/batch", bytes.NewReader(body))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", c.cfg.APIKey)
	resp, err := c.cfg.HTTPClient.Do(req)
	if err != nil {
		return true, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 300 {
		_, _ = io.Copy(io.Discard, resp.Body)
		return false, nil
	}
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	err = fmt.Errorf("POST /runs/batch: %s: %s", resp.Status, strings.TrimSpace(string(msg)))
	return resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500, err
}

func newRunID() string {
	id, err := uuid.NewV7()
	if err != nil {
		return uuid.NewString()
	}
	return id.String()
}

// dottedSegment is one "<start><run id>" part of a dotted order, with the
// start in LangSmith's %Y%m%dT%H%M%S%fZ form.
func dottedSegment(t time.Time, id string) string {
	return fmt.Sprintf("%s%06dZ%s", t.Format("20060102T150405"), t.Nanosecond()/1000, id)
}

// encode marshals v, or records why it could not.
func encode(c *Client, v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		c.cfg.Logf("langsmith: cannot encode run data: %v", err)
		b, _ = json.Marshal(map[string]string{"error": "unserializable: " + err.Error()})
	}
	return b
}
