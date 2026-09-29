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
