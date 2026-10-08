package agent

import (
	"context"
	"errors"
	"regexp"
	"slices"
	"strings"
	"testing"

	core "github.com/tsengmingchun/quill/apps/agent/agent"
	"github.com/tsengmingchun/quill/apps/agent/code/codetools"
	"github.com/tsengmingchun/quill/apps/agent/code/process"
	"github.com/tsengmingchun/quill/apps/agent/llm"
	"github.com/tsengmingchun/quill/apps/agent/middleware/planmode"
	rt "github.com/tsengmingchun/quill/apps/agent/runtime"
	"github.com/tsengmingchun/quill/apps/agent/tool"
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
	if !strings.Contains(got, "an interactive desktop app") {
		t.Fatal("interactive prompt missing mode description")
	}
}

func TestSystemPromptDescribesTheBrowserPanel(t *testing.T) {
	got, _ := SystemPrompt(Options{Cwd: "/w", Interactive: true, BrowserMCP: "playwright"})
	if !strings.Contains(got, "### Browser Panel") || !strings.Contains(got, "localhost") {
		t.Fatalf("prompt without browser section:\n%s", got)
	}
	if plain, _ := SystemPrompt(Options{Cwd: "/w", Interactive: true}); strings.Contains(plain, "### Browser Panel") {
		t.Fatal("browser section without browser tools")
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

func TestCLIAgentSendsToolScreenshotsAsUserImages(t *testing.T) {
	shot := &tool.Func{ToolName: "mcp__playwright__browser_take_screenshot", ToolSchema: tool.Schema{"type": "object"},
		Fn: func(context.Context, *tool.Runtime, map[string]any) (*tool.Result, error) {
			return &tool.Result{Content: []llm.ContentBlock{llm.TextBlock("Took a screenshot"), {Type: llm.BlockImage, MimeType: "image/png", Base64: "iVBORw0K"}}}, nil
		}}
	model := llm.NewFakeModel(
		llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "s", Name: shot.ToolName, Args: map[string]any{}}}},
		llm.NewAssistantMessage("looks right"),
	)
	a, err := CreateCLIAgent(context.Background(), Options{Model: model, Cwd: t.TempDir(), AutoApprove: true, NoWebTools: true, NoVerifyGate: true, MCPTools: []tool.Tool{shot}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Invoke(context.Background(), core.Input{Messages: []llm.Message{llm.NewUserMessage("check the page")}}, rt.RunConfig{}); err != nil {
		t.Fatal(err)
	}
	msgs := model.Requests()[1].Messages
	last := msgs[len(msgs)-1]
	if last.Role != llm.RoleUser || !slices.ContainsFunc(last.Content, func(c llm.ContentBlock) bool { return c.Type == llm.BlockImage }) {
		t.Fatalf("last message = %+v, want the screenshot in a user message", last)
	}
}

func TestProcessToolsAreInteractiveAndReviewedLikeExecute(t *testing.T) {
	procs := process.NewManager()
	defer procs.Close()
	model := llm.NewFakeModel(llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "p", Name: process.StartName, Args: map[string]any{"command": "sleep 30"}}}})
	a, err := CreateCLIAgent(context.Background(), Options{Model: model, Cwd: t.TempDir(), NoWebTools: true, Interactive: true,
		Plans: planmode.NewMemoryStore(), Processes: procs, Checkpointer: rt.NewMemoryCheckpointer()})
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.Invoke(context.Background(), core.Input{Messages: []llm.Message{llm.NewUserMessage("start the dev server")}}, rt.RunConfig{ThreadID: "t"})
	var paused *rt.InterruptError
	if !errors.As(err, &paused) {
		t.Fatalf("start_process ran without review: %v", err)
	}
	var offered []string
	for _, spec := range model.Requests()[0].Tools {
		offered = append(offered, spec.Name)
	}
	for _, name := range []string{process.StartName, process.ReadName, process.StopName} {
		if !slices.Contains(offered, name) {
			t.Errorf("tools %v missing %s", offered, name)
		}
	}

	headless := llm.NewFakeModel(llm.NewAssistantMessage("ok"))
	b, err := CreateCLIAgent(context.Background(), Options{Model: headless, Cwd: t.TempDir(), NoWebTools: true, NoVerifyGate: true, AutoApprove: true, Processes: procs})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Invoke(context.Background(), core.Input{Messages: []llm.Message{llm.NewUserMessage("hi")}}, rt.RunConfig{}); err != nil {
		t.Fatal(err)
	}
	for _, spec := range headless.Requests()[0].Tools {
		if spec.Name == process.StartName {
			t.Fatal("headless runs must not start background processes")
		}
	}
}

func TestCreateCLIAgentRequiresCwd(t *testing.T) {
	if _, err := CreateCLIAgent(context.Background(), Options{Model: llm.NewFakeModel()}); err == nil {
		t.Fatal("CreateCLIAgent without Cwd must fail")
	}
}
