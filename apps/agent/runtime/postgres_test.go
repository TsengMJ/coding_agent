package runtime

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/smallnest/langgraphgo/checkpoint/postgres"
	"github.com/smallnest/langgraphgo/graph"

	"github.com/tsengmingchun/quill/apps/agent/llm"
	"github.com/tsengmingchun/quill/apps/agent/migrate"
)

func newTestPostgres(t *testing.T) *PostgresCheckpointer {
	t.Helper()
	conn := os.Getenv("POSTGRES_CONN_STRING")
	if conn == "" {
		t.Skip("POSTGRES_CONN_STRING is not set")
	}
	cp, err := NewPostgresCheckpointer(context.Background(), conn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cp.Close)
	if err := migrate.Up(context.Background(), cp.Pool(), migrate.Quill()); err != nil {
		t.Fatal(err)
	}
	return cp
}

func TestPostgresCheckpointerPrunesAndContinuesSeq(t *testing.T) {
	ctx := context.Background()
	a := newTestPostgres(t)
	thread := fmt.Sprintf("pg-prune-%d", time.Now().UnixNano())
	t.Cleanup(func() { _ = a.DeleteThread(context.Background(), thread) })

	total := DefaultCheckpointsPerThread + 3
	for i := 1; i <= total; i++ {
		if err := a.Put(ctx, Checkpoint{ThreadID: thread, Steps: i}); err != nil {
			t.Fatal(err)
		}
	}
	listed, err := a.List(ctx, thread, 0)
	if err != nil || len(listed) != DefaultCheckpointsPerThread {
		t.Fatalf("list = %d checkpoints, %v", len(listed), err)
	}

	b := newTestPostgres(t)
	if err := b.Put(ctx, Checkpoint{ThreadID: thread, Steps: 99}); err != nil {
		t.Fatal(err)
	}
	latest, err := b.Latest(ctx, thread)
	if err != nil || latest == nil || latest.Steps != 99 || latest.Seq != int64(total+1) {
		t.Fatalf("latest after restart = %+v, %v", latest, err)
	}
}

func TestPostgresCheckpointerReadsLegacyRows(t *testing.T) {
	ctx := context.Background()
	a := newTestPostgres(t)
	thread := fmt.Sprintf("pg-legacy-%d", time.Now().UnixNano())
	t.Cleanup(func() { _ = a.DeleteThread(context.Background(), thread) })

	legacy := postgres.NewPostgresCheckpointStoreWithPool(a.Pool(), postgresCheckpointTable)
	if err := legacy.Save(ctx, &graph.Checkpoint{
		ID:        thread + ":old",
		State:     Checkpoint{ThreadID: thread, Seq: 40, Steps: 7},
		Timestamp: time.Now().Add(-time.Hour),
		Metadata:  map[string]any{"execution_id": thread},
	}); err != nil {
		t.Fatal(err)
	}
	if got, err := a.Latest(ctx, thread); err != nil || got == nil || got.Steps != 7 {
		t.Fatalf("legacy latest = %+v, %v", got, err)
	}
	if err := a.Put(ctx, Checkpoint{ThreadID: thread, Steps: 8}); err != nil {
		t.Fatal(err)
	}
	got, err := a.Latest(ctx, thread)
	if err != nil || got == nil || got.Steps != 8 || got.Seq != 41 {
		t.Fatalf("latest = %+v, %v", got, err)
	}
}

func TestPostgresCheckpointerRoundTrip(t *testing.T) {
	conn := os.Getenv("POSTGRES_CONN_STRING")
	if conn == "" {
		t.Skip("POSTGRES_CONN_STRING is not set")
	}
	ctx := context.Background()
	a, err := NewPostgresCheckpointer(ctx, conn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	if err := migrate.Up(context.Background(), a.Pool(), migrate.Quill()); err != nil {
		t.Fatal(err)
	}
	b, err := NewPostgresCheckpointer(ctx, conn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.Close)

	thread := fmt.Sprintf("pg-%d", time.Now().UnixNano())
	other := thread + "-other"
	t.Cleanup(func() {
		_ = a.DeleteThread(context.Background(), thread)
		_ = a.DeleteThread(context.Background(), other)
	})

	if latest, err := b.Latest(ctx, thread); err != nil || latest != nil {
		t.Fatalf("empty latest = %v, %v", latest, err)
	}
	if err := a.Put(ctx, Checkpoint{
		ThreadID: thread,
		State:    State{Messages: []llm.Message{llm.NewUserMessage("hi")}},
		Steps:    1,
	}); err != nil {
		t.Fatal(err)
	}
	got, err := b.Latest(ctx, thread)
	if err != nil || got == nil {
		t.Fatalf("latest = %v, %v", got, err)
	}
	if got.Steps != 1 || len(got.State.Messages) != 1 || got.State.Messages[0].Text() != "hi" {
		t.Fatalf("first checkpoint = %+v", got)
	}

	if err := b.Put(ctx, Checkpoint{
		ThreadID:          thread,
		State:             State{Messages: []llm.Message{llm.NewUserMessage("hi"), llm.NewAssistantMessage("paused")}},
		Steps:             2,
		ResumeFrom:        []string{"tools"},
		PendingInterrupts: []Interrupt{{ID: "tools", Value: "need approval"}},
	}); err != nil {
		t.Fatal(err)
	}
	c, err := NewPostgresCheckpointer(ctx, conn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	latest, err := c.Latest(ctx, thread)
	if err != nil || latest == nil {
		t.Fatalf("resumed latest = %v, %v", latest, err)
	}
	if latest.Steps != 2 || len(latest.ResumeFrom) != 1 || latest.ResumeFrom[0] != "tools" {
		t.Fatalf("resume = %+v", latest)
	}
	if len(latest.PendingInterrupts) != 1 || latest.PendingInterrupts[0].ID != "tools" || latest.PendingInterrupts[0].Value != "need approval" {
		t.Fatalf("interrupts = %+v", latest.PendingInterrupts)
	}
	if len(latest.State.Messages) != 2 || latest.State.Messages[1].Text() != "paused" {
		t.Fatalf("messages = %+v", latest.State.Messages)
	}
	if latest.Seq <= got.Seq {
		t.Fatalf("seq = %d, previous %d", latest.Seq, got.Seq)
	}

	listed, err := a.List(ctx, thread, 0)
	if err != nil || len(listed) != 2 {
		t.Fatalf("list = %+v, %v", listed, err)
	}
	if listed[0].Seq < listed[1].Seq {
		t.Fatalf("list order = %d then %d", listed[0].Seq, listed[1].Seq)
	}

	if err := a.Put(ctx, Checkpoint{
		ThreadID: other,
		State:    State{Messages: []llm.Message{llm.NewUserMessage("elsewhere")}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := b.DeleteThread(ctx, thread); err != nil {
		t.Fatal(err)
	}
	if latest, err := c.Latest(ctx, thread); err != nil || latest != nil {
		t.Fatalf("deleted latest = %v, %v", latest, err)
	}
	kept, err := a.Latest(ctx, other)
	if err != nil || kept == nil || kept.State.Messages[0].Text() != "elsewhere" {
		t.Fatalf("other thread = %+v, %v", kept, err)
	}
}
