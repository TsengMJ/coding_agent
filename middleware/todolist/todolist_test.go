package todolist

import (
	"context"
	"strings"
	"testing"

	"github.com/tsengmingchun/deepagents_golang/agent"
	"github.com/tsengmingchun/deepagents_golang/llm"
	"github.com/tsengmingchun/deepagents_golang/runtime"
)

func todosArg(items ...map[string]any) map[string]any {
	list := make([]any, len(items))
	for i, it := range items {
		list[i] = it
	}
	return map[string]any{"todos": list}
}

func run(t *testing.T, script ...llm.Message) (*runtime.State, *llm.FakeModel) {
	t.Helper()
	model := llm.NewFakeModel(append(script, llm.NewAssistantMessage("done"))...)
	a, err := agent.CreateAgent(agent.Config{Model: model, Middleware: []agent.Middleware{New("", "")}})
	if err != nil {
		t.Fatal(err)
	}
	st, err := a.Invoke(context.Background(), agent.Input{Messages: []llm.Message{llm.NewUserMessage("plan")}}, runtime.RunConfig{})
	if err != nil {
		t.Fatal(err)
	}
	return st, model
}

func TestWriteTodosUpdatesState(t *testing.T) {
	st, _ := run(t, llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{
		ID: "t1", Name: ToolName, Args: todosArg(
			map[string]any{"content": "read code", "status": "in_progress"},
			map[string]any{"content": "write tests", "status": "pending"},
		),
	}}})
	todos, err := ParseTodos(st.Values[TodosStateKey])
	if err != nil {
		t.Fatal(err)
	}
	if len(todos) != 2 || todos[0].Status != StatusInProgress || todos[1].Content != "write tests" {
		t.Fatalf("todos = %+v", todos)
	}
	if msg := st.Messages[2]; msg.ToolCallID != "t1" || !strings.HasPrefix(msg.Text(), "Updated todo list to") {
		t.Fatalf("tool message = %+v", msg)
	}
}

func TestWriteTodosRejectsBadStatus(t *testing.T) {
	st, _ := run(t, llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{
		ID: "t1", Name: ToolName, Args: todosArg(map[string]any{"content": "x", "status": "done"}),
	}}})
	if st.Messages[2].Status != llm.ToolStatusError {
		t.Fatalf("tool message = %+v", st.Messages[2])
	}
	if _, ok := st.Values[TodosStateKey]; ok {
		t.Fatal("todos must not be set on error")
	}
}

func TestParallelWriteTodosAreRejected(t *testing.T) {
	arg := todosArg(map[string]any{"content": "x", "status": "pending"})
	st, _ := run(t, llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{
		{ID: "t1", Name: ToolName, Args: arg},
		{ID: "t2", Name: ToolName, Args: arg},
	}})
	for _, msg := range st.Messages[2:4] {
		if msg.Status != llm.ToolStatusError || !strings.Contains(msg.Text(), "multiple times in parallel") {
			t.Fatalf("tool message = %+v", msg)
		}
	}
	if _, ok := st.Values[TodosStateKey]; ok {
		t.Fatal("todos must not be set")
	}
}

func TestSystemPromptIsAppended(t *testing.T) {
	_, model := run(t)
	sys := model.Requests()[0].Messages[0]
	if sys.Role != llm.RoleSystem || !strings.Contains(sys.Text(), "## `write_todos`") {
		t.Fatalf("system message = %+v", sys)
	}
}
