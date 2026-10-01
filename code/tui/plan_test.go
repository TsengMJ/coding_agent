package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/tsengmingchun/deepagents_golang/code/askuser"
	"github.com/tsengmingchun/deepagents_golang/code/config"
	"github.com/tsengmingchun/deepagents_golang/code/server"
	"github.com/tsengmingchun/deepagents_golang/code/storage"
	"github.com/tsengmingchun/deepagents_golang/llm"
	"github.com/tsengmingchun/deepagents_golang/middleware/planmode"
	"github.com/tsengmingchun/deepagents_golang/middleware/todolist"
)

func newPlanTestService(t *testing.T, replies ...llm.Message) (*server.Service, *llm.FakeModel) {
	t.Helper()
	model := llm.NewFakeModel(replies...)
	svc, err := server.New(context.Background(), server.Config{Storage: storage.NewMemory(), Model: model, Paths: config.Paths{UserDir: t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	return svc, model
}

func toolNames(req llm.ChatRequest) map[string]bool {
	names := map[string]bool{}
	for _, s := range req.Tools {
		names[s.Name] = true
	}
	return names
}

func TestPlanModeCreatesPlanThenBuild(t *testing.T) {
	plan := llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "p", Name: planmode.ToolName, Args: map[string]any{
		"name": "Add flag", "overview": "Add a -v flag.", "plan": "## Approach\n\nEdit main.go",
		"todos": []any{map[string]any{"id": "flag", "content": "Add the flag in main.go"}},
	}}}}
	svc, model := newPlanTestService(t, plan, llm.NewAssistantMessage("plan summary"), llm.NewAssistantMessage("built"))
	m := newTestModel(t, svc, t.TempDir())

	drain(t, m, m.submit("/plan"))
	if m.runMode != server.ModePlan || !strings.Contains(m.View(), "PLAN MODE") {
		t.Fatalf("/plan: mode %q view:\n%s", m.runMode, m.View())
	}
	drain(t, m, m.submit("add a flag"))
	if names := toolNames(model.Requests()[0]); names["write_file"] || !names[planmode.ToolName] {
		t.Fatalf("plan run tools = %v", names)
	}
	if !hasEntry(m, entryTool, "plan · Add flag") || !hasEntry(m, entryAssistant, "Edit main.go") || !hasEntry(m, entryInfo, "plan ready") {
		t.Fatalf("plan not shown: %+v", m.entries)
	}

	drain(t, m, m.submit("/build"))
	if m.runMode != server.ModeAgent || !hasEntry(m, entryAssistant, "built") {
		t.Fatalf("/build: mode %q entries %+v", m.runMode, m.entries)
	}
	ts, _ := svc.GetThread(context.Background(), m.thread)
	if ts.Plan.Status != planmode.StatusBuilding || len(ts.Todos) != 1 {
		t.Fatalf("after build: %+v %+v", ts.Plan, ts.Todos)
	}
}

func TestAskUserQuestionsAreAnswered(t *testing.T) {
	ask := llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "q", Name: askuser.ToolName, Args: map[string]any{
		"questions": []any{
			map[string]any{"id": "db", "prompt": "Which DB?", "options": []any{
				map[string]any{"id": "pg", "label": "Postgres"}, map[string]any{"id": "lite", "label": "SQLite"},
			}},
			map[string]any{"id": "extra", "prompt": "Anything else?", "options": []any{
				map[string]any{"id": "no", "label": "No"}, map[string]any{"id": "yes", "label": "Yes"},
			}},
		},
	}}}}
	svc, model := newPlanTestService(t, ask, llm.NewAssistantMessage("thanks"))
	m := newTestModel(t, svc, t.TempDir())
	drain(t, m, m.submit("go"))
	if m.mode != modeAsk || !strings.Contains(m.View(), "Question 1/2: Which DB?") {
		t.Fatalf("mode %v view:\n%s", m.mode, m.View())
	}
	press(t, m, "2")
	press(t, m, "enter")
	if !strings.Contains(m.View(), "Question 2/2") {
		t.Fatalf("second question not shown:\n%s", m.View())
	}
	press(t, m, "down")
	press(t, m, "down")
	press(t, m, "enter")
	for _, r := range "use redis" {
		press(t, m, string(r))
	}
	press(t, m, "enter")
	if m.mode != modeIdle || !hasEntry(m, entryAssistant, "thanks") {
		t.Fatalf("mode %v entries %+v", m.mode, m.entries)
	}
	reqs := model.Requests()
	var answer string
	for _, msg := range reqs[len(reqs)-1].Messages {
		if msg.ToolCallID == "q" {
			answer = msg.Text()
		}
	}
	if !strings.Contains(answer, "-> SQLite") || !strings.Contains(answer, "-> Other: use redis") {
		t.Fatalf("answer = %q", answer)
	}
}

func TestTodoRowShowsFullListAfterMerge(t *testing.T) {
	m := newTestModel(t, newService(t), t.TempDir())
	call := llm.ToolCall{ID: "t", Name: todolist.ToolName, Args: map[string]any{"merge": true, "todos": []any{
		map[string]any{"id": "a", "content": "first", "status": "completed"},
	}}}
	m.Update(eventMsg{ev: server.Event{Kind: server.EventMessage, Message: &llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{call}}}})
	if !hasEntry(m, entryTool, "todos (update) 1/1") {
		t.Fatalf("entries = %+v", m.entries)
	}
	res := llm.NewToolMessage("t", todolist.ToolName, todolist.ResultPrefix+`[{"id":"a","content":"first","status":"completed"},{"id":"b","content":"second","status":"cancelled","notes":"not needed"}]`)
	m.Update(eventMsg{ev: server.Event{Kind: server.EventMessage, Message: &res}})
	for _, want := range []string{"todos 1/2", "⊘ second", "not needed"} {
		if !hasEntry(m, entryTool, want) {
			t.Fatalf("missing %q: %+v", want, m.entries)
		}
	}
}
