package agent

import (
	"context"
	"slices"
	"strings"
	"testing"

	core "github.com/tsengmingchun/deepagents_golang/agent"
	"github.com/tsengmingchun/deepagents_golang/code/config"
	"github.com/tsengmingchun/deepagents_golang/llm"
	rt "github.com/tsengmingchun/deepagents_golang/runtime"
	"github.com/tsengmingchun/deepagents_golang/tool"
)

func countingTool(name string, calls *int) tool.Tool {
	return &tool.Func{ToolName: name, ToolSchema: tool.Schema{"type": "object"}, Fn: func(context.Context, *tool.Runtime, map[string]any) (*tool.Result, error) {
		*calls++
		return &tool.Result{Content: []llm.ContentBlock{llm.TextBlock("ok")}}, nil
	}}
}

func TestResearchLimitsHideAndRefuseOtherTools(t *testing.T) {
	var writes, reads int
	model := llm.NewFakeModel(
		llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "w", Name: "write_file", Args: map[string]any{}}}},
		llm.NewAssistantMessage("report"),
	)
	a, err := core.CreateAgent(core.Config{
		Model:      model,
		Tools:      []tool.Tool{countingTool("write_file", &writes), countingTool("read_file", &reads)},
		Middleware: []core.Middleware{researchLimits{allowed: []string{"read_file"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	st, err := a.Invoke(context.Background(), core.Input{Messages: []llm.Message{llm.NewUserMessage("go")}}, rt.RunConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if names := toolNames(model.Requests()[0]); !slices.Equal(names, []string{"read_file"}) {
		t.Fatalf("offered tools = %v", names)
	}
	if writes != 0 || st.Messages[2].Status != llm.ToolStatusError {
		t.Fatalf("write ran or was not refused: %+v", st.Messages[2])
	}
}

func TestResearchLimitsCapCallsPerTool(t *testing.T) {
	var fetches int
	call := func(id string) llm.Message {
		return llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: id, Name: "fetch_url", Args: map[string]any{}}}}
	}
	model := llm.NewFakeModel(call("1"), call("2"), call("3"), llm.NewAssistantMessage("report"))
	a, err := core.CreateAgent(core.Config{
		Model:      model,
		Tools:      []tool.Tool{countingTool("fetch_url", &fetches)},
		Middleware: []core.Middleware{researchLimits{allowed: []string{"fetch_url"}, max: map[string]int{"fetch_url": 2}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	st, err := a.Invoke(context.Background(), core.Input{Messages: []llm.Message{llm.NewUserMessage("go")}}, rt.RunConfig{})
	if err != nil {
		t.Fatal(err)
	}
	last := st.Messages[len(st.Messages)-2]
	if fetches != 2 || last.Status != llm.ToolStatusError || !strings.Contains(last.Text(), "limit") {
		t.Fatalf("fetches = %d, last tool message = %+v", fetches, last)
	}
}

func toolNames(req llm.ChatRequest) []string {
	var out []string
	for _, s := range req.Tools {
		out = append(out, s.Name)
	}
	return out
}

func TestWebResearcherOnlyWithSearchOrDocs(t *testing.T) {
	cases := map[string]struct {
		opts Options
		want bool
	}{
		"no keys":  {Options{}, false},
		"brave":    {Options{Settings: &config.Settings{WebSearchAPIKey: "k"}}, true},
		"context7": {Options{Settings: &config.Settings{Context7APIKey: "k"}}, true},
		"no web":   {Options{Settings: &config.Settings{WebSearchAPIKey: "k"}, NoWebTools: true}, false},
	}
	for name, tc := range cases {
		if got := hasWebResearcher(tc.opts); got != tc.want {
			t.Errorf("%s: %v", name, got)
		}
	}
}

func TestCLIAgentOffersWebResearcher(t *testing.T) {
	model := llm.NewFakeModel(llm.NewAssistantMessage("hi"))
	a, err := CreateCLIAgent(context.Background(), Options{Model: model, Cwd: t.TempDir(), AutoApprove: true,
		Settings: &config.Settings{WebSearchAPIKey: "k"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Invoke(context.Background(), core.Input{Messages: []llm.Message{llm.NewUserMessage("hi")}}, rt.RunConfig{}); err != nil {
		t.Fatal(err)
	}
	var task string
	for _, s := range model.Requests()[0].Tools {
		if s.Name == "task" {
			task = s.Description
		}
	}
	if !strings.Contains(task, "- "+WebResearcherName+":") {
		t.Fatalf("task description lacks the researcher:\n%s", task)
	}
}

func TestCLIAgentOffersBackgroundTasks(t *testing.T) {
	model := llm.NewFakeModel(llm.NewAssistantMessage("hi"))
	a, err := CreateCLIAgent(context.Background(), Options{Model: model, Cwd: t.TempDir(), AutoApprove: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Invoke(context.Background(), core.Input{Messages: []llm.Message{llm.NewUserMessage("hi")}}, rt.RunConfig{}); err != nil {
		t.Fatal(err)
	}
	if names := toolNames(model.Requests()[0]); !slices.Contains(names, "task_output") {
		t.Fatalf("tools = %v", names)
	}
}

func TestWebResearcherTaskNeedsApproval(t *testing.T) {
	cfg, ok := DefaultInterruptOn()["task"]
	if !ok || cfg.When == nil || cfg.Describe == nil {
		t.Fatal("task must be reviewed when it starts the web researcher")
	}
	research := llm.ToolCall{Name: "task", Args: map[string]any{"subagent_type": WebResearcherName, "description": "latest pgx release", "run_in_background": true}}
	if !cfg.When(research) || cfg.When(llm.ToolCall{Name: "task", Args: map[string]any{"subagent_type": "general-purpose"}}) {
		t.Fatal("only web researcher tasks are reviewed")
	}
	if got := cfg.Describe(research); !strings.Contains(got, "latest pgx release") || !strings.Contains(got, "background") {
		t.Fatalf("description = %q", got)
	}
}
