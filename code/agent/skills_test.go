package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	core "github.com/tsengmingchun/deepagents_golang/agent"
	"github.com/tsengmingchun/deepagents_golang/backend"
	"github.com/tsengmingchun/deepagents_golang/llm"
	rt "github.com/tsengmingchun/deepagents_golang/runtime"
)

func writeSkill(t *testing.T, dir, name, desc string) string {
	t.Helper()
	p := filepath.Join(dir, name, "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: " + name + "\ndescription: " + desc + "\n---\n# " + name + " instructions\n"
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestSkillSourcesOrder(t *testing.T) {
	got := SkillSources("/home/u/.deepagents", "")
	want := []string{
		"/home/u/.deepagents/agent/skills",
		"/home/u/.agents/skills",
		"/.deepagents/skills",
		"/.agents/skills",
	}
	if len(got) != len(want) {
		t.Fatalf("sources = %v", got)
	}
	for i, s := range got {
		if s.Path != want[i] {
			t.Errorf("source %d = %q, want %q", i, s.Path, want[i])
		}
	}
	if n := len(SkillSources("", "")); n != 2 {
		t.Fatalf("without a user dir only project sources remain, got %d", n)
	}
}

func TestHostRoutesAreReadOnly(t *testing.T) {
	root := t.TempDir()
	skill := writeSkill(t, root, "review", "Review code")
	b := withHostRoutes(NewBackend(t.TempDir()), []string{root})
	ctx := context.Background()

	if r := b.Read(ctx, skill, 0, 100); r.Error != "" || r.FileData == nil || !strings.Contains(r.FileData.Content, "review instructions") {
		t.Fatalf("read = %+v", r)
	}
	if r := b.Write(ctx, filepath.Join(root, "new.md"), "x"); !strings.Contains(r.Error, "read-only") {
		t.Fatalf("write = %+v", r)
	}
	if r := b.Edit(ctx, skill, "review", "x", false); !strings.Contains(r.Error, "read-only") {
		t.Fatalf("edit = %+v", r)
	}
	d, ok := b.(backend.Deleter)
	if !ok {
		t.Fatal("routing dropped the Deleter interface")
	}
	if r := d.Delete(ctx, skill); !strings.Contains(r.Error, "read-only") {
		t.Fatalf("delete = %+v", r)
	}
	if _, ok := b.(backend.SandboxBackend); !ok {
		t.Fatal("routing dropped the SandboxBackend interface")
	}
	if r := b.Write(ctx, "/project.txt", "ok"); r.Error != "" {
		t.Fatalf("project write = %+v", r)
	}
}

func TestCLIAgentListsAndReadsUserAndProjectSkills(t *testing.T) {
	home := t.TempDir()
	userDir := filepath.Join(home, ".deepagents")
	userSkill := writeSkill(t, filepath.Join(userDir, DefaultAgentName, "skills"), "user-skill", "From the user")
	cwd := t.TempDir()
	writeSkill(t, filepath.Join(cwd, ".agents", "skills"), "project-skill", "From the project")

	model := llm.NewFakeModel(
		llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "r", Name: "read_file", Args: map[string]any{"file_path": userSkill}}}},
		llm.NewAssistantMessage("done"),
	)
	a, err := CreateCLIAgent(context.Background(), Options{Model: model, Cwd: cwd, UserDir: userDir, AutoApprove: true, ProjectContext: "-"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Invoke(context.Background(), core.Input{Messages: []llm.Message{llm.NewUserMessage("go")}}, rt.RunConfig{}); err != nil {
		t.Fatal(err)
	}
	sys := model.Requests()[0].Messages[0].Text()
	for _, want := range []string{"**user-skill**: From the user", "**project-skill**: From the project", "/.agents/skills/project-skill/SKILL.md"} {
		if !strings.Contains(sys, want) {
			t.Errorf("system prompt missing %q", want)
		}
	}
	if strings.Contains(sys, "skill_load_warnings") || strings.Contains(sys, "/.deepagents/skills") {
		t.Error("missing skill directories must be skipped, not warned about")
	}
	msgs := model.Requests()[1].Messages
	if got := msgs[len(msgs)-1].Text(); !strings.Contains(got, "user-skill instructions") {
		t.Fatalf("read_file of user skill = %q", got)
	}
}
