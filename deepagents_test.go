package deepagents

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/tsengmingchun/deepagents_golang/agent"
	"github.com/tsengmingchun/deepagents_golang/backend"
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

func TestCancelledToolCallIsPatchedOnNextTurn(t *testing.T) {
	model := llm.NewFakeModel(
		llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "c1", Name: "slow"}}},
		llm.NewAssistantMessage("recovered"),
	)
	started := make(chan struct{})
	slow := &tool.Func{
		ToolName: "slow",
		Fn: func(ctx context.Context, _ *tool.Runtime, _ map[string]any) (*tool.Result, error) {
			close(started)
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}
	a, err := CreateDeepAgent(WithModel(model), WithTools(slow), WithCheckpointer(runtime.NewMemoryCheckpointer()))
	if err != nil {
		t.Fatal(err)
	}
	cfg := runtime.RunConfig{ThreadID: "t1"}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { <-started; cancel() }()
	if _, err := a.Invoke(ctx, agent.Input{Messages: []llm.Message{llm.NewUserMessage("go")}}, cfg); !errors.Is(err, context.Canceled) {
		t.Fatalf("first run err = %v", err)
	}

	state, err := a.Invoke(context.Background(), agent.Input{Messages: []llm.Message{llm.NewUserMessage("again")}}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got := state.Messages[len(state.Messages)-1].Text(); got != "recovered" {
		t.Fatalf("final text = %q", got)
	}
	sent := model.Requests()[1].Messages
	var roles []llm.Role
	for _, m := range sent {
		roles = append(roles, m.Role)
	}
	want := []llm.Role{llm.RoleUser, llm.RoleAssistant, llm.RoleTool, llm.RoleUser}
	if !slices.Equal(roles, want) {
		t.Fatalf("second request roles = %v", roles)
	}
}

func TestCreateDeepAgentRejectsSubAgents(t *testing.T) {
	_, err := CreateDeepAgent(WithModel(llm.NewFakeModel()), WithSubAgents(subagents.SubAgent{}))
	if !errors.Is(err, notimpl.ErrNotImplemented) {
		t.Fatalf("want ErrNotImplemented, got %v", err)
	}
}

func TestCreateDeepAgentWithBackendExposesFilesystemTools(t *testing.T) {
	model := llm.NewFakeModel(llm.NewAssistantMessage("ok"))
	b := backend.NewFilesystemBackend(backend.FilesystemOptions{RootDir: t.TempDir(), VirtualMode: true})
	a, err := CreateDeepAgent(WithModel(model), WithBackend(b))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Invoke(context.Background(), agent.Input{Messages: []llm.Message{llm.NewUserMessage("hi")}}, runtime.RunConfig{}); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, spec := range model.Requests()[0].Tools {
		names = append(names, spec.Name)
	}
	if want := []string{"ls", "read_file", "write_file", "edit_file", "glob", "grep"}; !slices.Equal(names, want) {
		t.Fatalf("tools = %v, want %v", names, want)
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

func TestFilesystemExposesDefaultToolsWithShellBackend(t *testing.T) {
	b := backend.NewLocalShellBackend(backend.LocalShellOptions{FilesystemOptions: backend.FilesystemOptions{RootDir: t.TempDir(), VirtualMode: true}})
	m, err := filesystem.New(filesystem.Options{Backend: b})
	if err != nil {
		t.Fatal(err)
	}
	if got := len(m.Tools()); got != len(filesystem.DefaultTools) {
		t.Fatalf("want %d tools, got %d", len(filesystem.DefaultTools), got)
	}
}

func TestCreateDeepAgentWithMemoryInjectsAgentsMD(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("always run tests"), 0o600); err != nil {
		t.Fatal(err)
	}
	model := llm.NewFakeModel(llm.NewAssistantMessage("ok"))
	b := backend.NewFilesystemBackend(backend.FilesystemOptions{RootDir: dir, VirtualMode: true})
	a, err := CreateDeepAgent(WithModel(model), WithBackend(b), WithMemory("/AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Invoke(context.Background(), agent.Input{Messages: []llm.Message{llm.NewUserMessage("hi")}}, runtime.RunConfig{}); err != nil {
		t.Fatal(err)
	}
	if sys := model.Requests()[0].Messages[0].Text(); !strings.Contains(sys, "always run tests") {
		t.Fatalf("system prompt:\n%s", sys)
	}
}

func middlewareNames(stack []agent.Middleware) []string {
	var names []string
	for _, m := range stack {
		names = append(names, m.Name())
	}
	return names
}

func TestBuildMiddlewareAddsSummarizationAfterFilesystem(t *testing.T) {
	b := backend.NewFilesystemBackend(backend.FilesystemOptions{RootDir: t.TempDir(), VirtualMode: true})
	stack, err := buildMiddleware(Options{Backend: b}, llm.NewFakeModel())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"FilesystemMiddleware", "SummarizationMiddleware", "PatchToolCallsMiddleware"}
	if got := middlewareNames(stack); !slices.Equal(got, want) {
		t.Fatalf("stack = %v, want %v", got, want)
	}
}

func TestCallerMiddlewareWithBaseNameReplacesItInPlace(t *testing.T) {
	b := backend.NewFilesystemBackend(backend.FilesystemOptions{RootDir: t.TempDir(), VirtualMode: true})
	custom, err := summarization.New(summarization.Options{
		Backend: b,
		Keep:    summarization.ContextSize{Kind: summarization.SizeMessages, Value: 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	stack, err := buildMiddleware(Options{Backend: b, Middleware: []agent.Middleware{custom, todolist.New("", "")}}, llm.NewFakeModel())
	if err != nil {
		t.Fatal(err)
	}
	if stack[1] != agent.Middleware(custom) {
		t.Fatalf("stack[1] = %T, want the caller's summarization middleware", stack[1])
	}
	if got := middlewareNames(stack); len(got) != 4 || got[3] != "TodoListMiddleware" {
		t.Fatalf("stack = %v", got)
	}
}

func TestDuplicateCallerMiddlewareIsRejected(t *testing.T) {
	_, err := buildMiddleware(Options{Middleware: []agent.Middleware{todolist.New("", ""), todolist.New("", "")}}, llm.NewFakeModel())
	if err == nil || !strings.Contains(err.Error(), "duplicate middleware") {
		t.Fatalf("err = %v", err)
	}
}
