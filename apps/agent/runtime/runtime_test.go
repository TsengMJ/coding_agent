package runtime

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/tsengmingchun/quill/apps/agent/llm"
)

var (
	_ Checkpointer = (*MemoryCheckpointer)(nil)
	_ Checkpointer = (*PostgresCheckpointer)(nil)
	_ Store        = (*InMemoryStore)(nil)
	_ error        = (*InterruptError)(nil)
)

func TestMemoryCheckpointerRoundTrip(t *testing.T) {
	ctx := context.Background()
	cp := NewMemoryCheckpointer()
	if latest, err := cp.Latest(ctx, "t1"); err != nil || latest != nil {
		t.Fatalf("empty latest = %v, %v", latest, err)
	}
	first := Checkpoint{ThreadID: "t1", State: State{Values: map[string]any{"n": 1}}}
	second := Checkpoint{ThreadID: "t1", State: State{Values: map[string]any{"n": 2}}}
	other := Checkpoint{ThreadID: "t2", State: State{Values: map[string]any{"n": 9}}}
	for _, item := range []Checkpoint{first, second, other} {
		if err := cp.Put(ctx, item); err != nil {
			t.Fatal(err)
		}
	}
	latest, err := cp.Latest(ctx, "t1")
	if err != nil || latest.State.Values["n"] != 2 {
		t.Fatalf("latest = %+v, %v", latest, err)
	}
	listed, err := cp.List(ctx, "t1", 0)
	if err != nil || len(listed) != 2 {
		t.Fatalf("list = %+v, %v", listed, err)
	}
	if err := cp.DeleteThread(ctx, "t1"); err != nil {
		t.Fatal(err)
	}
	if latest, err := cp.Latest(ctx, "t1"); err != nil || latest != nil {
		t.Fatalf("deleted latest = %v, %v", latest, err)
	}
	if latest, err := cp.Latest(ctx, "t2"); err != nil || latest.State.Values["n"] != 9 {
		t.Fatalf("other thread = %+v, %v", latest, err)
	}
}

func TestMemoryCheckpointerKeepsNewestPerThread(t *testing.T) {
	ctx := context.Background()
	cp := NewMemoryCheckpointer()
	total := DefaultCheckpointsPerThread + 5
	for i := 1; i <= total; i++ {
		if err := cp.Put(ctx, Checkpoint{ThreadID: "t1", State: State{Values: map[string]any{"n": i}}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := cp.Put(ctx, Checkpoint{ThreadID: "t2"}); err != nil {
		t.Fatal(err)
	}
	listed, err := cp.List(ctx, "t1", 0)
	if err != nil || len(listed) != DefaultCheckpointsPerThread {
		t.Fatalf("list = %d checkpoints, %v", len(listed), err)
	}
	latest, err := cp.Latest(ctx, "t1")
	if err != nil || latest.State.Values["n"] != total || latest.Seq != int64(total) {
		t.Fatalf("latest = %+v, %v", latest, err)
	}
	if other, err := cp.List(ctx, "t2", 0); err != nil || len(other) != 1 {
		t.Fatalf("other thread = %d, %v", len(other), err)
	}
}

func TestMemoryCheckpointerSeqRestartsAfterDelete(t *testing.T) {
	ctx := context.Background()
	cp := NewMemoryCheckpointer()
	for range 3 {
		if err := cp.Put(ctx, Checkpoint{ThreadID: "t1"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := cp.DeleteThread(ctx, "t1"); err != nil {
		t.Fatal(err)
	}
	if err := cp.Put(ctx, Checkpoint{ThreadID: "t1"}); err != nil {
		t.Fatal(err)
	}
	latest, err := cp.Latest(ctx, "t1")
	if err != nil || latest.Seq != 1 {
		t.Fatalf("latest = %+v, %v", latest, err)
	}
}

func TestCheckpointJSONRoundTrip(t *testing.T) {
	in := Checkpoint{
		ID:       "t1:1",
		ThreadID: "t1",
		State: State{Messages: []llm.Message{
			llm.NewUserMessage("hi"),
			llm.NewAssistantMessage("yo"),
		}},
		CreatedAt:         time.Date(2026, 9, 29, 9, 0, 0, 0, time.UTC),
		Metadata:          map[string]any{"note": "keep"},
		PendingInterrupts: []Interrupt{{ID: "tools", Value: "need approval"}},
		Seq:               4,
		Steps:             3,
		ResumeFrom:        []string{"tools"},
	}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var generic any
	if err := json.Unmarshal(raw, &generic); err != nil {
		t.Fatal(err)
	}
	if _, ok := generic.(map[string]any); !ok {
		t.Fatalf("json state = %T", generic)
	}
	out, err := decodeCheckpoint(generic)
	if err != nil {
		t.Fatal(err)
	}
	if out.ID != in.ID || out.ThreadID != in.ThreadID || out.Seq != in.Seq || out.Steps != in.Steps {
		t.Fatalf("identity = %+v", out)
	}
	if !out.CreatedAt.Equal(in.CreatedAt) {
		t.Fatalf("created_at = %s", out.CreatedAt)
	}
	if len(out.State.Messages) != 2 || out.State.Messages[0].Text() != "hi" || out.State.Messages[1].Text() != "yo" {
		t.Fatalf("messages = %+v", out.State.Messages)
	}
	if len(out.ResumeFrom) != 1 || out.ResumeFrom[0] != "tools" {
		t.Fatalf("resume_from = %#v", out.ResumeFrom)
	}
	if len(out.PendingInterrupts) != 1 || out.PendingInterrupts[0].ID != "tools" || out.PendingInterrupts[0].Value != "need approval" {
		t.Fatalf("interrupts = %+v", out.PendingInterrupts)
	}
	if out.Metadata["note"] != "keep" {
		t.Fatalf("metadata = %#v", out.Metadata)
	}
}
