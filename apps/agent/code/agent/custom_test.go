package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	core "github.com/tsengmingchun/quill/apps/agent/agent"
	codesubagents "github.com/tsengmingchun/quill/apps/agent/code/subagents"
	"github.com/tsengmingchun/quill/apps/agent/llm"
	rt "github.com/tsengmingchun/quill/apps/agent/runtime"
)

func writeCustomAgent(t *testing.T, dir, folder, content string) {
	t.Helper()
	path := filepath.Join(dir, folder, codesubagents.FileName)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCustomSubagentsAreOffered(t *testing.T) {
	cwd, home := t.TempDir(), t.TempDir()
	writeCustomAgent(t, codesubagents.ProjectDir(cwd), "reviewer", "---\ndescription: Reviews diffs\n---\nYou review code.")
	writeCustomAgent(t, codesubagents.UserDir(home, DefaultAgentName), "writer", "---\ndescription: Writes docs\n---\nYou write.")
	model := llm.NewFakeModel(
		llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "t1", Name: "task",
			Args: map[string]any{"subagent_type": "reviewer", "description": "review the diff"}}}},
		llm.NewAssistantMessage("looks good"),
		llm.NewAssistantMessage("done"),
	)
	a, err := CreateCLIAgent(context.Background(), Options{Model: model, Cwd: cwd, UserDir: home, AutoApprove: true, NoWebTools: true, NoVerifyGate: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Invoke(context.Background(), core.Input{Messages: []llm.Message{llm.NewUserMessage("review")}}, rt.RunConfig{}); err != nil {
		t.Fatal(err)
	}
	reqs := model.Requests()
	var task string
	for _, s := range reqs[0].Tools {
		if s.Name == "task" {
			task = s.Description
		}
	}
	for _, want := range []string{"- reviewer: Reviews diffs", "- writer: Writes docs"} {
		if !strings.Contains(task, want) {
			t.Fatalf("task description lacks %q:\n%s", want, task)
		}
	}
	if sys := reqs[1].Messages[0].Text(); !strings.Contains(sys, "You review code.") {
		t.Fatalf("subagent system prompt:\n%s", sys)
	}
	if tools := toolNames(reqs[1]); !strings.Contains(strings.Join(tools, ","), "edit_file") {
		t.Fatalf("custom subagent must inherit the main tools, got %v", tools)
	}
}

func TestCustomSubagentCannotShadowBuiltin(t *testing.T) {
	cwd := t.TempDir()
	writeCustomAgent(t, codesubagents.ProjectDir(cwd), ExplorerName, "---\ndescription: mine\n---\nx")
	_, err := CreateCLIAgent(context.Background(), Options{Model: llm.NewFakeModel(), Cwd: cwd, AutoApprove: true, NoWebTools: true})
	if err == nil || !strings.Contains(err.Error(), "built-in") {
		t.Fatalf("err = %v", err)
	}
}
