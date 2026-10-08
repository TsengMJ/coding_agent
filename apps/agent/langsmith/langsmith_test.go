package langsmith_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tsengmingchun/quill/apps/agent"
	"github.com/tsengmingchun/quill/apps/agent/agent"
	"github.com/tsengmingchun/quill/apps/agent/langsmith"
	"github.com/tsengmingchun/quill/apps/agent/llm"
	"github.com/tsengmingchun/quill/apps/agent/runtime"
	"github.com/tsengmingchun/quill/apps/agent/tracing"
)

type sentRun struct {
	ID          string          `json:"id"`
	TraceID     string          `json:"trace_id"`
	DottedOrder string          `json:"dotted_order"`
	ParentRunID string          `json:"parent_run_id"`
	Name        string          `json:"name"`
	RunType     string          `json:"run_type"`
	SessionName string          `json:"session_name"`
	EndTime     string          `json:"end_time"`
	Inputs      map[string]any  `json:"inputs"`
	Outputs     map[string]any  `json:"outputs"`
	Extra       json.RawMessage `json:"extra"`
	Error       string          `json:"error"`
}

func (r sentRun) metadata(t *testing.T) map[string]any {
	t.Helper()
	var extra struct {
		Metadata map[string]any `json:"metadata"`
	}
	if err := json.Unmarshal(r.Extra, &extra); err != nil {
		t.Fatal(err)
	}
	return extra.Metadata
}

// fakeLangSmith records batches; status is the reply code.
type fakeLangSmith struct {
	mu       sync.Mutex
	requests int
	keys     []string
	runs     map[string]*sentRun
	order    []string
	status   int
}

func newFake(t *testing.T, status int) (*fakeLangSmith, *httptest.Server) {
	f := &fakeLangSmith{runs: map[string]*sentRun{}, status: status}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.requests++
		f.keys = append(f.keys, r.Header.Get("x-api-key"))
		if r.URL.Path != "/runs/batch" {
			t.Errorf("path %s", r.URL.Path)
		}
		var b struct {
			Post  []sentRun `json:"post"`
			Patch []sentRun `json:"patch"`
		}
		if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
			t.Errorf("decode: %v", err)
		}
		if f.status >= 300 {
			w.WriteHeader(f.status)
			return
		}
		for _, p := range b.Post {
			run := p
			f.runs[p.ID] = &run
			f.order = append(f.order, p.ID)
		}
		for _, p := range b.Patch {
			run, ok := f.runs[p.ID]
			if !ok {
				t.Errorf("patch for unknown run %s", p.ID)
				continue
			}
			run.EndTime, run.Outputs, run.Error = p.EndTime, p.Outputs, p.Error
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fakeLangSmith) byType(typ string) []*sentRun {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*sentRun
	for _, id := range f.order {
		if r := f.runs[id]; r.RunType == typ {
			out = append(out, r)
		}
	}
	return out
}

