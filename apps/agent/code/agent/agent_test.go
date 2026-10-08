package agent

import (
	"context"
	"regexp"
	"slices"
	"strings"
	"testing"

	core "github.com/tsengmingchun/quill/apps/agent/agent"
	"github.com/tsengmingchun/quill/apps/agent/code/codetools"
	"github.com/tsengmingchun/quill/apps/agent/llm"
	"github.com/tsengmingchun/quill/apps/agent/middleware/planmode"
	rt "github.com/tsengmingchun/quill/apps/agent/runtime"
)

func TestSystemPromptHeadless(t *testing.T) {
	got, err := SystemPrompt(Options{Cwd: "/work/proj", Model: llm.NewFakeModel()})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"non-interactive (headless) mode", "The project root is `/work/proj`", "You are running as model `fake` (provider: fake)"} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
	if left := regexp.MustCompile(`\{[a-z_]+\}`).FindAllString(got, -1); len(left) > 0 {
		t.Fatalf("unreplaced placeholders: %v", left)
	}
}

func TestSystemPromptInteractive(t *testing.T) {
	got, _ := SystemPrompt(Options{Cwd: "/w", Interactive: true})
	if !strings.Contains(got, "an interactive TUI") {
		t.Fatal("interactive prompt missing mode description")
	}
}

func TestCLIAgentOffersCodingTools(t *testing.T) {
	model := llm.NewFakeModel(llm.NewAssistantMessage("ok"))
	a, err := CreateCLIAgent(context.Background(), Options{Model: model, Cwd: t.TempDir(), AutoApprove: true, NoWebTools: true})
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
	for _, want := range []string{codetools.ApplyPatchName, codetools.GitStatusName, codetools.GitDiffName, codetools.RunTestsName, codetools.DiagnosticsName} {
		if !slices.Contains(got, want) {
			t.Errorf("tools %v missing %s", got, want)
		}
	}
}

func TestCLIAgentVerifyGateOnlyWhenHeadless(t *testing.T) {
	cases := map[string]struct {
		opts      Options
		wantCalls int
	}{
		"headless":    {Options{}, 4},
		"interactive": {Options{Interactive: true, Plans: planmode.NewMemoryStore()}, 2},
		"disabled":    {Options{NoVerifyGate: true}, 2},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			model := llm.NewFakeModel(
				llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "w", Name: "write_file",
					Args: map[string]any{"file_path": "/x.txt", "content": "hi\n"}}}},
				llm.NewAssistantMessage("done"),
				llm.NewAssistantMessage("done"),
				llm.NewAssistantMessage("done"),
			)
			opts := tc.opts
			opts.Model, opts.Cwd, opts.AutoApprove, opts.NoWebTools = model, t.TempDir(), true, true
			a, err := CreateCLIAgent(context.Background(), opts)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := a.Invoke(context.Background(), core.Input{Messages: []llm.Message{llm.NewUserMessage("write x")}}, rt.RunConfig{}); err != nil {
				t.Fatal(err)
			}
			if got := len(model.Requests()); got != tc.wantCalls {
				t.Errorf("model calls = %d, want %d", got, tc.wantCalls)
			}
		})
	}
}

func TestCreateCLIAgentRequiresCwd(t *testing.T) {
	if _, err := CreateCLIAgent(context.Background(), Options{Model: llm.NewFakeModel()}); err == nil {
		t.Fatal("CreateCLIAgent without Cwd must fail")
	}
}
