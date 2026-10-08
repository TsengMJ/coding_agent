package server

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/tsengmingchun/quill/apps/agent/code/cost"
	"github.com/tsengmingchun/quill/apps/agent/llm"
	"github.com/tsengmingchun/quill/apps/agent/middleware/subagents"
)

func replyWithUsage(text string, in, out, cached int) llm.Message {
	m := llm.NewAssistantMessage(text)
	m.Usage = &llm.Usage{InputTokens: in, OutputTokens: out, TotalTokens: in + out, CacheRead: cached}
	return m
}

func tagged(m llm.Message, model string) llm.Message { return tagModel(m, model) }

func TestComputeUsageTotalsPerModel(t *testing.T) {
	prices := cost.Table{"a": {Input: 1, Output: 2}}
	msgs := []llm.Message{
		llm.NewUserMessage("hi"),
		tagged(replyWithUsage("1", 1000, 100, 0), "p:a"),
		llm.NewUserMessage("again"),
		tagged(replyWithUsage("2", 3000, 200, 1000), "p:b"),
		llm.NewUserMessage("untagged reply follows"),
		replyWithUsage("3", 4000, 50, 0),
	}
	u := computeUsage(msgs, "p:a", prices)
	if len(u.Models) != 2 || u.Models[0].Model != "p:a" || u.Models[1].Model != "p:b" {
		t.Fatalf("models = %+v", u.Models)
	}
	a, b := u.Models[0], u.Models[1]
	if a.Calls != 2 || a.InputTokens != 5000 || a.OutputTokens != 150 {
		t.Errorf("a = %+v (untagged replies count toward the fallback model)", a)
	}
	if b.Calls != 1 || b.CacheReadTokens != 1000 || b.CostUSD != nil {
		t.Errorf("b = %+v", b)
	}
	if a.CostUSD == nil || !near(*a.CostUSD, (5000*1+150*2)/1e6) {
		t.Errorf("a cost = %v", a.CostUSD)
	}
	if !near(u.TotalCostUSD, *a.CostUSD) || len(u.Unpriced) != 1 || u.Unpriced[0] != "p:b" {
		t.Errorf("total = %v unpriced = %v", u.TotalCostUSD, u.Unpriced)
	}
	if u.ContextTokens != 4000 {
		t.Errorf("context = %d, want the last reply's input tokens", u.ContextTokens)
	}
	conv := llm.ApproximateTokenCounter(msgs[:5], nil)
	if u.ConversationTokens != conv || u.FixedTokens != 4000-conv {
		t.Errorf("conversation = %d fixed = %d, want %d / %d", u.ConversationTokens, u.FixedTokens, conv, 4000-conv)
	}
}

func TestComputeUsageCountsSubagents(t *testing.T) {
	prices := cost.Table{"a": {Input: 1, Output: 2}}
	report := llm.NewToolMessage("c1", subagents.TaskToolName, "report")
	report.Metadata = map[string]any{subagents.UsageKey: []any{
		map[string]any{"model": "p:a", "calls": 3, "input_tokens": 2000, "output_tokens": 20},
		map[string]any{"model": "", "calls": 1, "input_tokens": 10, "output_tokens": 1},
	}}
	msgs := []llm.Message{
		llm.NewUserMessage("hi"),
		tagged(replyWithUsage("", 1000, 100, 0), "p:a"),
		report,
		tagged(replyWithUsage("done", 1500, 10, 0), "p:a"),
	}
	u := computeUsage(msgs, "p:a", prices)
	if len(u.Models) != 1 || u.Models[0].Calls != 6 || u.Models[0].InputTokens != 4510 {
		t.Fatalf("models = %+v", u.Models)
	}
	if len(u.Subagents) != 1 || u.Subagents[0].Calls != 4 || u.Subagents[0].InputTokens != 2010 || u.Subagents[0].CostUSD == nil {
		t.Fatalf("subagents = %+v", u.Subagents)
	}
	if !near(u.TotalCostUSD, (4510*1+131*2)/1e6) || u.ContextTokens != 1500 {
		t.Fatalf("total = %v context = %d", u.TotalCostUSD, u.ContextTokens)
	}
}

func TestComputeUsageWithoutReplies(t *testing.T) {
	msgs := []llm.Message{llm.NewUserMessage("hi")}
	u := computeUsage(msgs, "p:a", cost.Table{})
	if u.ContextTokens != 0 || u.FixedTokens != 0 || len(u.Models) != 0 || u.ConversationTokens == 0 {
		t.Fatalf("usage = %+v", u)
	}
}

func near(a, b float64) bool { d := a - b; return d < 1e-12 && d > -1e-12 }

func TestServiceGetUsageAcrossModels(t *testing.T) {
	svc, _ := newService(t, replyWithUsage("default", 1000, 10, 0))
	alt := llm.NewFakeModel(replyWithUsage("alt", 2000, 20, 0)).WithProfile(llm.ModelProfile{MaxInputTokens: 50000})
	svc.newModel = func(string) (llm.ChatModel, error) { return alt, nil }
	svc.prices = cost.Table{"fake": {Input: 1, Output: 1}}
	cwd := t.TempDir()
	if _, err := collect(t, svc, userReq("t1", cwd, "one")); err != nil {
		t.Fatal(err)
	}
	req := userReq("t1", cwd, "two")
	req.Model = "server-test:alt"
	if _, err := collect(t, svc, req); err != nil {
		t.Fatal(err)
	}
	u, err := svc.GetUsage(context.Background(), "t1")
	if err != nil {
		t.Fatal(err)
	}
	if u.Model != "server-test:alt" || u.ContextTokens != 2000 || u.ContextLimit != 50000 || u.ContextPercent != 4 {
		t.Fatalf("usage = %+v", u)
	}
	if len(u.Models) != 2 || u.Models[0].Model != "fake:fake" || u.Models[1].Model != "server-test:alt" {
		t.Fatalf("models = %+v", u.Models)
	}
	if u.Models[0].CostUSD == nil || u.Models[1].CostUSD != nil {
		t.Fatalf("costs = %v / %v", u.Models[0].CostUSD, u.Models[1].CostUSD)
	}
	if missing, err := svc.GetUsage(context.Background(), "nope"); err != nil || missing != nil {
		t.Fatalf("unknown thread = %+v, %v", missing, err)
	}
}

func TestUsageRoute(t *testing.T) {
	svc, _ := newService(t, replyWithUsage("ok", 100, 5, 0))
	if _, err := collect(t, svc, userReq("t1", t.TempDir(), "hi")); err != nil {
		t.Fatal(err)
	}
	h := NewHandler(svc)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/threads/t1/usage", nil))
	var u ThreadUsage
	if err := json.Unmarshal(w.Body.Bytes(), &u); err != nil || w.Code != 200 || u.ContextTokens != 100 {
		t.Fatalf("%d %s (%v)", w.Code, w.Body.String(), err)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/threads/nope/usage", nil))
	if w.Code != 404 {
		t.Fatalf("unknown thread: %d", w.Code)
	}
}
