package patchtoolcalls

import (
	"context"
	"strings"
	"testing"

	"github.com/tsengmingchun/deepagents_golang/llm"
	"github.com/tsengmingchun/deepagents_golang/runtime"
)

func assistantCalling(calls ...llm.ToolCall) llm.Message {
	m := llm.NewAssistantMessage("")
	m.ToolCalls = calls
	return m
}

func apply(t *testing.T, msgs []llm.Message) []llm.Message {
	t.Helper()
	state := &runtime.State{Messages: msgs}
	u, err := New().BeforeAgent(context.Background(), state, nil)
	if err != nil {
		t.Fatal(err)
	}
	if u == nil {
		return msgs
	}
	if err := state.Apply(*u, nil); err != nil {
		t.Fatal(err)
	}
	return state.Messages
}

func TestNoChangeWhenEveryCallIsAnswered(t *testing.T) {
	call := llm.ToolCall{ID: "c1", Name: "ls"}
	state := &runtime.State{Messages: []llm.Message{
		llm.NewUserMessage("hi"),
		assistantCalling(call),
		llm.NewToolMessage("c1", "ls", "ok"),
	}}
	u, err := New().BeforeAgent(context.Background(), state, nil)
	if err != nil || u != nil {
		t.Fatalf("update = %+v, %v", u, err)
	}
}

func TestInsertsCancellationRightAfterDanglingCall(t *testing.T) {
	got := apply(t, []llm.Message{
		llm.NewUserMessage("run it"),
		assistantCalling(llm.ToolCall{ID: "c1", Name: "execute"}, llm.ToolCall{ID: "c2", Name: "ls"}),
		llm.NewToolMessage("c2", "ls", "ok"),
		llm.NewUserMessage("next"),
	})
	if len(got) != 5 {
		t.Fatalf("messages = %d: %+v", len(got), got)
	}
	patched := got[2]
	if patched.Role != llm.RoleTool || patched.ToolCallID != "c1" || patched.Name != "execute" || patched.Status != llm.ToolStatusError {
		t.Fatalf("patched = %+v", patched)
	}
	if !strings.Contains(patched.Text(), "did not complete") {
		t.Fatalf("text = %q", patched.Text())
	}
	if got[3].ToolCallID != "c2" || got[4].Text() != "next" {
		t.Fatalf("order = %+v", got)
	}
}
