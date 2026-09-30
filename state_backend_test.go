package deepagents

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/tsengmingchun/deepagents_golang/agent"
	"github.com/tsengmingchun/deepagents_golang/backend"
	"github.com/tsengmingchun/deepagents_golang/llm"
	"github.com/tsengmingchun/deepagents_golang/runtime"
)

func toolCalls(calls ...llm.ToolCall) llm.Message {
	return llm.Message{Role: llm.RoleAssistant, ToolCalls: calls}
}

func stateFiles(t *testing.T, st *runtime.State) map[string]backend.FileData {
	t.Helper()
	files, ok := st.Values[backend.FilesStateKey].(map[string]backend.FileData)
	if !ok {
		t.Fatalf("files state = %T %v", st.Values[backend.FilesStateKey], st.Values[backend.FilesStateKey])
	}
	return files
}

func toolText(st *runtime.State, id string) string {
	for _, m := range st.Messages {
		if m.Role == llm.RoleTool && m.ToolCallID == id {
			return m.Text()
		}
	}
	return ""
}

func TestCreateDeepAgentDefaultsToStateBackend(t *testing.T) {
	model := llm.NewFakeModel(
		toolCalls(llm.ToolCall{ID: "w", Name: "write_file", Args: map[string]any{"file_path": "/notes/a.md", "content": "hello world\n"}}),
		toolCalls(llm.ToolCall{ID: "e", Name: "edit_file", Args: map[string]any{"file_path": "/notes/a.md", "old_string": "world", "new_string": "state"}}),
		toolCalls(llm.ToolCall{ID: "r", Name: "read_file", Args: map[string]any{"file_path": "/notes/a.md"}}),
		toolCalls(llm.ToolCall{ID: "w2", Name: "write_file", Args: map[string]any{"file_path": "/tmp/x", "content": "x"}}),
		toolCalls(llm.ToolCall{ID: "d", Name: "delete", Args: map[string]any{"file_path": "/tmp"}}),
		llm.NewAssistantMessage("done"),
	)
	a, err := CreateDeepAgent(WithModel(model))
	if err != nil {
		t.Fatal(err)
	}
	st, err := a.Invoke(context.Background(), agent.Input{Messages: []llm.Message{llm.NewUserMessage("go")}}, runtime.RunConfig{})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, spec := range model.Requests()[0].Tools {
		names = append(names, spec.Name)
	}
	if want := []string{"ls", "read_file", "write_file", "edit_file", "delete", "glob", "grep", "task"}; !slices.Equal(names, want) {
		t.Fatalf("tools = %v, want %v", names, want)
	}
	if got := toolText(st, "r"); !strings.Contains(got, "hello state") {
		t.Fatalf("read_file result = %q", got)
	}
	files := stateFiles(t, st)
	if len(files) != 1 || files["/notes/a.md"].Content != "hello state\n" {
		t.Fatalf("files = %+v", files)
	}
}

func TestStateFilesPersistAcrossInvokes(t *testing.T) {
	model := llm.NewFakeModel(
		toolCalls(llm.ToolCall{ID: "w", Name: "write_file", Args: map[string]any{"file_path": "/a.txt", "content": "remember me"}}),
		llm.NewAssistantMessage("written"),
		toolCalls(llm.ToolCall{ID: "r", Name: "read_file", Args: map[string]any{"file_path": "/a.txt"}}),
		llm.NewAssistantMessage("read"),
	)
	a, err := CreateDeepAgent(WithModel(model), WithCheckpointer(runtime.NewMemoryCheckpointer()))
	if err != nil {
		t.Fatal(err)
	}
	cfg := runtime.RunConfig{ThreadID: "t1"}
	if _, err := a.Invoke(context.Background(), agent.Input{Messages: []llm.Message{llm.NewUserMessage("write")}}, cfg); err != nil {
		t.Fatal(err)
	}
	st, err := a.Invoke(context.Background(), agent.Input{Messages: []llm.Message{llm.NewUserMessage("read")}}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got := toolText(st, "r"); !strings.Contains(got, "remember me") {
		t.Fatalf("read_file result = %q", got)
	}
}

func TestParallelStateWritesToDifferentFilesAreAllKept(t *testing.T) {
	model := llm.NewFakeModel(
		toolCalls(
			llm.ToolCall{ID: "a", Name: "write_file", Args: map[string]any{"file_path": "/a", "content": "A"}},
			llm.ToolCall{ID: "b", Name: "write_file", Args: map[string]any{"file_path": "/b", "content": "B"}},
			llm.ToolCall{ID: "c", Name: "write_file", Args: map[string]any{"file_path": "/c", "content": "C"}},
		),
		llm.NewAssistantMessage("done"),
	)
	a, err := CreateDeepAgent(WithModel(model))
	if err != nil {
		t.Fatal(err)
	}
	st, err := a.Invoke(context.Background(), agent.Input{Messages: []llm.Message{llm.NewUserMessage("go")}}, runtime.RunConfig{})
	if err != nil {
		t.Fatal(err)
	}
	files := stateFiles(t, st)
	if len(files) != 3 || files["/a"].Content != "A" || files["/b"].Content != "B" || files["/c"].Content != "C" {
		t.Fatalf("files = %+v", files)
	}
}

func TestPrepopulatedStateFilesAreReadable(t *testing.T) {
	model := llm.NewFakeModel(
		toolCalls(llm.ToolCall{ID: "r", Name: "read_file", Args: map[string]any{"file_path": "/seed.txt"}}),
		llm.NewAssistantMessage("done"),
	)
	a, err := CreateDeepAgent(WithModel(model))
	if err != nil {
		t.Fatal(err)
	}
	seed, _ := backend.CreateFileData("seeded", backend.EncodingUTF8)
	in := agent.Input{
		Messages: []llm.Message{llm.NewUserMessage("go")},
		Values:   map[string]any{backend.FilesStateKey: map[string]backend.FileData{"/seed.txt": seed}},
	}
	st, err := a.Invoke(context.Background(), in, runtime.RunConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if got := toolText(st, "r"); !strings.Contains(got, "seeded") {
		t.Fatalf("read_file result = %q", got)
	}
}

func TestSubagentStateFilesReachParent(t *testing.T) {
	model := llm.NewFakeModel(
		toolCalls(llm.ToolCall{ID: "t", Name: "task", Args: map[string]any{"description": "write the report", "subagent_type": "general-purpose"}}),
		toolCalls(llm.ToolCall{ID: "sw", Name: "write_file", Args: map[string]any{"file_path": "/report.md", "content": "from subagent"}}),
		llm.NewAssistantMessage("report written"),
		toolCalls(llm.ToolCall{ID: "r", Name: "read_file", Args: map[string]any{"file_path": "/report.md"}}),
		llm.NewAssistantMessage("done"),
	)
	a, err := CreateDeepAgent(WithModel(model))
	if err != nil {
		t.Fatal(err)
	}
	st, err := a.Invoke(context.Background(), agent.Input{Messages: []llm.Message{llm.NewUserMessage("go")}}, runtime.RunConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if got := toolText(st, "r"); !strings.Contains(got, "from subagent") {
		t.Fatalf("parent read_file result = %q", got)
	}
}
