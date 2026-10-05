package agent

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/tsengmingchun/quill/llm"
	"github.com/tsengmingchun/quill/runtime"
	"github.com/tsengmingchun/quill/tool"
	"github.com/tsengmingchun/quill/tracing"
)

type recordedRun struct {
	info    tracing.RunInfo
	parent  *recordedRun
	outputs map[string]any
	err     error
	ended   bool
}

type recordingTracer struct {
	mu   sync.Mutex
	runs []*recordedRun
}

type recParentKey struct{}

func (t *recordingTracer) Start(ctx context.Context, info tracing.RunInfo) (context.Context, tracing.Run) {
	r := &recordedRun{info: info}
	r.parent, _ = ctx.Value(recParentKey{}).(*recordedRun)
	t.mu.Lock()
	t.runs = append(t.runs, r)
	t.mu.Unlock()
	return context.WithValue(ctx, recParentKey{}, r), endFunc(func(out map[string]any, err error) {
		t.mu.Lock()
		defer t.mu.Unlock()
		r.outputs, r.err, r.ended = out, err, true
	})
}

type endFunc func(map[string]any, error)

func (f endFunc) End(out map[string]any, err error) { f(out, err) }

func TestTracerRecordsChainModelAndToolRuns(t *testing.T) {
	tr := &recordingTracer{}
	failing := &tool.Func{ToolName: "boom", ToolSchema: tool.Schema{"type": "object"},
		Fn: func(context.Context, *tool.Runtime, map[string]any) (*tool.Result, error) {
			return nil, errors.New("kaput")
		}}
	model := llm.NewFakeModel(
		llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "c1", Name: "boom", Args: map[string]any{"x": 1}}}},
		llm.NewAssistantMessage("done"),
	)
	a, err := CreateAgent(Config{Model: model, Tools: []tool.Tool{failing}, Tracer: tr, Name: "main"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.Invoke(context.Background(), Input{Messages: []llm.Message{llm.NewUserMessage("go")}},
		runtime.RunConfig{ThreadID: "th", Tags: []string{"quill:resume"}, Metadata: map[string]any{"k": "v"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(tr.runs) != 4 {
		t.Fatalf("runs = %d, want chain + 2 llm + tool", len(tr.runs))
	}
	root := tr.runs[0]
	if root.info.Type != tracing.RunChain || root.parent != nil || !root.ended || root.info.Tags[0] != "quill:resume" {
		t.Fatalf("root = %+v", root)
	}
	md := root.info.Metadata
	if md[tracing.AgentTypeKey] != "root" || md[tracing.ThreadIDKey] != "th" || md["k"] != "v" || md[tracing.AgentNameKey] != "main" {
		t.Fatalf("root metadata = %v", md)
	}
	toolRun := tr.runs[2]
	if toolRun.info.Type != tracing.RunTool || toolRun.parent != root || toolRun.err == nil || toolRun.info.Inputs["x"] != 1 {
		t.Fatalf("tool run = %+v", toolRun)
	}
	for _, i := range []int{1, 3} {
		if r := tr.runs[i]; r.info.Type != tracing.RunLLM || r.parent != root || !r.ended {
			t.Fatalf("llm run %d = %+v", i, r)
		}
	}
	if msgs, _ := root.outputs[TraceMessagesKey].([]llm.Message); len(msgs) != 4 {
		t.Fatalf("root outputs = %v", root.outputs)
	}
}

func TestNestedAgentUsesTracerFromContext(t *testing.T) {
	tr := &recordingTracer{}
	inner, err := CreateAgent(Config{Model: llm.NewFakeModel(llm.NewAssistantMessage("inner")), Name: "inner"})
	if err != nil {
		t.Fatal(err)
	}
	call := &tool.Func{ToolName: "delegate", ToolSchema: tool.Schema{"type": "object"},
		Fn: func(ctx context.Context, rt *tool.Runtime, _ map[string]any) (*tool.Result, error) {
			cfg := runtime.RunConfig{Configurable: map[string]any{tracing.AgentTypeKey: "subagent"}}
			_, err := inner.Invoke(ctx, Input{Messages: []llm.Message{llm.NewUserMessage("x")}}, cfg)
			return &tool.Result{}, err
		}}
	outer, err := CreateAgent(Config{Tracer: tr, Tools: []tool.Tool{call}, Model: llm.NewFakeModel(
		llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "d", Name: "delegate"}}},
		llm.NewAssistantMessage("done"),
	)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := outer.Invoke(context.Background(), Input{Messages: []llm.Message{llm.NewUserMessage("go")}}, runtime.RunConfig{}); err != nil {
		t.Fatal(err)
	}
	var sub *recordedRun
	for _, r := range tr.runs {
		if r.info.Type == tracing.RunChain && r.parent != nil {
			sub = r
		}
	}
	if sub == nil || sub.parent.info.Type != tracing.RunTool || sub.info.Metadata[tracing.AgentTypeKey] != "subagent" || sub.info.Name != "inner" {
		t.Fatalf("nested run = %+v", sub)
	}
}

func TestNoTracerRecordsNothing(t *testing.T) {
	a, err := CreateAgent(Config{Model: llm.NewFakeModel(llm.NewAssistantMessage("hi"))})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := a.Invoke(ctx, Input{Messages: []llm.Message{llm.NewUserMessage("go")}}, runtime.RunConfig{}); err != nil {
		t.Fatal(err)
	}
	if tracing.FromContext(ctx) != nil {
		t.Fatal("tracer leaked into the caller's context")
	}
}
