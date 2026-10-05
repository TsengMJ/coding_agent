package quill

import (
	"context"
	"strings"
	"testing"

	"github.com/tsengmingchun/quill/agent"
	"github.com/tsengmingchun/quill/backend"
	"github.com/tsengmingchun/quill/llm"
	"github.com/tsengmingchun/quill/middleware/subagents"
	"github.com/tsengmingchun/quill/runtime"
)

func skillFiles(t *testing.T, skills map[string]string) map[string]any {
	t.Helper()
	files := map[string]backend.FileData{}
	for path, desc := range skills {
		name := path[strings.LastIndex(path, "/")+1:]
		fd, err := backend.CreateFileData("---\nname: "+name+"\ndescription: "+desc+"\n---\n# body\n", backend.EncodingUTF8)
		if err != nil {
			t.Fatal(err)
		}
		files[path+"/SKILL.md"] = fd
	}
	return map[string]any{backend.FilesStateKey: files}
}

func TestWithSkillsListsSkillsInMainPrompt(t *testing.T) {
	model := llm.NewFakeModel(llm.NewAssistantMessage("done"))
	a, err := CreateAgent(WithModel(model), WithSkills("/skills/"))
	if err != nil {
		t.Fatal(err)
	}
	in := agent.Input{
		Messages: []llm.Message{llm.NewUserMessage("go")},
		Values:   skillFiles(t, map[string]string{"/skills/pdf-tools": "Work with PDFs"}),
	}
	if _, err := a.Invoke(context.Background(), in, runtime.RunConfig{}); err != nil {
		t.Fatal(err)
	}
	sys := model.Requests()[0].Messages[0].Text()
	for _, want := range []string{"**pdf-tools**: Work with PDFs", "/skills/pdf-tools/SKILL.md"} {
		if !strings.Contains(sys, want) {
			t.Errorf("system prompt missing %q:\n%s", want, sys)
		}
	}
}

func TestGeneralPurposeSubagentGetsMainSkills(t *testing.T) {
	model := llm.NewFakeModel(
		toolCalls(llm.ToolCall{ID: "t", Name: "task", Args: map[string]any{"description": "x", "subagent_type": "general-purpose"}}),
		llm.NewAssistantMessage("sub done"),
		llm.NewAssistantMessage("done"),
	)
	a, err := CreateAgent(WithModel(model), WithSkills("/skills/"))
	if err != nil {
		t.Fatal(err)
	}
	in := agent.Input{
		Messages: []llm.Message{llm.NewUserMessage("go")},
		Values:   skillFiles(t, map[string]string{"/skills/pdf-tools": "Work with PDFs"}),
	}
	if _, err := a.Invoke(context.Background(), in, runtime.RunConfig{}); err != nil {
		t.Fatal(err)
	}
	if sys := model.Requests()[1].Messages[0].Text(); !strings.Contains(sys, "**pdf-tools**") {
		t.Fatalf("general-purpose prompt missing skill:\n%s", sys)
	}
}

func TestDeclarativeSubagentUsesOwnSkills(t *testing.T) {
	model := llm.NewFakeModel(
		toolCalls(llm.ToolCall{ID: "t", Name: "task", Args: map[string]any{"description": "x", "subagent_type": "writer"}}),
		llm.NewAssistantMessage("sub done"),
		llm.NewAssistantMessage("done"),
	)
	a, err := CreateAgent(WithModel(model), WithSkills("/main/"),
		WithSubAgents(subagents.SubAgent{Name: "writer", Description: "writes", SystemPrompt: "write", Skills: []string{"/writer/"}}))
	if err != nil {
		t.Fatal(err)
	}
	in := agent.Input{
		Messages: []llm.Message{llm.NewUserMessage("go")},
		Values:   skillFiles(t, map[string]string{"/main/main-skill": "Main only", "/writer/style-guide": "Writing style"}),
	}
	if _, err := a.Invoke(context.Background(), in, runtime.RunConfig{}); err != nil {
		t.Fatal(err)
	}
	sys := model.Requests()[1].Messages[0].Text()
	if !strings.Contains(sys, "**style-guide**") || strings.Contains(sys, "main-skill") {
		t.Fatalf("writer prompt should list only its own skills:\n%s", sys)
	}
}
