package subagents

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/tsengmingchun/quill/agent"
	"github.com/tsengmingchun/quill/llm"
	"github.com/tsengmingchun/quill/runtime"
	"github.com/tsengmingchun/quill/tool"
)

func bgCall(id, typ, desc string) llm.ToolCall {
	c := taskCall(id, typ, desc)
	c.Args["run_in_background"] = true
	return c
}

func outputCall(id, taskID string, timeout float64) llm.ToolCall {
	return llm.ToolCall{ID: id, Name: TaskOutputToolName, Args: map[string]any{"task_id": taskID, "timeout": timeout}}
}

// releaseTool closes release when called, so a test can prove the parent
// kept working while a background task was blocked.
func releaseTool(release chan struct{}) tool.Tool {
	return &tool.Func{ToolName: "release", ToolSchema: tool.Schema{"type": "object"}, Fn: func(context.Context, *tool.Runtime, map[string]any) (*tool.Result, error) {
		close(release)
		return textResult("released"), nil
	}}
}

// blockedAgent reports once release is closed, or fails when ctx ends.
func blockedAgent(release <-chan struct{}, report string) agent.Agent {
	return funcAgent{invoke: func(ctx context.Context, _ agent.Input) (*runtime.State, error) {
		select {
		case <-release:
			return &runtime.State{Messages: []llm.Message{llm.NewAssistantMessage(report)}}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}}
}

func backgroundParent(t *testing.T, sub agent.Agent, release chan struct{}, replies ...llm.Message) (agent.Agent, *llm.FakeModel) {
	t.Helper()
	mw, err := New(Options{Background: true, SubAgents: []Spec{CompiledSubAgent{Name: "w", Description: "Works", Runnable: sub}}})
	if err != nil {
		t.Fatal(err)
	}
	model := llm.NewFakeModel(replies...)
	a, err := agent.CreateAgent(agent.Config{Model: model, Tools: []tool.Tool{releaseTool(release)}, Middleware: []agent.Middleware{mw}})
	if err != nil {
		t.Fatal(err)
	}
	return a, model
}

func notifications(st *runtime.State) []string {
	var out []string
	for _, m := range st.Messages {
		if strings.HasPrefix(m.Text(), "<task-notification>") {
			out = append(out, m.Text())
		}
	}
	return out
}

func TestBackgroundTaskOverlapsParentAndNotifiesOnce(t *testing.T) {
	release := make(chan struct{})
	a, _ := backgroundParent(t, blockedAgent(release, "the report"), release,
		callsMessage(bgCall("c1", "w", "research")),
		callsMessage(llm.ToolCall{ID: "c2", Name: "release", Args: map[string]any{}}),
		llm.NewAssistantMessage("waiting"),
		llm.NewAssistantMessage("final"),
	)
	st, err := a.Invoke(context.Background(), agent.Input{Messages: []llm.Message{llm.NewUserMessage("go")}}, runtime.RunConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if got := toolText(st, "c1"); !strings.Contains(got, "c1") || !strings.Contains(got, "background") {
		t.Fatalf("task result = %q", got)
	}
	notes := notifications(st)
	if len(notes) != 1 || !strings.Contains(notes[0], "<task-id>c1</task-id>") ||
		!strings.Contains(notes[0], "<status>completed</status>") || !strings.Contains(notes[0], "the report") {
		t.Fatalf("notifications = %q", notes)
	}
	if last := st.Messages[len(st.Messages)-1]; last.Role != llm.RoleAssistant {
		t.Fatalf("the model must answer after the notification, last = %+v", last)
	}
}

func TestTaskOutputWaitsAndTimesOut(t *testing.T) {
	release := make(chan struct{})
	a, _ := backgroundParent(t, blockedAgent(release, "the report"), release,
		callsMessage(bgCall("c1", "w", "research")),
		callsMessage(outputCall("c2", "c1", 0.05)),
		callsMessage(llm.ToolCall{ID: "c3", Name: "release", Args: map[string]any{}}, outputCall("c4", "c1", 5)),
		llm.NewAssistantMessage("done"),
	)
	st, err := a.Invoke(context.Background(), agent.Input{Messages: []llm.Message{llm.NewUserMessage("go")}}, runtime.RunConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if got := toolText(st, "c2"); !strings.Contains(got, "still running") {
		t.Fatalf("timed-out output = %q", got)
	}
	if got := toolText(st, "c4"); !strings.Contains(got, "the report") {
		t.Fatalf("waited output = %q", got)
	}
	if notes := notifications(st); len(notes) != 0 {
		t.Fatalf("a result read with task_output must not be notified again: %q", notes)
	}
}

func TestCancellingRunCancelsBackgroundTasks(t *testing.T) {
	cancelled := make(chan struct{})
	started := make(chan struct{})
	sub := funcAgent{invoke: func(ctx context.Context, _ agent.Input) (*runtime.State, error) {
		close(started)
		<-ctx.Done()
		close(cancelled)
		return nil, ctx.Err()
	}}
	a, _ := backgroundParent(t, sub, make(chan struct{}),
		callsMessage(bgCall("c1", "w", "research")),
		llm.NewAssistantMessage("waiting"),
	)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { <-started; cancel() }()
	_, err := a.Invoke(ctx, agent.Input{Messages: []llm.Message{llm.NewUserMessage("go")}}, runtime.RunConfig{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("background task was not cancelled")
	}
}

func TestFailedBackgroundTaskIsReported(t *testing.T) {
	sub := funcAgent{invoke: func(context.Context, agent.Input) (*runtime.State, error) {
		return nil, errors.New("boom")
	}}
	a, _ := backgroundParent(t, sub, make(chan struct{}),
		callsMessage(bgCall("c1", "w", "research")),
		llm.NewAssistantMessage("waiting"),
		llm.NewAssistantMessage("final"),
	)
	st, err := a.Invoke(context.Background(), agent.Input{Messages: []llm.Message{llm.NewUserMessage("go")}}, runtime.RunConfig{})
	if err != nil {
		t.Fatal(err)
	}
	notes := notifications(st)
	if len(notes) != 1 || !strings.Contains(notes[0], "<status>failed</status>") || !strings.Contains(notes[0], "boom") {
		t.Fatalf("notifications = %q", notes)
	}
}

func TestBackgroundNeedsOption(t *testing.T) {
	mw, err := New(Options{SubAgents: []Spec{CompiledSubAgent{Name: "w", Description: "d", Runnable: funcAgent{
		invoke: func(context.Context, agent.Input) (*runtime.State, error) {
			t.Error("subagent must not run")
			return nil, nil
		},
	}}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(mw.Tools()) != 1 {
		t.Fatalf("tools = %d, want only task", len(mw.Tools()))
	}
	st := runParent(t, mw, agent.Input{Messages: []llm.Message{llm.NewUserMessage("go")}}, bgCall("c1", "w", "x"))
	if got := toolText(st, "c1"); !strings.Contains(got, `unexpected argument "run_in_background"`) {
		t.Fatalf("result = %q", got)
	}
}

func TestTaskOutputUnknownID(t *testing.T) {
	a, _ := backgroundParent(t, funcAgent{}, make(chan struct{}),
		callsMessage(outputCall("c1", "nope", 0)),
		llm.NewAssistantMessage("done"),
	)
	st, err := a.Invoke(context.Background(), agent.Input{Messages: []llm.Message{llm.NewUserMessage("go")}}, runtime.RunConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if got := toolText(st, "c1"); !strings.Contains(got, "no background task") {
		t.Fatalf("result = %q", got)
	}
}

func TestParseNotificationRoundTrips(t *testing.T) {
	text := notification(&bgTask{id: "c1", subagentType: "w", status: TaskCompleted, report: "line 1\n<b>line 2</b>"})
	got, ok := ParseNotification(text)
	want := Notification{TaskID: "c1", SubagentType: "w", Status: TaskCompleted, Result: "line 1\n<b>line 2</b>"}
	if !ok || got != want {
		t.Fatalf("ParseNotification = %+v, %v", got, ok)
	}
	if _, ok := ParseNotification("hello"); ok {
		t.Fatal("plain text is not a notification")
	}
}
