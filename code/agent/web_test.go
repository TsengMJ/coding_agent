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

func names(ts []tool.Tool) []string {
	var out []string
	for _, t := range ts {
		out = append(out, t.Name())
	}
	return out
}

func TestWebToolsNeedBraveKeyForSearch(t *testing.T) {
	if got := names(WebTools(nil)); !slices.Equal(got, []string{"fetch_url"}) {
		t.Errorf("without key = %v", got)
	}
	if got := names(WebTools(&config.Settings{WebSearchAPIKey: "k"})); !slices.Equal(got, []string{"web_search", "fetch_url"}) {
		t.Errorf("with key = %v", got)
	}
}

func TestCLIAgentOffersWebTools(t *testing.T) {
	model := llm.NewFakeModel(llm.NewAssistantMessage("ok"))
	a, err := CreateCLIAgent(context.Background(), Options{
		Model: model, Cwd: t.TempDir(), Settings: &config.Settings{WebSearchAPIKey: "k"}, AutoApprove: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Invoke(context.Background(), core.Input{Messages: []llm.Message{llm.NewUserMessage("hi")}}, rt.RunConfig{}); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, spec := range model.Requests()[0].Tools {
		got = append(got, spec.Name)
	}
	for _, want := range []string{"web_search", "fetch_url"} {
		if !slices.Contains(got, want) {
			t.Errorf("tools %v missing %s", got, want)
		}
	}
	prompt := model.Requests()[0].Messages[0].Text()
	if !strings.Contains(prompt, "### Web Search Tool Usage") {
		t.Error("prompt missing web search guidance")
	}
}

func TestNoWebToolsRemovesWebTools(t *testing.T) {
	model := llm.NewFakeModel(llm.NewAssistantMessage("ok"))
	a, err := CreateCLIAgent(context.Background(), Options{
		Model: model, Cwd: t.TempDir(), Settings: &config.Settings{WebSearchAPIKey: "k"}, AutoApprove: true,
		NoWebTools: true, WorkingDirSection: "### Current Working Directory\n\nThe project root is `/app`.\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Invoke(context.Background(), core.Input{Messages: []llm.Message{llm.NewUserMessage("hi")}}, rt.RunConfig{}); err != nil {
		t.Fatal(err)
	}
	for _, spec := range model.Requests()[0].Tools {
		if spec.Name == "web_search" || spec.Name == "fetch_url" {
			t.Errorf("offline agent offers %s", spec.Name)
		}
	}
	prompt := model.Requests()[0].Messages[0].Text()
	if strings.Contains(prompt, "Web Search Tool Usage") {
		t.Error("offline prompt has web search guidance")
	}
	if !strings.Contains(prompt, "The project root is `/app`.") || strings.Contains(prompt, "use virtual absolute paths") {
		t.Error("WorkingDirSection should replace the default section")
	}
}

func TestSystemPromptWebSearchSectionOnlyWithKey(t *testing.T) {
	without, _ := SystemPrompt(Options{Cwd: "/w"})
	if strings.Contains(without, "Web Search Tool Usage") {
		t.Error("guidance must be absent without a key")
	}
	interactive, _ := SystemPrompt(Options{Cwd: "/w", Interactive: true, Settings: &config.Settings{WebSearchAPIKey: "k"}})
	for _, want := range []string{"Web Search Tool Usage", "and ask clarifying questions", "`freshness`", "`result_filter`", "`offset`", "`summary: true`"} {
		if !strings.Contains(interactive, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
	headless, _ := SystemPrompt(Options{Cwd: "/w", Settings: &config.Settings{WebSearchAPIKey: "k"}})
	if strings.Contains(headless, "found and ask clarifying questions") {
		t.Error("headless prompt must not ask clarifying questions")
	}
}

func TestWebToolsRequireApproval(t *testing.T) {
	cfg := DefaultInterruptOn()
	for _, name := range []string{"web_search", "fetch_url"} {
		if c, ok := cfg[name]; !ok || c.Describe == nil {
			t.Errorf("%s must be reviewed with a per-call description", name)
		}
	}
}

func TestFormatWebSearchDescriptionListsNonDefaultArgs(t *testing.T) {
	got := FormatWebSearchDescription(llm.ToolCall{Args: map[string]any{
		"query": "go 1.27", "count": float64(5), "freshness": "pw", "country": "DE",
		"goggles": []any{"a", "b"}, "location": map[string]any{"city": "Berlin"}, "summary": true,
	}})
	for _, want := range []string{"Query: go 1.27\nMax results: 5\n", "country: DE\n", "freshness: pw\n", "goggles: a, b\n",
		"location: city=Berlin\n", "Brave Search API credits (plus follow-up"} {
		if !strings.Contains(got, want) {
			t.Errorf("description missing %q:\n%s", want, got)
		}
	}
	plain := FormatWebSearchDescription(llm.ToolCall{Args: map[string]any{"query": "x"}})
	if want := "Query: x\nMax results: Brave default (20)\n\n⚠  This will use Brave Search API credits"; plain != want {
		t.Errorf("plain = %q", plain)
	}
}

func TestFormatFetchURLDescriptionWarns(t *testing.T) {
	got := FormatFetchURLDescription(llm.ToolCall{Args: map[string]any{"url": "https://xn--80ak6aa92e.com/\u200bpath"}})
	for _, want := range []string{"URL: https://xn--80ak6aa92e.com/path\nTimeout: 30s", "hidden Unicode characters", "Decoded domain: "} {
		if !strings.Contains(got, want) {
			t.Errorf("description missing %q:\n%s", want, got)
		}
	}
	if got := FormatFetchURLDescription(llm.ToolCall{Args: map[string]any{"url": "https://go.dev", "timeout": float64(10)}}); strings.Contains(got, "warning") || !strings.Contains(got, "Timeout: 10s") {
		t.Errorf("plain = %q", got)
	}
}
