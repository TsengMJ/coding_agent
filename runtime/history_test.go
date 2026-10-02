package runtime

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/tsengmingchun/deepagents_golang/llm"
)

func TestHistoryPagersAgreeOnTurns(t *testing.T) {
	pagers := map[string]func(t *testing.T) (Checkpointer, string){
		"memory": func(*testing.T) (Checkpointer, string) { return NewMemoryCheckpointer(), "t" },
		"postgres": func(t *testing.T) (Checkpointer, string) {
			conn := os.Getenv("POSTGRES_CONN_STRING")
			if conn == "" {
				t.Skip("POSTGRES_CONN_STRING is not set")
			}
			cp, err := NewPostgresCheckpointer(context.Background(), conn)
			if err != nil {
				t.Fatal(err)
			}
			thread := fmt.Sprintf("hist-%d", time.Now().UnixNano())
			t.Cleanup(func() { _ = cp.DeleteThread(context.Background(), thread); cp.Close() })
			return cp, thread
		},
	}
	summary := llm.NewUserMessage("summary of earlier work")
	summary.Metadata = map[string]any{SyntheticSourceKey: "summarization"}
	msgs := []llm.Message{llm.NewUserMessage("first"), summary}
	for range 30 {
		msgs = append(msgs, llm.NewAssistantMessage("step"))
	}
	msgs = append(msgs, llm.NewUserMessage("second"), llm.NewAssistantMessage("done"))
	for name, mk := range pagers {
		t.Run(name, func(t *testing.T) {
			cp, thread := mk(t)
			if err := cp.Put(context.Background(), Checkpoint{ThreadID: thread, State: State{Messages: msgs}}); err != nil {
				t.Fatal(err)
			}
			all, err := LoadHistoryPage(context.Background(), cp, thread, 0, 20)
			if err != nil {
				t.Fatal(err)
			}
			if len(all.Messages) != len(msgs) || all.Messages[0].Text() != "first" || all.HasMore {
				t.Fatalf("fewer turns than the limit should return the whole thread, got %d messages from %q (more %v)",
					len(all.Messages), all.Messages[0].Text(), all.HasMore)
			}
			last, err := LoadHistoryPage(context.Background(), cp, thread, 0, 1)
			if err != nil {
				t.Fatal(err)
			}
			if len(last.Messages) != 2 || last.Messages[0].Text() != "second" {
				t.Fatalf("one turn = %d messages from %q", len(last.Messages), last.Messages[0].Text())
			}
			older, err := LoadHistoryPage(context.Background(), cp, thread, last.NextBefore, 1)
			if err != nil {
				t.Fatal(err)
			}
			if older.Messages[0].Text() != "first" || len(older.Messages) != 32 || older.HasMore {
				t.Fatalf("a summary must not start a turn: %d messages from %q", len(older.Messages), older.Messages[0].Text())
			}
		})
	}
}

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
