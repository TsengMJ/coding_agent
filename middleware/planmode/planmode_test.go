package planmode

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/tsengmingchun/quill/agent"
	"github.com/tsengmingchun/quill/llm"
	"github.com/tsengmingchun/quill/middleware/subagents"
	"github.com/tsengmingchun/quill/middleware/todolist"
	"github.com/tsengmingchun/quill/runtime"
	"github.com/tsengmingchun/quill/tool"
)

type harness struct {
	agent agent.Agent
	model *llm.FakeModel
	store *MemoryStore
	wrote *[]string
}

func newHarness(t *testing.T, script ...llm.Message) harness {
	t.Helper()
	store := NewMemoryStore()
	mw, err := New(Options{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	var wrote []string
	writeFile := &tool.Func{ToolName: "write_file", ToolSchema: tool.Schema{"type": "object"}, Fn: func(_ context.Context, _ *tool.Runtime, args map[string]any) (*tool.Result, error) {
		wrote = append(wrote, args["file_path"].(string))
		return &tool.Result{Content: []llm.ContentBlock{llm.TextBlock("ok")}}, nil
	}}
	readFile := &tool.Func{ToolName: "read_file", ToolSchema: tool.Schema{"type": "object"}, Fn: func(context.Context, *tool.Runtime, map[string]any) (*tool.Result, error) {
		return &tool.Result{Content: []llm.ContentBlock{llm.TextBlock("content")}}, nil
	}}
	model := llm.NewFakeModel(script...)
	a, err := agent.CreateAgent(agent.Config{
		Model:        model,
		Tools:        []tool.Tool{writeFile, readFile},
		Middleware:   []agent.Middleware{todolist.New("", ""), mw},
		Checkpointer: runtime.NewMemoryCheckpointer(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return harness{agent: a, model: model, store: store, wrote: &wrote}
}

func planCfg() runtime.RunConfig {
	return runtime.RunConfig{ThreadID: "t1", Configurable: map[string]any{ModeKey: ModePlan}}
}

func (h harness) run(t *testing.T, cfg runtime.RunConfig, text string) *runtime.State {
	t.Helper()
	st, err := h.agent.Invoke(context.Background(), agent.Input{Messages: []llm.Message{llm.NewUserMessage(text)}}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func toolNames(req llm.ChatRequest) []string {
	var names []string
	for _, s := range req.Tools {
		names = append(names, s.Name)
	}
	return names
}

func call(id, name string, args map[string]any) llm.Message {
	return llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: id, Name: name, Args: args}}}
}

func planArgsMap(name string) map[string]any {
	return map[string]any{
		"name": name, "overview": "Add a flag.", "plan": "## Approach\n\nEdit main.go.",
		"todos": []any{
			map[string]any{"id": "flag", "content": "Add -v flag in main.go"},
			map[string]any{"id": "test", "content": "Cover it in main_test.go"},
		},
	}
}

func TestPlanModeOffersOnlyReadOnlyTools(t *testing.T) {
	h := newHarness(t, llm.NewAssistantMessage("ok"))
	h.run(t, planCfg(), "plan it")
	req := h.model.Requests()[0]
	names := toolNames(req)
	if slices.Contains(names, "write_file") || slices.Contains(names, todolist.ToolName) {
		t.Fatalf("plan mode tools = %v", names)
	}
	if !slices.Contains(names, "read_file") || !slices.Contains(names, ToolName) {
		t.Fatalf("plan mode tools = %v", names)
	}
	if !strings.Contains(req.Messages[0].Text(), "# Plan Mode") {
		t.Fatal("plan mode prompt missing")
	}
}

func TestAgentModeHidesCreatePlan(t *testing.T) {
	h := newHarness(t, llm.NewAssistantMessage("ok"))
	h.run(t, runtime.RunConfig{ThreadID: "t1"}, "do it")
	req := h.model.Requests()[0]
	names := toolNames(req)
	if slices.Contains(names, ToolName) || !slices.Contains(names, "write_file") {
		t.Fatalf("agent mode tools = %v", names)
	}
	if strings.Contains(req.Messages[0].Text(), "# Plan Mode") {
		t.Fatal("plan mode prompt in agent mode")
	}
}

func TestPlanModeBlocksWriteCalls(t *testing.T) {
	h := newHarness(t, call("w1", "write_file", map[string]any{"file_path": "/a"}), llm.NewAssistantMessage("ok"))
	st := h.run(t, planCfg(), "plan it")
	if len(*h.wrote) != 0 {
		t.Fatalf("write_file ran in plan mode: %v", *h.wrote)
	}
	if msg := st.Messages[2]; msg.Status != llm.ToolStatusError || !strings.Contains(msg.Text(), "not available in plan mode") {
		t.Fatalf("tool message = %+v", msg)
	}
}

func newSubagentHarness(t *testing.T, script ...llm.Message) (agent.Agent, *llm.FakeModel, *[]string) {
	t.Helper()
	mw, err := New(Options{Store: NewMemoryStore(), ReadOnlySubagents: []string{"explorer"}})
	if err != nil {
		t.Fatal(err)
	}
	var started []string
	task := &tool.Func{ToolName: subagents.TaskToolName, ToolSchema: tool.Schema{"type": "object"}, Fn: func(_ context.Context, _ *tool.Runtime, args map[string]any) (*tool.Result, error) {
		started = append(started, args["subagent_type"].(string))
		return &tool.Result{Content: []llm.ContentBlock{llm.TextBlock("report")}}, nil
	}}
	output := &tool.Func{ToolName: subagents.TaskOutputToolName, ToolSchema: tool.Schema{"type": "object"}, Fn: func(context.Context, *tool.Runtime, map[string]any) (*tool.Result, error) {
		return &tool.Result{Content: []llm.ContentBlock{llm.TextBlock("done")}}, nil
	}}
	model := llm.NewFakeModel(script...)
	a, err := agent.CreateAgent(agent.Config{Model: model, Tools: []tool.Tool{task, output}, Middleware: []agent.Middleware{mw}})
	if err != nil {
		t.Fatal(err)
	}
	return a, model, &started
}

func TestPlanModeAllowsOnlyReadOnlySubagents(t *testing.T) {
	a, model, started := newSubagentHarness(t,
		llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{
			{ID: "t1", Name: subagents.TaskToolName, Args: map[string]any{"subagent_type": "explorer", "description": "look"}},
			{ID: "t2", Name: subagents.TaskToolName, Args: map[string]any{"subagent_type": "general-purpose", "description": "edit"}},
		}},
		llm.NewAssistantMessage("ok"),
	)
	st, err := a.Invoke(context.Background(), agent.Input{Messages: []llm.Message{llm.NewUserMessage("plan")}}, planCfg())
	if err != nil {
		t.Fatal(err)
	}
	names := toolNames(model.Requests()[0])
	if !slices.Contains(names, subagents.TaskToolName) || !slices.Contains(names, subagents.TaskOutputToolName) {
		t.Fatalf("plan mode tools = %v", names)
	}
	if !strings.Contains(model.Requests()[0].Messages[0].Text(), "`explorer`") {
		t.Fatal("plan mode prompt does not list the read-only subagents")
	}
	if !slices.Equal(*started, []string{"explorer"}) {
		t.Fatalf("started = %v", *started)
	}
	for _, m := range st.Messages {
		if m.ToolCallID == "t2" && (m.Status != llm.ToolStatusError || !strings.Contains(m.Text(), "not available in plan mode")) {
			t.Fatalf("general-purpose result = %+v", m)
		}
	}
}

func TestPlanModeHidesTaskWithoutReadOnlySubagents(t *testing.T) {
	h := newHarness(t, llm.NewAssistantMessage("ok"))
	h.run(t, planCfg(), "plan it")
	if names := toolNames(h.model.Requests()[0]); slices.Contains(names, subagents.TaskToolName) {
		t.Fatalf("plan mode tools = %v", names)
	}
}

func TestCreatePlanWritesFileAndState(t *testing.T) {
	h := newHarness(t, call("p1", ToolName, planArgsMap("Add verbose flag")), llm.NewAssistantMessage("summary"))
	st := h.run(t, planCfg(), "plan it")
	p := PlanOf(st)
	if p == nil || p.Status != StatusDraft || len(p.Todos) != 2 || p.Todos[0].Status != todolist.StatusPending {
		t.Fatalf("plan = %+v", p)
	}
	if p.ID != "t1-add-verbose-flag" {
		t.Fatalf("id = %q", p.ID)
	}
	saved, err := h.store.Get(context.Background(), p.ID)
	if err != nil || saved == nil {
		t.Fatalf("stored plan = %+v, %v", saved, err)
	}
	md := RenderMarkdown(*saved)
	for _, want := range []string{"# Add verbose flag", "Edit main.go.", "- [ ] Add -v flag in main.go (`flag`)"} {
		if !strings.Contains(md, want) {
			t.Fatalf("stored plan missing %q:\n%s", want, md)
		}
	}
}

func TestCreatePlanAgainReplacesStoredPlan(t *testing.T) {
	h := newHarness(t,
		call("p1", ToolName, planArgsMap("First")), llm.NewAssistantMessage("v1"),
		call("p2", ToolName, planArgsMap("Second")), llm.NewAssistantMessage("v2"),
	)
	first := PlanOf(h.run(t, planCfg(), "plan it")).ID
	st := h.run(t, planCfg(), "revise")
	p := PlanOf(st)
	if p.ID != first || p.Name != "Second" {
		t.Fatalf("plan = %+v, want id %q", p, first)
	}
	if saved, _ := h.store.Get(context.Background(), first); saved == nil || saved.Name != "Second" {
		t.Fatalf("stored plan = %+v", saved)
	}
}

func TestBuildMarksStoredPlanBuilding(t *testing.T) {
	h := newHarness(t, call("p1", ToolName, planArgsMap("Add verbose flag")), llm.NewAssistantMessage("summary"), llm.NewAssistantMessage("building"))
	id := PlanOf(h.run(t, planCfg(), "plan it")).ID
	h.run(t, runtime.RunConfig{ThreadID: "t1", Configurable: map[string]any{BuildKey: true}}, "build the plan")
	if saved, _ := h.store.Get(context.Background(), id); saved == nil || saved.Status != StatusBuilding {
		t.Fatalf("stored plan = %+v", saved)
	}
}

func TestNewRequiresStore(t *testing.T) {
	if _, err := New(Options{}); err == nil {
		t.Fatal("New without Store succeeded")
	}
}

func TestBuildSeedsTodosAndAddsPlanPrompt(t *testing.T) {
	h := newHarness(t, call("p1", ToolName, planArgsMap("Add verbose flag")), llm.NewAssistantMessage("summary"), llm.NewAssistantMessage("building"))
	h.run(t, planCfg(), "plan it")
	st := h.run(t, runtime.RunConfig{ThreadID: "t1", Configurable: map[string]any{BuildKey: true}}, "build the plan")
	todos, err := todolist.ParseTodos(st.Values[todolist.TodosStateKey])
	if err != nil {
		t.Fatal(err)
	}
	if len(todos) != 2 || todos[0].ID != "flag" || todos[0].Status != todolist.StatusInProgress || todos[1].Status != todolist.StatusPending {
		t.Fatalf("todos = %+v", todos)
	}
	if p := PlanOf(st); p.Status != StatusBuilding {
		t.Fatalf("plan status = %q", p.Status)
	}
	reqs := h.model.Requests()
	sys := reqs[len(reqs)-1].Messages[0].Text()
	if !strings.Contains(sys, "# Approved Plan") || !strings.Contains(sys, "`flag` Add -v flag") {
		t.Fatalf("build prompt missing:\n%s", sys)
	}
}

func TestBuildWithoutPlanFails(t *testing.T) {
	h := newHarness(t, llm.NewAssistantMessage("x"))
	_, err := h.agent.Invoke(context.Background(), agent.Input{Messages: []llm.Message{llm.NewUserMessage("build")}},
		runtime.RunConfig{ThreadID: "t1", Configurable: map[string]any{BuildKey: true}})
	if err == nil || !strings.Contains(err.Error(), "no plan to build") {
		t.Fatalf("err = %v", err)
	}
}

func TestSlug(t *testing.T) {
	cases := map[string]string{"Add verbose flag!": "add-verbose-flag", "中文": "plan", "  a--b  ": "a-b"}
	for in, want := range cases {
		if got := slug(in); got != want {
			t.Errorf("slug(%q) = %q, want %q", in, got, want)
		}
	}
}