func newClient(t *testing.T, endpoint string) *langsmith.Client {
	t.Helper()
	c, err := langsmith.New(langsmith.Config{APIKey: "k", Endpoint: endpoint, Project: "p", FlushInterval: time.Hour,
		RetryDelay: time.Millisecond, Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestRunTreeWithSubagent(t *testing.T) {
	fake, srv := newFake(t, http.StatusAccepted)
	client := newClient(t, srv.URL)
	model := llm.NewFakeModel(
		llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "t1", Name: "task",
			Args: map[string]any{"subagent_type": "general-purpose", "description": "look around"}}}},
		llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("sub report")}, Usage: &llm.Usage{InputTokens: 10, OutputTokens: 2}},
		llm.NewAssistantMessage("all done"),
	)
	a, err := quill.CreateAgent(quill.WithModel(model), quill.WithTracer(client), quill.WithName("main"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Invoke(context.Background(), agent.Input{Messages: []llm.Message{llm.NewUserMessage("go")}},
		runtime.RunConfig{ThreadID: "th1"}); err != nil {
		t.Fatal(err)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}

	chains := fake.byType("chain")
	if len(chains) != 2 {
		t.Fatalf("chain runs = %d, want main and subagent", len(chains))
	}
	root, sub := chains[0], chains[1]
	if root.ParentRunID != "" || root.TraceID != root.ID || root.SessionName != "p" || root.EndTime == "" {
		t.Fatalf("root = %+v", root)
	}
	if md := root.metadata(t); md["ls_agent_type"] != "root" || md["thread_id"] != "th1" || md["lc_agent_name"] != "main" {
		t.Fatalf("root metadata = %v", md)
	}
	tools := fake.byType("tool")
	if len(tools) != 1 || tools[0].Name != "task" || tools[0].ParentRunID != root.ID {
		t.Fatalf("tool runs = %+v", tools)
	}
	if out, _ := tools[0].Outputs["output"].(string); out != "sub report" {
		t.Fatalf("task output = %v", tools[0].Outputs)
	}
	if sub.ParentRunID != tools[0].ID || sub.TraceID != root.ID || !strings.HasPrefix(sub.DottedOrder, tools[0].DottedOrder+".") {
		t.Fatalf("subagent run is not below the task run: %+v", sub)
	}
	if md := sub.metadata(t); md["ls_agent_type"] != "subagent" || md["lc_agent_name"] != "general-purpose" {
		t.Fatalf("subagent metadata = %v", md)
	}
	llms := fake.byType("llm")
	if len(llms) != 3 {
		t.Fatalf("llm runs = %d, want 3", len(llms))
	}
	if llms[1].ParentRunID != sub.ID || llms[0].ParentRunID != root.ID {
		t.Fatalf("llm parents = %s, %s", llms[0].ParentRunID, llms[1].ParentRunID)
	}
	usage, _ := llms[1].Outputs["usage_metadata"].(map[string]any)
	if usage["input_tokens"] != float64(10) || usage["total_tokens"] != float64(12) {
		t.Fatalf("usage = %v", llms[1].Outputs)
	}
	if msgs, _ := llms[0].Inputs["messages"].([]any); len(msgs) == 0 {
		t.Fatalf("llm inputs = %v", llms[0].Inputs)
	}
	if fake.keys[0] != "k" {
		t.Fatalf("api key header = %q", fake.keys[0])
	}
}

func TestDeliveryFailureDoesNotFailRun(t *testing.T) {
	fake, srv := newFake(t, http.StatusInternalServerError)
	client := newClient(t, srv.URL)
	a, err := agent.CreateAgent(agent.Config{Model: llm.NewFakeModel(llm.NewAssistantMessage("hi")), Tracer: client})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Invoke(context.Background(), agent.Input{Messages: []llm.Message{llm.NewUserMessage("go")}}, runtime.RunConfig{}); err != nil {
		t.Fatalf("run failed with a broken LangSmith: %v", err)
	}
	if err := client.Flush(context.Background()); err == nil {
		t.Fatal("Flush should report the delivery error")
	}
	if fake.requests != 3 {
		t.Fatalf("requests = %d, want 3 attempts", fake.requests)
	}
}

func TestEndAfterFlushBecomesPatch(t *testing.T) {
	fake, srv := newFake(t, http.StatusAccepted)
	client := newClient(t, srv.URL)
	_, run := client.Start(context.Background(), tracing.RunInfo{Name: "slow", Type: tracing.RunChain})
	if err := client.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	run.End(map[string]any{"output": "x"}, nil)
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	runs := fake.byType("chain")
	if len(runs) != 1 || runs[0].EndTime == "" || runs[0].Outputs["output"] != "x" || fake.requests != 2 {
		t.Fatalf("runs = %+v, requests = %d", runs, fake.requests)
	}
}

func TestFromEnvIsOffByDefault(t *testing.T) {
	for _, k := range append(langsmith.TracingEnvVars, langsmith.APIKeyEnvVars...) {
		t.Setenv(k, "")
	}
	c, err := langsmith.FromEnv()
	if err != nil || c != nil {
		t.Fatalf("FromEnv = %v, %v; want nil, nil", c, err)
	}
}

func TestFromEnvNeedsAPIKey(t *testing.T) {
	for _, k := range langsmith.APIKeyEnvVars {
		t.Setenv(k, "")
	}
	t.Setenv("LANGSMITH_TRACING", "true")
	if _, err := langsmith.FromEnv(); err == nil || !strings.Contains(err.Error(), "LANGSMITH_API_KEY") {
		t.Fatalf("err = %v", err)
	}
}

func TestFromEnvReadsLegacyNames(t *testing.T) {
	for _, k := range append(append(langsmith.TracingEnvVars, langsmith.APIKeyEnvVars...), langsmith.ProjectEnvVars...) {
		t.Setenv(k, "")
	}
	t.Setenv("LANGCHAIN_TRACING_V2", "true")
	t.Setenv("LANGCHAIN_API_KEY", "legacy")
	t.Setenv("LANGCHAIN_PROJECT", "proj")
	c, err := langsmith.FromEnv()
	if err != nil || c == nil {
		t.Fatalf("FromEnv = %v, %v", c, err)
	}
	defer c.Close()
	if c.Project() != "proj" {
		t.Fatalf("project = %q", c.Project())
	}
}

func TestNoTracerSendsNothing(t *testing.T) {
	fake, _ := newFake(t, http.StatusAccepted)
	a, err := quill.CreateAgent(quill.WithModel(llm.NewFakeModel(llm.NewAssistantMessage("hi"))), quill.WithTracer(nil))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Invoke(context.Background(), agent.Input{Messages: []llm.Message{llm.NewUserMessage("go")}}, runtime.RunConfig{}); err != nil {
		t.Fatal(err)
	}
	if fake.requests != 0 {
		t.Fatalf("requests = %d", fake.requests)
	}
}
