package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/tsengmingchun/deepagents_golang/llm"
	"github.com/tsengmingchun/deepagents_golang/runtime"
	"github.com/tsengmingchun/deepagents_golang/tool"
)

func toolCallReply(calls ...llm.ToolCall) llm.Message {
	return llm.Message{Role: llm.RoleAssistant, ToolCalls: calls}
}

func echoTool() tool.Tool {
	return &tool.Func{
		ToolName:        "echo",
		ToolDescription: "echo back the text argument",
		ToolSchema:      tool.Schema{"type": "object"},
		Fn: func(_ context.Context, _ *tool.Runtime, args map[string]any) (*tool.Result, error) {
			text, _ := args["text"].(string)
			return &tool.Result{Content: []llm.ContentBlock{llm.TextBlock("echo:" + text)}}, nil
		},
	}
}

func invoke(t *testing.T, cfg Config, prompt string) (*runtime.State, error) {
	t.Helper()
	a, err := CreateAgent(cfg)
	if err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}
	return a.Invoke(context.Background(), Input{Messages: []llm.Message{llm.NewUserMessage(prompt)}}, runtime.RunConfig{})
}

func roles(msgs []llm.Message) string {
	parts := make([]string, len(msgs))
	for i, m := range msgs {
		parts[i] = string(m.Role)
	}
	return strings.Join(parts, ",")
}

func TestLoopEndsWithoutToolCalls(t *testing.T) {
	state, err := invoke(t, Config{Model: llm.NewFakeModel(llm.NewAssistantMessage("hi"))}, "hello")
	if err != nil {
		t.Fatal(err)
	}
	if got := roles(state.Messages); got != "user,assistant" {
		t.Fatalf("roles = %s", got)
	}
}

func TestLoopRunsTwoToolRounds(t *testing.T) {
	model := llm.NewFakeModel(
		toolCallReply(llm.ToolCall{ID: "c1", Name: "echo", Args: map[string]any{"text": "a"}}),
		toolCallReply(llm.ToolCall{ID: "c2", Name: "echo", Args: map[string]any{"text": "b"}}),
		llm.NewAssistantMessage("done"),
	)
	sys := llm.NewSystemMessage("be brief")
	state, err := invoke(t, Config{Model: model, SystemMessage: &sys, Tools: []tool.Tool{echoTool()}}, "go")
	if err != nil {
		t.Fatal(err)
	}
	if got := roles(state.Messages); got != "user,assistant,tool,assistant,tool,assistant" {
		t.Fatalf("roles = %s", got)
	}
	if got := state.Messages[4].Text(); got != "echo:b" {
		t.Fatalf("second tool result = %q", got)
	}
	reqs := model.Requests()
	if len(reqs) != 3 || reqs[0].Messages[0].Role != llm.RoleSystem || len(reqs[0].Tools) != 1 {
		t.Fatalf("unexpected first request: %+v", reqs[0])
	}
}

func TestLoopUnknownToolReturnsErrorMessage(t *testing.T) {
	model := llm.NewFakeModel(
		toolCallReply(llm.ToolCall{ID: "c1", Name: "missing"}),
		llm.NewAssistantMessage("sorry"),
	)
	state, err := invoke(t, Config{Model: model, Tools: []tool.Tool{echoTool()}}, "go")
	if err != nil {
		t.Fatal(err)
	}
	toolMsg := state.Messages[2]
	if toolMsg.Status != llm.ToolStatusError || !strings.Contains(toolMsg.Text(), "not a valid tool") {
		t.Fatalf("unexpected tool message: %+v", toolMsg)
	}
}

func TestLoopToolErrorBecomesErrorMessage(t *testing.T) {
	failing := &tool.Func{
		ToolName: "fail",
		Fn: func(context.Context, *tool.Runtime, map[string]any) (*tool.Result, error) {
			return nil, errors.New("boom")
		},
	}
	model := llm.NewFakeModel(toolCallReply(llm.ToolCall{ID: "c1", Name: "fail"}), llm.NewAssistantMessage("ok"))
	state, err := invoke(t, Config{Model: model, Tools: []tool.Tool{failing}}, "go")
	if err != nil {
		t.Fatal(err)
	}
	if state.Messages[2].Status != llm.ToolStatusError || !strings.Contains(state.Messages[2].Text(), "boom") {
		t.Fatalf("unexpected tool message: %+v", state.Messages[2])
	}
}

func TestLoopRecursionLimit(t *testing.T) {
	call := toolCallReply(llm.ToolCall{ID: "c", Name: "echo"})
	model := llm.NewFakeModel(call, call, call)
	_, err := invoke(t, Config{Model: model, Tools: []tool.Tool{echoTool()}, RecursionLimit: 2}, "go")
	if !errors.Is(err, ErrRecursionLimit) {
		t.Fatalf("want ErrRecursionLimit, got %v", err)
	}
}

type recorder struct {
	name string
	log  *[]string
}

func (r *recorder) Name() string { return r.name }

func (r *recorder) WrapModelCall(ctx context.Context, req *ModelRequest, next ModelHandler) (*ModelResponse, error) {
	*r.log = append(*r.log, r.name+">")
	resp, err := next(ctx, req)
	*r.log = append(*r.log, "<"+r.name)
	return resp, err
}

func (r *recorder) BeforeAgent(context.Context, *runtime.State, *Runtime) (*runtime.Update, error) {
	*r.log = append(*r.log, r.name+".before_agent")
	return nil, nil
}

func (r *recorder) AfterModel(context.Context, *runtime.State, *Runtime) (*runtime.Update, error) {
	*r.log = append(*r.log, r.name+".after_model")
	return nil, nil
}

func TestLoopWrapModelCallNesting(t *testing.T) {
	var log []string
	cfg := Config{
		Model:      llm.NewFakeModel(llm.NewAssistantMessage("hi")),
		Middleware: []Middleware{&recorder{"a", &log}, &recorder{"b", &log}},
	}
	if _, err := invoke(t, cfg, "go"); err != nil {
		t.Fatal(err)
	}
	want := "a.before_agent,b.before_agent,a>,b>,<b,<a,b.after_model,a.after_model"
	if got := strings.Join(log, ","); got != want {
		t.Fatalf("hook order = %s\nwant       %s", got, want)
	}
}

func TestLoopRejectsDuplicateMiddleware(t *testing.T) {
	var log []string
	_, err := CreateAgent(Config{
		Model:      llm.NewFakeModel(),
		Middleware: []Middleware{&recorder{"a", &log}, &recorder{"a", &log}},
	})
	if err == nil || !strings.Contains(err.Error(), "duplicate middleware") {
		t.Fatalf("want duplicate middleware error, got %v", err)
	}
}

func TestLoopRequiresModel(t *testing.T) {
	if _, err := CreateAgent(Config{}); err == nil {
		t.Fatal("want error for nil model")
	}
}
