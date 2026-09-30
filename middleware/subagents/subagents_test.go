package subagents

import (
	"context"
	"errors"
	"iter"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tsengmingchun/deepagents_golang/agent"
	"github.com/tsengmingchun/deepagents_golang/internal/notimpl"
	"github.com/tsengmingchun/deepagents_golang/llm"
	"github.com/tsengmingchun/deepagents_golang/runtime"
)

// funcAgent is a CompiledSubAgent runnable backed by a function.
type funcAgent struct {
	invoke func(ctx context.Context, in agent.Input) (*runtime.State, error)
}

func (f funcAgent) Name() string { return "func" }
func (f funcAgent) Invoke(ctx context.Context, in agent.Input, _ runtime.RunConfig) (*runtime.State, error) {
	return f.invoke(ctx, in)
}
func (f funcAgent) Stream(context.Context, agent.Input, runtime.RunConfig, ...runtime.StreamMode) iter.Seq2[runtime.Event, error] {
	return nil
}
func (f funcAgent) GetState(context.Context, string) (*runtime.State, error) { return nil, nil }

func taskCall(id, typ, desc string) llm.ToolCall {
	return llm.ToolCall{ID: id, Name: TaskToolName, Args: map[string]any{"subagent_type": typ, "description": desc}}
}

func callsMessage(calls ...llm.ToolCall) llm.Message {
	return llm.Message{Role: llm.RoleAssistant, ToolCalls: calls}
}

