package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	core "github.com/tsengmingchun/deepagents_golang/agent"
	"github.com/tsengmingchun/deepagents_golang/llm"
	rt "github.com/tsengmingchun/deepagents_golang/runtime"
)

func TestLocalContextInGitRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-q", "-b", "trunk"}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := LocalContext(context.Background(), dir)
	for _, want := range []string{"### Project Context", "Git branch: `trunk`", "Uncommitted changes (1):", "?? go.mod", "Project files: go.mod"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestLocalContextOutsideGit(t *testing.T) {
	dir := t.TempDir()
	if got := LocalContext(context.Background(), dir); got != "" {
		t.Fatalf("empty non-git dir = %q", got)
	}
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := LocalContext(context.Background(), dir)
	if strings.Contains(got, "Git branch") || !strings.Contains(got, "Project files: package.json") {
		t.Fatalf("got:\n%s", got)
	}
}

func TestCachedAgentRefreshesProjectContext(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	if out, err := exec.Command("git", "-C", dir, "init", "-q", "-b", "trunk").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	model := llm.NewFakeModel(llm.NewAssistantMessage("one"), llm.NewAssistantMessage("two"))
	a, err := CreateCLIAgent(context.Background(), Options{Model: model, Cwd: dir, AutoApprove: true, NoWebTools: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, question := range []string{"first", "second"} {
		if question == "second" {
			if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if out, err := exec.Command("git", "-C", dir, "switch", "-q", "-c", "feature").CombinedOutput(); err != nil {
				t.Fatalf("git switch: %v: %s", err, out)
			}
		}
		if _, err := a.Invoke(context.Background(), core.Input{Messages: []llm.Message{llm.NewUserMessage(question)}}, rt.RunConfig{}); err != nil {
			t.Fatal(err)
		}
	}
	first, second := model.Requests()[0].Messages[0].Text(), model.Requests()[1].Messages[0].Text()
	if !strings.Contains(first, "Git branch: `trunk`") || strings.Contains(first, "Project files: go.mod") {
		t.Fatalf("first context has wrong project snapshot")
	}
	for _, want := range []string{"Git branch: `feature`", "?? go.mod", "Project files: go.mod"} {
		if !strings.Contains(second, want) {
			t.Fatalf("second context missing %q: %q", want, second)
		}
	}
}

func TestLiveContextUsesExplicitOverrideAndDropsUnavailableGit(t *testing.T) {
	dir := t.TempDir()
	mw := &liveContext{cwd: dir, model: llm.NewFakeModel(), project: "fixed project"}
	req := &core.ModelRequest{}
	var got string
	next := func(_ context.Context, r *core.ModelRequest) (*core.ModelResponse, error) {
		got = r.SystemMessage.Text()
		return &core.ModelResponse{}, nil
	}
	if _, err := mw.WrapModelCall(context.Background(), req, next); err != nil || !strings.Contains(got, "fixed project") {
		t.Fatalf("override = %q, %v", got, err)
	}
	mw.project = ""
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _ = mw.WrapModelCall(context.Background(), req, next)
	if !strings.Contains(got, "go.mod") {
		t.Fatalf("marker missing: %q", got)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	_, _ = mw.WrapModelCall(context.Background(), req, next)
	if strings.Contains(got, "go.mod") {
		t.Fatalf("stale context remained: %q", got)
	}
}

func TestCLIAgentLoadsMemoryAndTodos(t *testing.T) {
	cwd, userDir := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "AGENTS.md"), []byte("project rule"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(userDir, DefaultAgentName), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(userDir, DefaultAgentName, "AGENTS.md"), []byte("user pref"), 0o600); err != nil {
		t.Fatal(err)
	}
	model := llm.NewFakeModel(llm.NewAssistantMessage("ok"))
	a, err := CreateCLIAgent(context.Background(), Options{Model: model, Cwd: cwd, UserDir: userDir, AutoApprove: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Invoke(context.Background(), core.Input{Messages: []llm.Message{llm.NewUserMessage("hi")}}, rt.RunConfig{}); err != nil {
		t.Fatal(err)
	}
	req := model.Requests()[0]
	sys := req.Messages[0].Text()
	for _, want := range []string{"/AGENTS.md\n\nproject rule", "user pref", "## `write_todos`", "Project files: AGENTS.md"} {
		if !strings.Contains(sys, want) {
			t.Errorf("system prompt missing %q", want)
		}
	}
	found := false
	for _, spec := range req.Tools {
		found = found || spec.Name == "write_todos"
	}
	if !found {
		t.Error("write_todos tool not offered")
	}
}

func TestCLISubagentWritesWithoutApprovalInManualMode(t *testing.T) {
	cwd := t.TempDir()
	model := llm.NewFakeModel(
		llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "t1", Name: "task",
			Args: map[string]any{"subagent_type": "general-purpose", "description": "write notes.txt"}}}},
		llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "w1", Name: "write_file",
			Args: map[string]any{"file_path": "/notes.txt", "content": "hi"}}}},
		llm.NewAssistantMessage("wrote notes.txt"),
		llm.NewAssistantMessage("done"),
	)
	a, err := CreateCLIAgent(context.Background(), Options{Model: model, Cwd: cwd})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Invoke(context.Background(), core.Input{Messages: []llm.Message{llm.NewUserMessage("go")}}, rt.RunConfig{}); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(cwd, "notes.txt")); err != nil || string(got) != "hi" {
		t.Fatalf("notes.txt = %q, %v", got, err)
	}
}
