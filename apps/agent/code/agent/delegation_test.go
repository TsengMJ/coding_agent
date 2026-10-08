package agent

import (
	"context"
	"slices"
	"strings"
	"testing"

	core "github.com/tsengmingchun/quill/apps/agent/agent"
	"github.com/tsengmingchun/quill/apps/agent/code/config"
	"github.com/tsengmingchun/quill/apps/agent/llm"
	rt "github.com/tsengmingchun/quill/apps/agent/runtime"
)

func TestSystemPromptExplainsDelegation(t *testing.T) {
	prompt, err := SystemPrompt(Options{Cwd: "/w"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"## Subagents", "`" + ExplorerName + "`", "`general-purpose`", "Do it yourself when", "complete brief"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt lacks %q", want)
		}
	}
	if strings.Contains(prompt, WebResearcherName) {
		t.Error("prompt without web keys must not offer the web researcher")
	}
}

func TestSystemPromptDelegatesResearchWithWebKeys(t *testing.T) {
	prompt, _ := SystemPrompt(Options{Cwd: "/w", Settings: &config.Settings{WebSearchAPIKey: "k"}})
	section := prompt[strings.Index(prompt, "## Subagents"):strings.Index(prompt, "Do it yourself when")]
	if !strings.Contains(section, "`"+WebResearcherName+"`") {
		t.Fatalf("subagents section lacks the researcher:\n%s", section)
	}
}

func TestExplorerOnlyReads(t *testing.T) {
	model := llm.NewFakeModel(
		llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "t1", Name: "task",
			Args: map[string]any{"subagent_type": ExplorerName, "description": "find main"}}}},
		llm.NewAssistantMessage("main is in /main.go"),
		llm.NewAssistantMessage("done"),
	)
	a, err := CreateCLIAgent(context.Background(), Options{Model: model, Cwd: t.TempDir(), AutoApprove: true, NoWebTools: true, NoVerifyGate: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Invoke(context.Background(), core.Input{Messages: []llm.Message{llm.NewUserMessage("where is main?")}}, rt.RunConfig{}); err != nil {
		t.Fatal(err)
	}
	reqs := model.Requests()
	var task string
	for _, s := range reqs[0].Tools {
		if s.Name == "task" {
			task = s.Description
		}
	}
	if !strings.Contains(task, "- "+ExplorerName+":") {
		t.Fatalf("task description lacks the explorer:\n%s", task)
	}
	got := toolNames(reqs[1])
	slices.Sort(got)
	want := slices.Clone(explorerReadOnly)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("explorer tools = %v, want %v", got, want)
	}
}
