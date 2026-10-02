package runtime

import (
	"context"
	"testing"

	"github.com/tsengmingchun/deepagents_golang/llm"
)

func TestLoadHistoryPageKeepsWholeUserTurns(t *testing.T) {
	cp := NewMemoryCheckpointer()
	msgs := []llm.Message{}
	for i := 0; i < 4; i++ {
		msgs = append(msgs, llm.NewUserMessage(string(rune('a'+i))), llm.NewAssistantMessage("reply"))
	}
	if err := cp.Put(context.Background(), Checkpoint{ThreadID: "t", State: State{Messages: msgs}}); err != nil {
		t.Fatal(err)
	}
	first, err := LoadHistoryPage(context.Background(), cp, "t", 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(first.Messages); got != 4 || first.Messages[0].Text() != "c" || !first.HasMore {
		t.Fatalf("first page = %+v", first)
	}
	second, err := LoadHistoryPage(context.Background(), cp, "t", first.NextBefore, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(second.Messages); got != 4 || second.Messages[0].Text() != "a" || second.HasMore {
		t.Fatalf("second page = %+v", second)
	}
}
