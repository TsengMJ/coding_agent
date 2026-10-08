package subagents

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/tsengmingchun/quill/apps/agent/agent"
	"github.com/tsengmingchun/quill/apps/agent/llm"
	"github.com/tsengmingchun/quill/apps/agent/runtime"
)

func replyUsing(text string, in, out int, model string) llm.Message {
	m := llm.NewAssistantMessage(text)
	m.Usage = &llm.Usage{InputTokens: in, OutputTokens: out, TotalTokens: in + out}
	if model != "" {
		m.Metadata = map[string]any{ModelNameKey: model}
	}
	return m
}

func toolMessage(st *runtime.State, callID string) llm.Message {
	for _, m := range st.Messages {
		if m.Role == llm.RoleTool && m.ToolCallID == callID {
			return m
		}
	}
	return llm.Message{}
}

func TestTaskReportCarriesSubagentUsage(t *testing.T) {
	nested := llm.NewToolMessage("n1", TaskToolName, "inner report")
	nested.Metadata = map[string]any{UsageKey: []any{map[string]any{"model": "p:small", "calls": 2, "input_tokens": 50, "output_tokens": 5}}}
	sub := funcAgent{invoke: func(context.Context, agent.Input) (*runtime.State, error) {
		return &runtime.State{Messages: []llm.Message{
			llm.NewUserMessage("task"),
			replyUsing("", 100, 10, "p:big"),
			nested,
			replyUsing("report", 300, 30, ""),
		}}, nil
	}}
	mw, err := New(Options{SubAgents: []Spec{CompiledSubAgent{Name: "w", Description: "Works", Runnable: sub}}})
	if err != nil {
		t.Fatal(err)
	}
	st := runParent(t, mw, agent.Input{Messages: []llm.Message{llm.NewUserMessage("go")}}, taskCall("c1", "w", "do"))
	got := ParseUsage(toolMessage(st, "c1").Metadata[UsageKey])
	want := []ModelUsage{
		{Model: "", Calls: 1, InputTokens: 300, OutputTokens: 30},
		{Model: "p:big", Calls: 1, InputTokens: 100, OutputTokens: 10},
		{Model: "p:small", Calls: 2, InputTokens: 50, OutputTokens: 5},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("usage = %+v", got)
	}
}

func TestDeclarativeSubagentUsageNamesItsModel(t *testing.T) {
	sub := llm.NewFakeModel(replyUsing("report", 10, 1, ""))
	mw, err := New(Options{SubAgents: []Spec{SubAgent{Name: "w", Description: "Works", Model: sub}}})
	if err != nil {
		t.Fatal(err)
	}
	st := runParent(t, mw, agent.Input{Messages: []llm.Message{llm.NewUserMessage("go")}}, taskCall("c1", "w", "do"))
	got := ParseUsage(toolMessage(st, "c1").Metadata[UsageKey])
	if len(got) != 1 || got[0].Model != specOf(sub) || got[0].Calls != 1 {
		t.Fatalf("usage = %+v", got)
	}
}

func TestBackgroundReportCarriesUsage(t *testing.T) {
	release := make(chan struct{})
	sub := funcAgent{invoke: func(ctx context.Context, _ agent.Input) (*runtime.State, error) {
		select {
		case <-release:
			return &runtime.State{Messages: []llm.Message{replyUsing("report", 7, 3, "p:m")}}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}}
	a, _ := backgroundParent(t, sub, release,
		callsMessage(bgCall("c1", "w", "research")),
		callsMessage(llm.ToolCall{ID: "r", Name: "release", Args: map[string]any{}}, outputCall("c2", "c1", 5)),
		callsMessage(bgCall("c3", "w", "research")),
		llm.NewAssistantMessage("waiting"),
		llm.NewAssistantMessage("final"),
	)
	st, err := a.Invoke(context.Background(), agent.Input{Messages: []llm.Message{llm.NewUserMessage("go")}}, runtime.RunConfig{})
	if err != nil {
		t.Fatal(err)
	}
	want := []ModelUsage{{Model: "p:m", Calls: 1, InputTokens: 7, OutputTokens: 3}}
	if got := ParseUsage(toolMessage(st, "c2").Metadata[UsageKey]); !reflect.DeepEqual(got, want) {
		t.Fatalf("task_output usage = %+v", got)
	}
	var notified []ModelUsage
	for _, m := range st.Messages {
		if m.Metadata["task_notification"] == "c3" {
			notified = ParseUsage(m.Metadata[UsageKey])
		}
	}
	if !reflect.DeepEqual(notified, want) {
		t.Fatalf("notification usage = %+v", notified)
	}
}

func TestDeclarativeSubagentReturnsStructuredResponse(t *testing.T) {
	schema := map[string]any{"title": "Findings", "type": "object",
		"properties": map[string]any{"files": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}},
		"required":   []any{"files"}}
	sub := llm.NewFakeModel(llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{
		{ID: "f", Name: "Findings", Args: map[string]any{"files": []any{"a.go", "b.go"}}},
	}})
	mw, err := New(Options{SubAgents: []Spec{SubAgent{Name: "finder", Description: "Finds", Model: sub,
		ResponseFormat: agent.ToolStrategy{Schema: schema}}}})
	if err != nil {
		t.Fatal(err)
	}
	st := runParent(t, mw, agent.Input{Messages: []llm.Message{llm.NewUserMessage("go")}}, taskCall("c1", "finder", "find files"))
	if got := toolText(st, "c1"); got != `{"files":["a.go","b.go"]}` {
		t.Fatalf("task result = %q", got)
	}
}

func TestParseUsageAfterJSON(t *testing.T) {
	in := []ModelUsage{{Model: "p:m", Calls: 1, InputTokens: 2, OutputTokens: 3, CacheRead: 4}}
	raw, _ := json.Marshal(map[string]any{UsageKey: in})
	var back map[string]any
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if got := ParseUsage(back[UsageKey]); !reflect.DeepEqual(got, in) {
		t.Fatalf("usage = %+v", got)
	}
	if ParseUsage("junk") != nil {
		t.Fatal("junk must parse to nil")
	}
}
