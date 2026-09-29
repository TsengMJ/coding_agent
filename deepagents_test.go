package deepagents

import (
	"context"
	"errors"
	"testing"

	"github.com/tsengmingchun/deepagents_golang/agent"
	"github.com/tsengmingchun/deepagents_golang/internal/notimpl"
	"github.com/tsengmingchun/deepagents_golang/llm"
	"github.com/tsengmingchun/deepagents_golang/middleware/asyncsubagents"
	"github.com/tsengmingchun/deepagents_golang/middleware/filesystem"
	"github.com/tsengmingchun/deepagents_golang/middleware/hitl"
	"github.com/tsengmingchun/deepagents_golang/middleware/memory"
	"github.com/tsengmingchun/deepagents_golang/middleware/patchtoolcalls"
	"github.com/tsengmingchun/deepagents_golang/middleware/promptcaching"
	"github.com/tsengmingchun/deepagents_golang/middleware/rubric"
	"github.com/tsengmingchun/deepagents_golang/middleware/skills"
	"github.com/tsengmingchun/deepagents_golang/middleware/subagents"
	"github.com/tsengmingchun/deepagents_golang/middleware/summarization"
	"github.com/tsengmingchun/deepagents_golang/middleware/todolist"
	"github.com/tsengmingchun/deepagents_golang/middleware/toolexclusion"
	"github.com/tsengmingchun/deepagents_golang/middleware/unsupportedcontent"
	"github.com/tsengmingchun/deepagents_golang/runtime"
	"github.com/tsengmingchun/deepagents_golang/tool"
)

// Hook conformance: each middleware exposes the hooks its Python counterpart overrides.
var (
	_ agent.ToolProvider        = (*filesystem.Middleware)(nil)
	_ agent.StateSchemaProvider = (*filesystem.Middleware)(nil)
	_ agent.WrapModelCallHook   = (*filesystem.Middleware)(nil)
	_ agent.WrapToolCallHook    = (*filesystem.Middleware)(nil)

	_ agent.ToolProvider      = (*subagents.Middleware)(nil)
	_ agent.WrapModelCallHook = (*subagents.Middleware)(nil)

	_ agent.ToolProvider        = (*asyncsubagents.Middleware)(nil)
	_ agent.StateSchemaProvider = (*asyncsubagents.Middleware)(nil)

	_ agent.WrapModelCallHook   = (*summarization.Middleware)(nil)
	_ agent.StateSchemaProvider = (*summarization.Middleware)(nil)
	_ agent.ToolProvider        = (*summarization.ToolMiddleware)(nil)

	_ agent.BeforeAgentHook   = (*memory.Middleware)(nil)
	_ agent.WrapModelCallHook = (*memory.Middleware)(nil)

	_ agent.BeforeAgentHook   = (*skills.Middleware)(nil)
	_ agent.WrapModelCallHook = (*skills.Middleware)(nil)

	_ agent.BeforeAgentHook = (*rubric.Middleware)(nil)
	_ agent.AfterAgentHook  = (*rubric.Middleware)(nil)

	_ agent.BeforeAgentHook   = (*patchtoolcalls.Middleware)(nil)
	_ agent.WrapModelCallHook = (*unsupportedcontent.Middleware)(nil)
	_ agent.AfterModelHook    = (*hitl.Middleware)(nil)
	_ agent.ToolProvider      = (*todolist.Middleware)(nil)
	_ agent.WrapModelCallHook = (*promptcaching.Middleware)(nil)
	_ agent.WrapModelCallHook = (*toolexclusion.Middleware)(nil)

	_ subagents.Spec = subagents.SubAgent{}
	_ subagents.Spec = subagents.CompiledSubAgent{}
	_ subagents.Spec = asyncsubagents.AsyncSubAgent{}
)

func TestCreateDeepAgentRunsToolRoundTrip(t *testing.T) {
	model := llm.NewFakeModel(
		llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "c1", Name: "add"}}},
		llm.NewAssistantMessage("the answer is 3"),
	)
	add := &tool.Func{
		ToolName: "add",
		Fn: func(context.Context, *tool.Runtime, map[string]any) (*tool.Result, error) {
			return &tool.Result{Content: []llm.ContentBlock{llm.TextBlock("3")}}, nil
		},
	}
	a, err := CreateDeepAgent(WithModel(model), WithTools(add), WithSystemPrompt("you add numbers"))
	if err != nil {
		t.Fatal(err)
	}
	state, err := a.Invoke(context.Background(), agent.Input{Messages: []llm.Message{llm.NewUserMessage("1+2")}}, runtime.RunConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if got := state.Messages[len(state.Messages)-1].Text(); got != "the answer is 3" {
		t.Fatalf("final text = %q", got)
	}
	if first := model.Requests()[0].Messages[0]; first.Role != llm.RoleSystem || first.Text() != "you add numbers" {
		t.Fatalf("system prompt not sent first: %+v", first)
	}
}

func TestCreateDeepAgentRejectsSubAgents(t *testing.T) {
	_, err := CreateDeepAgent(WithModel(llm.NewFakeModel()), WithSubAgents(subagents.SubAgent{}))
	if !errors.Is(err, notimpl.ErrNotImplemented) {
		t.Fatalf("want ErrNotImplemented, got %v", err)
	}
}

func TestRequiredMiddlewareNames(t *testing.T) {
	want := map[string]bool{"FilesystemMiddleware": true, "SubAgentMiddleware": true}
	for _, name := range RequiredMiddleware {
		if !want[name] {
			t.Fatalf("unexpected required middleware %q", name)
		}
	}
}

func TestFilesystemExposesAllToolsByDefault(t *testing.T) {
	if got := len(filesystem.New(filesystem.Options{}).Tools()); got != len(filesystem.AllTools) {
		t.Fatalf("want %d tools, got %d", len(filesystem.AllTools), got)
	}
}
