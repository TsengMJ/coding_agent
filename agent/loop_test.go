package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

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

func TestLoopToolPanicBecomesErrorMessage(t *testing.T) {
	panicky := &tool.Func{
		ToolName: "boom",
		Fn: func(context.Context, *tool.Runtime, map[string]any) (*tool.Result, error) {
			panic("kaboom")
		},
	}
	model := llm.NewFakeModel(toolCallReply(llm.ToolCall{ID: "c1", Name: "boom"}), llm.NewAssistantMessage("ok"))
	state, err := invoke(t, Config{Model: model, Tools: []tool.Tool{panicky}}, "go")
	if err != nil {
		t.Fatal(err)
	}
	if state.Messages[2].Status != llm.ToolStatusError || !strings.Contains(state.Messages[2].Text(), "kaboom") {
		t.Fatalf("unexpected tool message: %+v", state.Messages[2])
	}
}

type panicAfterModel struct{}

func (panicAfterModel) Name() string { return "panicky" }

func (panicAfterModel) AfterModel(context.Context, *runtime.State, *Runtime) (*runtime.Update, error) {
	panic("hook exploded")
}

func TestLoopNodePanicBecomesError(t *testing.T) {
	model := llm.NewFakeModel(llm.NewAssistantMessage("hi"))
	_, err := invoke(t, Config{Model: model, Middleware: []Middleware{panicAfterModel{}}}, "go")
	if !errors.Is(err, ErrPanic) || !strings.Contains(err.Error(), "hook exploded") {
		t.Fatalf("err = %v", err)
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

func TestLoopThreadResumeUsesCheckpointer(t *testing.T) {
	cp := runtime.NewMemoryCheckpointer()
	model := llm.NewFakeModel(
		llm.NewAssistantMessage("one"),
		llm.NewAssistantMessage("two"),
		llm.NewAssistantMessage("elsewhere"),
	)
	a, err := CreateAgent(Config{Model: model, Checkpointer: cp})
	if err != nil {
		t.Fatal(err)
	}
	cfg := runtime.RunConfig{ThreadID: "t1"}
	if _, err := a.Invoke(context.Background(), Input{Messages: []llm.Message{llm.NewUserMessage("hi")}}, cfg); err != nil {
		t.Fatal(err)
	}
	state, err := a.Invoke(context.Background(), Input{Messages: []llm.Message{llm.NewUserMessage("again")}}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got := roles(state.Messages); got != "user,assistant,user,assistant" {
		t.Fatalf("roles = %s", got)
	}
	if state.Messages[1].Text() != "one" || state.Messages[3].Text() != "two" {
		t.Fatalf("texts = %q %q", state.Messages[1].Text(), state.Messages[3].Text())
	}
	other, err := a.Invoke(context.Background(), Input{Messages: []llm.Message{llm.NewUserMessage("elsewhere")}}, runtime.RunConfig{ThreadID: "t2"})
	if err != nil {
		t.Fatal(err)
	}
	if got := roles(other.Messages); got != "user,assistant" {
		t.Fatalf("other thread roles = %s", got)
	}
	got, err := a.GetState(context.Background(), "t1")
	if err != nil || roles(got.Messages) != "user,assistant,user,assistant" {
		t.Fatalf("GetState = %v, %v", got, err)
	}
}

func TestLoopInterruptBeforeToolsAndResume(t *testing.T) {
	cp := runtime.NewMemoryCheckpointer()
	model := llm.NewFakeModel(
		toolCallReply(llm.ToolCall{ID: "c1", Name: "echo", Args: map[string]any{"text": "a"}}),
		llm.NewAssistantMessage("done"),
	)
	a, err := CreateAgent(Config{Model: model, Tools: []tool.Tool{echoTool()}, Checkpointer: cp})
	if err != nil {
		t.Fatal(err)
	}
	cfg := runtime.RunConfig{ThreadID: "t", Configurable: map[string]any{"interrupt_before": []string{"tools"}}}
	_, err = a.Invoke(context.Background(), Input{Messages: []llm.Message{llm.NewUserMessage("go")}}, cfg)
	var paused *runtime.InterruptError
	if !errors.As(err, &paused) || len(paused.Interrupts) != 1 || paused.Interrupts[0].ID != "tools" {
		t.Fatalf("want interrupt before tools, got %v", err)
	}
	state, err := a.Invoke(context.Background(), Input{Resume: &runtime.Command{}}, runtime.RunConfig{ThreadID: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if got := roles(state.Messages); got != "user,assistant,tool,assistant" {
		t.Fatalf("roles = %s", got)
	}
	if state.Messages[2].Text() != "echo:a" || state.Messages[3].Text() != "done" {
		t.Fatalf("messages = %+v", state.Messages)
	}
}

type gotoModel struct{ jumped bool }

func (g *gotoModel) Name() string { return "goto" }

func (g *gotoModel) AfterAgent(context.Context, *runtime.State, *Runtime) (*runtime.Command, error) {
	if g.jumped {
		return nil, nil
	}
	g.jumped = true
	return &runtime.Command{Goto: GotoModel}, nil
}

func TestLoopAfterAgentGotoModel(t *testing.T) {
	model := llm.NewFakeModel(llm.NewAssistantMessage("a"), llm.NewAssistantMessage("b"))
	state, err := invoke(t, Config{Model: model, Middleware: []Middleware{&gotoModel{}}}, "go")
	if err != nil {
		t.Fatal(err)
	}
	if got := roles(state.Messages); got != "user,assistant,assistant" {
		t.Fatalf("roles = %s", got)
	}
	if state.Messages[2].Text() != "b" {
		t.Fatalf("final = %q", state.Messages[2].Text())
	}
}

func TestLoopStreamEmitsValues(t *testing.T) {
	a, err := CreateAgent(Config{Model: llm.NewFakeModel(llm.NewAssistantMessage("hi"))})
	if err != nil {
		t.Fatal(err)
	}
	var modes []runtime.StreamMode
	for ev, err := range a.Stream(context.Background(), Input{Messages: []llm.Message{llm.NewUserMessage("go")}}, runtime.RunConfig{}) {
		if err != nil {
			t.Fatal(err)
		}
		modes = append(modes, ev.Mode)
	}
	if len(modes) == 0 || modes[0] != runtime.StreamValues {
		t.Fatalf("modes = %v", modes)
	}
}

func TestLoopStreamEmitsModelChunksWhenMessagesRequested(t *testing.T) {
	a, err := CreateAgent(Config{Model: llm.NewFakeModel(llm.NewAssistantMessage("hello big world"))})
	if err != nil {
		t.Fatal(err)
	}
	var text strings.Builder
	var final *runtime.State
	for ev, err := range a.Stream(context.Background(), Input{Messages: []llm.Message{llm.NewUserMessage("go")}}, runtime.RunConfig{}, runtime.StreamValues, runtime.StreamMessages) {
		if err != nil {
			t.Fatal(err)
		}
		switch ev.Mode {
		case runtime.StreamMessages:
			if final != nil && len(final.Messages) > 1 {
				t.Fatal("chunk arrived after the assistant message was stored")
			}
			text.WriteString(ev.Data.(llm.Chunk).Delta.Text())
		case runtime.StreamValues:
			final = ev.Data.(*runtime.State)
		}
	}
	if text.String() != "hello big world" {
		t.Fatalf("streamed text = %q", text.String())
	}
	if got := final.Messages[len(final.Messages)-1].Text(); got != "hello big world" {
		t.Fatalf("stored text = %q", got)
	}
}

// barrierTool returns only after n calls are in flight at the same time, so
// sequential execution times out instead of passing.
func barrierTool(n int) tool.Tool {
	var mu sync.Mutex
	started := 0
	all := make(chan struct{})
	return &tool.Func{
		ToolName: "wait",
		Fn: func(ctx context.Context, _ *tool.Runtime, args map[string]any) (*tool.Result, error) {
			mu.Lock()
			started++
			if started == n {
				close(all)
			}
			mu.Unlock()
			select {
			case <-all:
			case <-time.After(2 * time.Second):
				return nil, errors.New("calls did not overlap")
			}
			id, _ := args["id"].(string)
			return &tool.Result{Content: []llm.ContentBlock{llm.TextBlock("done:" + id)}}, nil
		},
	}
}

func TestLoopRunsToolCallsInParallel(t *testing.T) {
	model := llm.NewFakeModel(
		toolCallReply(
			llm.ToolCall{ID: "c1", Name: "wait", Args: map[string]any{"id": "1"}},
			llm.ToolCall{ID: "c2", Name: "wait", Args: map[string]any{"id": "2"}},
			llm.ToolCall{ID: "c3", Name: "wait", Args: map[string]any{"id": "3"}},
		),
		llm.NewAssistantMessage("ok"),
	)
	state, err := invoke(t, Config{Model: model, Tools: []tool.Tool{barrierTool(3)}}, "go")
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"done:1", "done:2", "done:3"} {
		msg := state.Messages[2+i]
		if msg.Status == llm.ToolStatusError || msg.Text() != want || msg.ToolCallID != fmt.Sprintf("c%d", i+1) {
			t.Fatalf("tool message %d = %+v, want %s", i, msg, want)
		}
	}
}

func TestLoopParallelCommandUpdatesApplyInCallOrder(t *testing.T) {
	setter := &tool.Func{
		ToolName: "set",
		Fn: func(_ context.Context, rt *tool.Runtime, args map[string]any) (*tool.Result, error) {
			v, _ := args["v"].(string)
			if v == "first" {
				time.Sleep(20 * time.Millisecond)
			}
			return &tool.Result{Command: &runtime.Command{Update: &runtime.Update{
				Messages: []llm.Message{llm.NewToolMessage(rt.ToolCallID, "set", "set "+v)},
				Values:   map[string]any{"last": v},
			}}}, nil
		},
	}
	model := llm.NewFakeModel(
		toolCallReply(
			llm.ToolCall{ID: "c1", Name: "set", Args: map[string]any{"v": "first"}},
			llm.ToolCall{ID: "c2", Name: "set", Args: map[string]any{"v": "second"}},
		),
		llm.NewAssistantMessage("ok"),
	)
	state, err := invoke(t, Config{Model: model, Tools: []tool.Tool{setter}}, "go")
	if err != nil {
		t.Fatal(err)
	}
	if got := state.Values["last"]; got != "second" {
		t.Fatalf("last = %v, want second", got)
	}
	if state.Messages[2].ToolCallID != "c1" || state.Messages[3].ToolCallID != "c2" {
		t.Fatalf("tool message order: %s, %s", state.Messages[2].ToolCallID, state.Messages[3].ToolCallID)
	}
}

func TestLoopStreamWithoutMessagesModeEmitsNoChunks(t *testing.T) {
	a, err := CreateAgent(Config{Model: llm.NewFakeModel(llm.NewAssistantMessage("hi there"))})
	if err != nil {
		t.Fatal(err)
	}
	for ev, err := range a.Stream(context.Background(), Input{Messages: []llm.Message{llm.NewUserMessage("go")}}, runtime.RunConfig{}) {
		if err != nil {
			t.Fatal(err)
		}
		if ev.Mode == runtime.StreamMessages {
			t.Fatal("unexpected chunk event")
		}
	}
}
