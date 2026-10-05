package quill

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/tsengmingchun/quill/agent"
	"github.com/tsengmingchun/quill/backend"
	"github.com/tsengmingchun/quill/llm"
	"github.com/tsengmingchun/quill/middleware/hitl"
	"github.com/tsengmingchun/quill/middleware/subagents"
	"github.com/tsengmingchun/quill/middleware/summarization"
	"github.com/tsengmingchun/quill/middleware/todolist"
	"github.com/tsengmingchun/quill/profiles"
	"github.com/tsengmingchun/quill/runtime"
)

// Models built from "assembly-test:<name>" specs, so tests can reach
// profiles registered under that spec.
var (
	testModelsMu sync.Mutex
	testModels   = map[string]*llm.FakeModel{}
)

func init() {
	err := llm.RegisterProvider("assembly-test", func(name string, _ map[string]any) (llm.ChatModel, error) {
		testModelsMu.Lock()
		defer testModelsMu.Unlock()
		return testModels[name], nil
	})
	if err != nil {
		panic(err)
	}
}

// specModel registers a scripted model under a spec unique to the test.
func specModel(t *testing.T, replies ...llm.Message) (string, *llm.FakeModel) {
	t.Helper()
	name := strings.NewReplacer("/", "-", " ", "-").Replace(t.Name())
	m := llm.NewFakeModel(replies...)
	testModelsMu.Lock()
	testModels[name] = m
	testModelsMu.Unlock()
	return "assembly-test:" + name, m
}

func ptr[T any](v T) *T { return &v }

func tempBackend(t *testing.T) backend.Backend {
	return backend.NewFilesystemBackend(backend.FilesystemOptions{RootDir: t.TempDir(), VirtualMode: true})
}

func middlewareNames(stack []agent.Middleware) []string {
	var names []string
	for _, m := range stack {
		names = append(names, m.Name())
	}
	return names
}

func stackFor(t *testing.T, o Options) []agent.Middleware {
	t.Helper()
	model, err := resolveModel(o)
	if err != nil {
		t.Fatal(err)
	}
	a, err := newAssembly(o, model)
	if err != nil {
		t.Fatal(err)
	}
	stack, err := a.buildMiddleware()
	if err != nil {
		t.Fatal(err)
	}
	return stack
}

func stackErr(o Options) error {
	model, err := resolveModel(o)
	if err != nil {
		return err
	}
	a, err := newAssembly(o, model)
	if err != nil {
		return err
	}
	_, err = a.buildMiddleware()
	return err
}