// runParent runs a parent agent whose model makes the given calls, then answers "done".
func runParent(t *testing.T, mw *Middleware, in agent.Input, calls ...llm.ToolCall) *runtime.State {
	t.Helper()
	model := llm.NewFakeModel(callsMessage(calls...), llm.NewAssistantMessage("done"))
	a, err := agent.CreateAgent(agent.Config{
		Model:      model,
		Middleware: []agent.Middleware{mw},
		StateSchema: runtime.Schema{
			"shared": {Name: "shared"},
			"secret": {Name: "secret", Private: true},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	st, err := a.Invoke(context.Background(), in, runtime.RunConfig{})
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func toolText(st *runtime.State, callID string) string {
	for _, m := range st.Messages {
		if m.Role == llm.RoleTool && m.ToolCallID == callID {
			return m.Text()
		}
	}
	return "<missing>"
}

func TestValidateSpecs(t *testing.T) {
	cases := []struct {
		name  string
		specs []Spec
		want  string
	}{
		{"duplicate", []Spec{SubAgent{Name: "a"}, CompiledSubAgent{Name: "a"}}, "duplicate subagent name"},
		{"empty name", []Spec{SubAgent{}}, "needs a name"},
		{"bad mode", []Spec{SubAgent{Name: "a", Mode: "shared"}}, "invalid mode"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateSpecs(tc.specs); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
	if err := ValidateSpecs([]Spec{SubAgent{Name: "a", Mode: ModeFork}}); !errors.Is(err, notimpl.ErrNotImplemented) {
		t.Fatalf("fork: err = %v", err)
	}
	if err := ValidateSpecs([]Spec{SubAgent{Name: "a", Mode: "handoff"}, SubAgent{Name: "b", Mode: ModeIsolated}}); err != nil {
		t.Fatalf("valid specs: %v", err)
	}
}

func TestNewRequiresSubagents(t *testing.T) {
	if _, err := New(Options{}); err == nil {
		t.Fatal("want error without subagents")
	}
}

func TestTaskRunsIsolatedSubagentAndReturnsItsReport(t *testing.T) {
	sub := llm.NewFakeModel(llm.NewAssistantMessage("the report\n"))
	mw, err := New(Options{SubAgents: []Spec{SubAgent{
		Name: "researcher", Description: "Researches things", SystemPrompt: "You research.", Model: sub,
	}}, PrivateStateKeys: []string{"secret"}})
	if err != nil {
		t.Fatal(err)
	}
	if desc := mw.Tools()[0].Description(); !strings.Contains(desc, "- researcher: Researches things") {
		t.Fatalf("task description:\n%s", desc)
	}
	in := agent.Input{
		Messages: []llm.Message{llm.NewUserMessage("parent conversation")},
		Values:   map[string]any{"shared": "s", "secret": "hidden", "todos": []any{"x"}},
	}
	st := runParent(t, mw, in, taskCall("c1", "researcher", "find the bug"))
	if got := toolText(st, "c1"); got != "the report" {
		t.Fatalf("tool result = %q", got)
	}
	req := sub.Requests()[0]
	var texts []string
	for _, m := range req.Messages {
		texts = append(texts, string(m.Role)+":"+m.Text())
	}
	if len(req.Messages) != 2 || texts[0] != "system:You research." || texts[1] != "user:find the bug" {
		t.Fatalf("subagent saw %q", texts)
	}
}

func TestStreamingTaskForwardsSubagentEventsUnderItsNamespace(t *testing.T) {
	sub := llm.NewFakeModel(llm.NewAssistantMessage("the report\n"))
	mw, err := New(Options{SubAgents: []Spec{SubAgent{Name: "researcher", Description: "Researches", Model: sub}}})
	if err != nil {
		t.Fatal(err)
	}
	model := llm.NewFakeModel(callsMessage(taskCall("c1", "researcher", "find the bug")), llm.NewAssistantMessage("done"))
	a, err := agent.CreateAgent(agent.Config{Model: model, Middleware: []agent.Middleware{mw}})
	if err != nil {
		t.Fatal(err)
	}
	var nested, final *runtime.State
	for ev, err := range a.Stream(context.Background(), agent.Input{Messages: []llm.Message{llm.NewUserMessage("go")}}, runtime.RunConfig{}) {
		if err != nil {
			t.Fatal(err)
		}
		st, _ := ev.Data.(*runtime.State)
		switch {
		case len(ev.Namespace) == 0:
			final = st
		case len(ev.Namespace) == 1 && ev.Namespace[0] == "task:c1":
			nested = st
		default:
			t.Fatalf("unexpected namespace %q", ev.Namespace)
		}
	}
	if nested == nil || nested.Messages[0].Text() != "find the bug" || nested.Messages[len(nested.Messages)-1].Text() != "the report\n" {
		t.Fatalf("last forwarded subagent state = %+v", nested)
	}
	if got := toolText(final, "c1"); got != "the report" {
		t.Fatalf("tool result = %q", got)
	}
}

func TestTaskPassesSharedStateOnlyAndCopiesItBack(t *testing.T) {
	var got map[string]any
	mw, err := New(Options{SubAgents: []Spec{CompiledSubAgent{Name: "worker", Description: "Works", Runnable: funcAgent{
		invoke: func(_ context.Context, in agent.Input) (*runtime.State, error) {
			got = in.Values
			return &runtime.State{
				Messages: append(in.Messages, llm.NewAssistantMessage("ok")),
				Values:   map[string]any{"shared": "updated", "secret": "leak"},
			}, nil
		},
	}}}, PrivateStateKeys: []string{"secret"}})
	if err != nil {
		t.Fatal(err)
	}
	in := agent.Input{Messages: []llm.Message{llm.NewUserMessage("go")}, Values: map[string]any{"shared": "s", "secret": "hidden"}}
	st := runParent(t, mw, in, taskCall("c1", "worker", "do it"))
	if len(got) != 1 || got["shared"] != "s" {
		t.Fatalf("subagent input values = %v", got)
	}
	if st.Values["shared"] != "updated" || st.Values["secret"] != "hidden" {
		t.Fatalf("parent values = %v", st.Values)
	}
}

func TestTaskReturnsStructuredResponseAsJSON(t *testing.T) {
	mw, err := New(Options{SubAgents: []Spec{CompiledSubAgent{Name: "w", Description: "d", Runnable: funcAgent{
		invoke: func(context.Context, agent.Input) (*runtime.State, error) {
			return &runtime.State{StructuredResponse: map[string]any{"answer": 42}}, nil
		},
	}}}})
	if err != nil {
		t.Fatal(err)
	}
	st := runParent(t, mw, agent.Input{Messages: []llm.Message{llm.NewUserMessage("go")}}, taskCall("c1", "w", "x"))
	if got := toolText(st, "c1"); got != `{"answer":42}` {
		t.Fatalf("tool result = %q", got)
	}
}

func TestTaskRejectsUnknownTypeAndUnknownArguments(t *testing.T) {
	mw, err := New(Options{SubAgents: []Spec{CompiledSubAgent{Name: "w", Description: "d", Runnable: funcAgent{
		invoke: func(context.Context, agent.Input) (*runtime.State, error) {
			t.Error("subagent must not run")
			return nil, nil
		},
	}}}})
	if err != nil {
		t.Fatal(err)
	}
	extra := taskCall("c2", "w", "label")
	extra.Args["prompt"] = "the real instructions"
	st := runParent(t, mw, agent.Input{Messages: []llm.Message{llm.NewUserMessage("go")}}, taskCall("c1", "nope", "x"), extra)
	if got := toolText(st, "c1"); !strings.Contains(got, "does not exist") || !strings.Contains(got, "`w`") {
		t.Fatalf("unknown type result = %q", got)
	}
	if got := toolText(st, "c2"); !strings.Contains(got, `unexpected argument "prompt"`) {
		t.Fatalf("unknown argument result = %q", got)
	}
}

func TestParallelTaskCallsRunConcurrently(t *testing.T) {
	var wg sync.WaitGroup
	wg.Add(2)
	barrier := func(report string) agent.Agent {
		return funcAgent{invoke: func(ctx context.Context, _ agent.Input) (*runtime.State, error) {
			wg.Done()
			done := make(chan struct{})
			go func() { wg.Wait(); close(done) }()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				return nil, errors.New("the other task never started")
			}
			return &runtime.State{Messages: []llm.Message{llm.NewAssistantMessage(report)}}, nil
		}}
	}
	mw, err := New(Options{SubAgents: []Spec{
		CompiledSubAgent{Name: "a", Description: "A", Runnable: barrier("from a")},
		CompiledSubAgent{Name: "b", Description: "B", Runnable: barrier("from b")},
	}})
	if err != nil {
		t.Fatal(err)
	}
	st := runParent(t, mw, agent.Input{Messages: []llm.Message{llm.NewUserMessage("go")}},
		taskCall("c1", "a", "x"), taskCall("c2", "b", "y"))
	if toolText(st, "c1") != "from a" || toolText(st, "c2") != "from b" {
		t.Fatalf("results: %q %q", toolText(st, "c1"), toolText(st, "c2"))
	}
}

func TestSystemPromptListsSubagents(t *testing.T) {
	mw, err := New(Options{SystemPrompt: "Use task.", SubAgents: []Spec{CompiledSubAgent{Name: "w", Description: "Works", Runnable: funcAgent{}}}})
	if err != nil {
		t.Fatal(err)
	}
	var sys string
	_, err = mw.WrapModelCall(context.Background(), &agent.ModelRequest{}, func(_ context.Context, req *agent.ModelRequest) (*agent.ModelResponse, error) {
		sys = req.SystemMessage.Text()
		return &agent.ModelResponse{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sys, "Use task.\n\nAvailable subagent types:\n\n- w: Works") {
		t.Fatalf("system = %q", sys)
	}
}
