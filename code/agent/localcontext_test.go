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
	"github.com/tsengmingchun/deepagents_golang/middleware/planmode"
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
	reqs := model.Requests()
	if reqs[0].Messages[0].Text() != reqs[1].Messages[0].Text() {
		t.Fatal("system message changed with the working tree; it must stay a cacheable prefix")
	}
	first, second := lastText(reqs[0]), lastText(reqs[1])
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
	req := &core.ModelRequest{Messages: []llm.Message{llm.NewUserMessage("hi")}}
	var got string
	next := func(_ context.Context, r *core.ModelRequest) (*core.ModelResponse, error) {
		got = r.SystemMessage.Text() + "\n" + r.Messages[len(r.Messages)-1].Text()
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

func lastText(r llm.ChatRequest) string { return r.Messages[len(r.Messages)-1].Text() }

func TestLiveContextLeavesHistoryAndSystemMessageUnchanged(t *testing.T) {
	dir := t.TempDir()
	mw := &liveContext{cwd: dir, model: llm.NewFakeModel()}
	history := []llm.Message{llm.NewUserMessage("hi")}
	req := &core.ModelRequest{Messages: history}
	var seen []llm.Message
	next := func(_ context.Context, r *core.ModelRequest) (*core.ModelResponse, error) {
		seen = r.Messages
		return &core.ModelResponse{}, nil
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := mw.WrapModelCall(context.Background(), req, next); err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 || len(seen) != 2 || !strings.Contains(seen[1].Text(), "Project files: go.mod") {
		t.Fatalf("history = %d messages, request = %+v", len(history), seen)
	}
	if !rt.IsUserTurn(seen[0]) || rt.IsUserTurn(seen[1]) {
		t.Fatal("the live context message must not count as a user turn")
	}
}

func TestSystemMessageIsStableWithinARun(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	model := llm.NewFakeModel(
		llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "t", Name: "write_todos",
			Args: map[string]any{"todos": []any{map[string]any{"content": "write a", "status": "in_progress"}}}}}},
		llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "w", Name: "write_file",
			Args: map[string]any{"file_path": "/a.txt", "content": "hi"}}}},
		llm.NewAssistantMessage("done"),
	)
	a, err := CreateCLIAgent(context.Background(), Options{Model: model, Cwd: dir, AutoApprove: true, NoWebTools: true, Interactive: true, Plans: planmode.NewMemoryStore()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Invoke(context.Background(), core.Input{Messages: []llm.Message{llm.NewUserMessage("go")}}, rt.RunConfig{}); err != nil {
		t.Fatal(err)
	}
	reqs := model.Requests()
	for i, r := range reqs[1:] {
		if r.Messages[0].Text() != reqs[0].Messages[0].Text() {
			t.Fatalf("system message of call %d differs from the first", i+2)
		}
	}
	if last := lastText(reqs[len(reqs)-1]); !strings.Contains(last, "?? a.txt") {
		t.Fatalf("live context should show the new file: %q", last)
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
	for _, want := range []string{"/AGENTS.md\n\nproject rule", "user pref", "## `write_todos`"} {
		if !strings.Contains(sys, want) {
			t.Errorf("system prompt missing %q", want)
		}
	}
	if !strings.Contains(lastText(req), "Project files: AGENTS.md") {
		t.Errorf("live context missing from the request tail: %q", lastText(req))
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
