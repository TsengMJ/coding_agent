package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	core "github.com/tsengmingchun/quill/apps/agent/agent"
	"github.com/tsengmingchun/quill/apps/agent/llm"
	rt "github.com/tsengmingchun/quill/apps/agent/runtime"
)

// sentBudget runs one model call through b and returns the budget warning
// it added, or "".
func sentBudget(t *testing.T, ctx context.Context, b *budgetNotice, req *core.ModelRequest) string {
	t.Helper()
	var sent []llm.Message
	_, err := b.WrapModelCall(ctx, req, func(_ context.Context, r *core.ModelRequest) (*core.ModelResponse, error) {
		sent = r.Messages
		return &core.ModelResponse{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(sent) == len(req.Messages) {
		return ""
	}
	last := sent[len(sent)-1]
	if last.Role != llm.RoleSystem || last.Metadata[rt.SyntheticSourceKey] != BudgetSource {
		t.Fatalf("tail = %+v", last)
	}
	return last.Text()
}

func TestBudgetNoticeAfterMostOfTheDeadline(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	b := newBudgetNotice()
	b.now = func() time.Time { return now }
	ctx, cancel := context.WithDeadline(context.Background(), now.Add(50*time.Minute))
	defer cancel()
	req := &core.ModelRequest{Messages: []llm.Message{llm.NewUserMessage("fix it")}, Runtime: &core.Runtime{Config: rt.RunConfig{ThreadID: "t"}}}
	if got := sentBudget(t, ctx, b, req); got != "" {
		t.Fatalf("warned at the start: %q", got)
	}
	now = now.Add(39 * time.Minute)
	if got := sentBudget(t, ctx, b, req); got != "" {
		t.Fatalf("warned at 78%%: %q", got)
	}
	now = now.Add(time.Minute)
	if got := sentBudget(t, ctx, b, req); !strings.Contains(got, "40 of 50 minutes") {
		t.Fatalf("warning = %q", got)
	}
}

func TestBudgetNoticeAfterMostOfTheSteps(t *testing.T) {
	msgs := []llm.Message{llm.NewUserMessage("fix it")}
	for range 8 {
		msgs = append(msgs, llm.NewAssistantMessage("working"))
	}
	state := &rt.State{Messages: msgs}
	req := func(limit int) *core.ModelRequest {
		return &core.ModelRequest{Messages: msgs, State: state, Runtime: &core.Runtime{Config: rt.RunConfig{RecursionLimit: limit}}}
	}
	if got := sentBudget(t, context.Background(), newBudgetNotice(), req(11)); got != "" {
		t.Fatalf("warned at 8 of 11 steps: %q", got)
	}
	if got := sentBudget(t, context.Background(), newBudgetNotice(), req(10)); !strings.Contains(got, "8 of 10 steps") {
		t.Fatalf("warning = %q", got)
	}
	if got := sentBudget(t, context.Background(), newBudgetNotice(), req(0)); got != "" {
		t.Fatalf("warned without a step limit: %q", got)
	}
}
