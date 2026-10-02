package bench

import (
	"fmt"
	"strings"
	"testing"

	"github.com/tsengmingchun/deepagents_golang/code/cost"
	"github.com/tsengmingchun/deepagents_golang/code/trace"
	"github.com/tsengmingchun/deepagents_golang/llm"
)

// readCalls builds a conversation of n read_file calls with large outputs.
func readCalls(n int) []llm.Message {
	msgs := []llm.Message{llm.NewSystemMessage("system"), llm.NewUserMessage("fix it")}
	for i := range n {
		id := fmt.Sprintf("c%d", i)
		msgs = append(msgs,
			llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: id, Name: "read_file", Args: map[string]any{"i": i}}}},
			llm.NewToolMessage(id, "read_file", strings.Repeat("x", 4000)))
	}
	return msgs
}

func TestReplayMaskingCountsSharedPrefixAsCached(t *testing.T) {
	first := readCalls(1)
	second := readCalls(2)
	calls := []trace.LLMCallDetail{{Messages: first}, {Messages: second}}
	got := ReplayMasking(calls, cost.Table{})
	want := llm.ApproximateTokenCounter(first, nil)
	if got.Recorded.Cached != want {
		t.Fatalf("cached = %d, want the first request's %d tokens", got.Recorded.Cached, want)
	}
	if got.Masked != got.Recorded {
		t.Fatalf("short conversation was masked: %+v", got)
	}
}

func TestReplayMaskingSeparatesAgents(t *testing.T) {
	msgs := readCalls(1)
	calls := []trace.LLMCallDetail{{Messages: msgs}, {Messages: msgs, LLMCall: trace.LLMCall{Namespace: []string{"explorer"}}}}
	if got := ReplayMasking(calls, cost.Table{}); got.Recorded.Cached != 0 {
		t.Fatalf("cached = %d across different agents", got.Recorded.Cached)
	}
}

func TestReplayMaskingShrinksLongConversations(t *testing.T) {
	calls := []trace.LLMCallDetail{{Messages: readCalls(50)}}
	got := ReplayMasking(calls, cost.Table{})
	if got.Masked.Tokens >= got.Recorded.Tokens/2 {
		t.Fatalf("masked %d of %d tokens", got.Masked.Tokens, got.Recorded.Tokens)
	}
}