func invoke(t *testing.T, a agent.Agent, text string) *runtime.State {
	t.Helper()
	st, err := a.Invoke(context.Background(), agent.Input{Messages: []llm.Message{llm.NewUserMessage(text)}}, runtime.RunConfig{})
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func toolNames(req llm.ChatRequest) []string {
	var names []string
	for _, spec := range req.Tools {
		names = append(names, spec.Name)
	}
	return names
}

func TestDefaultStackOrder(t *testing.T) {
	got := middlewareNames(stackFor(t, Options{Model: llm.NewFakeModel(), Backend: tempBackend(t)}))
	want := []string{"FilesystemMiddleware", "SubAgentMiddleware", "PatchToolCallsMiddleware", "SummarizationMiddleware"}
	if !slices.Equal(got, want) {
		t.Fatalf("stack = %v, want %v", got, want)
	}
	got = middlewareNames(stackFor(t, Options{Model: llm.NewFakeModel()}))
	if want := []string{"SubAgentMiddleware", "PatchToolCallsMiddleware"}; !slices.Equal(got, want) {
		t.Fatalf("stack without backend = %v, want %v", got, want)
	}
}

func TestCallerMiddlewareReplacesByNameAndOtherwiseLandsBeforeTheTail(t *testing.T) {
	b := tempBackend(t)
	custom, err := summarization.New(summarization.Options{Backend: b, Keep: summarization.ContextSize{Kind: summarization.SizeMessages, Value: 2}})
	if err != nil {
		t.Fatal(err)
	}
	stack := stackFor(t, Options{
		Model:       llm.NewFakeModel(),
		Backend:     b,
		Middleware:  []agent.Middleware{todolist.New("", ""), custom},
		InterruptOn: map[string]hitl.InterruptOnConfig{"execute": {}},
	})
	want := []string{"FilesystemMiddleware", "SubAgentMiddleware", "PatchToolCallsMiddleware", "TodoListMiddleware", "HumanInTheLoopMiddleware", "SummarizationMiddleware"}
	if got := middlewareNames(stack); !slices.Equal(got, want) {
		t.Fatalf("stack = %v, want %v", got, want)
	}
	if stack[len(stack)-1] != agent.Middleware(custom) {
		t.Fatal("caller summarization must replace the default and count the final request")
	}
}

func TestDuplicateCallerMiddlewareIsRejected(t *testing.T) {
	err := stackErr(Options{Model: llm.NewFakeModel(), Middleware: []agent.Middleware{todolist.New("", ""), todolist.New("", "")}})
	if err == nil || !strings.Contains(err.Error(), "duplicate middleware") {
		t.Fatalf("err = %v", err)
	}
}

func TestProfileExcludedMiddlewareValidation(t *testing.T) {
	cases := []struct {
		name     string
		excluded string
		want     string
	}{
		{"required", "FilesystemMiddleware", "cannot exclude"},
		{"private", "_ToolExclusionMiddleware", "private middleware"},
		{"unmatched", "NoSuchMiddleware", "not in any stack"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec, _ := specModel(t)
			if err := profiles.RegisterHarnessProfile(spec, profiles.HarnessProfile{ExcludedMiddleware: []string{tc.excluded}}); err != nil {
				t.Fatal(err)
			}
			err := stackErr(Options{ModelSpec: spec, Backend: tempBackend(t)})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestProfileExcludesMiddlewareAndTools(t *testing.T) {
	spec, model := specModel(t, llm.NewAssistantMessage("ok"))
	err := profiles.RegisterHarnessProfile(spec, profiles.HarnessProfile{
		ExcludedMiddleware: []string{"SummarizationMiddleware"},
		ExcludedTools:      []string{"grep", "task"},
		SystemPromptSuffix: ptr("Be brief."),
	})
	if err != nil {
		t.Fatal(err)
	}
	o := Options{ModelSpec: spec, Backend: tempBackend(t)}
	want := []string{"FilesystemMiddleware", "SubAgentMiddleware", "PatchToolCallsMiddleware", "_ToolExclusionMiddleware"}
	if got := middlewareNames(stackFor(t, o)); !slices.Equal(got, want) {
		t.Fatalf("stack = %v, want %v", got, want)
	}
	a, err := CreateAgent(WithModelSpec(spec), WithBackend(o.Backend), WithSystemPrompt("You code."))
	if err != nil {
		t.Fatal(err)
	}
	invoke(t, a, "hi")
	req := model.Requests()[0]
	if names := toolNames(req); slices.Contains(names, "grep") || slices.Contains(names, "task") || !slices.Contains(names, "ls") {
		t.Fatalf("tools sent = %v", names)
	}
	if sys := req.Messages[0].Text(); !strings.HasPrefix(sys, "You code.\n\nBe brief.") {
		t.Fatalf("system prompt = %q", sys)
	}
}

func TestProfileCanDisableGeneralPurposeSubagent(t *testing.T) {
	spec, _ := specModel(t)
	err := profiles.RegisterHarnessProfile(spec, profiles.HarnessProfile{
		GeneralPurposeSubagent: &profiles.GeneralPurposeSubagentProfile{Enabled: ptr(false)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := middlewareNames(stackFor(t, Options{ModelSpec: spec})); slices.Contains(got, "SubAgentMiddleware") {
		t.Fatalf("stack = %v", got)
	}
}

func TestMainAgentDelegatesToGeneralPurposeSubagent(t *testing.T) {
	model := llm.NewFakeModel(
		llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "t1", Name: "task",
			Args: map[string]any{"subagent_type": "general-purpose", "description": "count the files"}}}},
		llm.NewAssistantMessage("there are 3 files"),
		llm.NewAssistantMessage("done"),
	)
	a, err := CreateAgent(WithModel(model), WithBackend(tempBackend(t)), WithSystemPrompt("main prompt"))
	if err != nil {
		t.Fatal(err)
	}
	st := invoke(t, a, "how many files?")
	var result string
	for _, m := range st.Messages {
		if m.ToolCallID == "t1" {
			result = m.Text()
		}
	}
	if result != "there are 3 files" {
		t.Fatalf("task result = %q", result)
	}
	sub := model.Requests()[1]
	if sys := sub.Messages[0].Text(); !strings.HasPrefix(sys, subagents.DefaultSubagentPrompt) || strings.Contains(sys, "main prompt") {
		t.Fatalf("subagent system prompt = %q", sys)
	}
	if len(sub.Messages) != 2 || sub.Messages[1].Text() != "count the files" {
		t.Fatalf("subagent messages = %+v", sub.Messages)
	}
	if names := toolNames(sub); !slices.Contains(names, "read_file") || slices.Contains(names, "task") {
		t.Fatalf("subagent tools = %v", names)
	}
}

func TestDeclarativeSubagentInheritsModelAndTools(t *testing.T) {
	model := llm.NewFakeModel(
		llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "t1", Name: "task",
			Args: map[string]any{"subagent_type": "reviewer", "description": "review main.go"}}}},
		llm.NewAssistantMessage("looks good"),
		llm.NewAssistantMessage("done"),
	)
	a, err := CreateAgent(WithModel(model), WithBackend(tempBackend(t)),
		WithSubAgents(subagents.SubAgent{Name: "reviewer", Description: "Reviews code", SystemPrompt: "You review."}))
	if err != nil {
		t.Fatal(err)
	}
	invoke(t, a, "review please")
	main := model.Requests()[0]
	var taskDesc string
	for _, spec := range main.Tools {
		if spec.Name == "task" {
			taskDesc = spec.Description
		}
	}
	if !strings.Contains(taskDesc, "- general-purpose: ") || !strings.Contains(taskDesc, "- reviewer: Reviews code") {
		t.Fatalf("task description:\n%s", taskDesc)
	}
	sub := model.Requests()[1]
	if sub.Messages[0].Text() != "You review." || !slices.Contains(toolNames(sub), "edit_file") {
		t.Fatalf("reviewer request: system %q tools %v", sub.Messages[0].Text(), toolNames(sub))
	}
}
