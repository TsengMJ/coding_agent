package client

import (
	"context"
	"testing"

	"github.com/tsengmingchun/quill/code/server"
	"github.com/tsengmingchun/quill/llm"
)

func f64(v float64) *float64 { return &v }

func TestFormatTokens(t *testing.T) {
	u := &server.ThreadUsage{Model: "openai:gpt-4o", ContextTokens: 12345, ContextLimit: 128000, ContextPercent: 9.64}
	if got, want := FormatTokens(u), "12.3k / 128k tokens (9.6%) · openai:gpt-4o"; got != want {
		t.Errorf("got %q want %q", got, want)
	}
	u = &server.ThreadUsage{Model: "x:y", ContextTokens: 800}
	if got, want := FormatTokens(u), "800 tokens · x:y (context window unknown)"; got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestFormatContext(t *testing.T) {
	u := &server.ThreadUsage{Model: "m", ContextTokens: 12000, ContextLimit: 100000, ContextPercent: 12,
		FixedTokens: 5000, ConversationTokens: 7000}
	want := "context 12k / 100k tokens (12.0%) · m\n" +
		"  system prompt + tools  ~5k\n" +
		"  conversation           ~7k\n" +
		"  free                   88k"
	if got := FormatContext(u); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestFormatCost(t *testing.T) {
	u := &server.ThreadUsage{
		Models: []server.ModelUsage{
			{Model: "openai:gpt-4o", Calls: 3, InputTokens: 20000, CacheReadTokens: 5000, OutputTokens: 1500, CostUSD: f64(0.0625)},
			{Model: "x:y", Calls: 1, InputTokens: 100, OutputTokens: 10},
		},
		TotalCostUSD: 0.0625,
		Unpriced:     []string{"x:y"},
	}
	want := "openai:gpt-4o  3 calls · in 20k (5k cached) · out 1.5k · $0.0625\n" +
		"x:y  1 call · in 100 · out 10 · unpriced\n" +
		"total $0.0625 (excluding unpriced models; add them to ~/.quill/prices.json)"
	if got := FormatCost(u); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
	if got := FormatCost(&server.ThreadUsage{}); got != "no model calls yet" {
		t.Errorf("empty: %q", got)
	}
}

func TestUsageCommandsReportThroughSession(t *testing.T) {
	s := &fakeSession{usage: &server.ThreadUsage{Model: "m", ContextTokens: 10}}
	reg := NewCommandRegistry(s, CommandOptions{})
	for _, line := range []string{"/tokens", "/context", "/cost"} {
		if err := reg.Dispatch(context.Background(), line); err != nil {
			t.Fatalf("%s: %v", line, err)
		}
	}
	if len(s.info) != 3 || s.info[0] != "10 tokens · m (context window unknown)" {
		t.Fatalf("info = %q", s.info)
	}
}

func TestRemoteGetUsage(t *testing.T) {
	r := remoteFor(t, newService(t, llm.NewAssistantMessage("ok")))
	cwd := t.TempDir()
	if _, err := drain(r, server.RunRequest{ThreadID: "t1", Cwd: cwd, Messages: []llm.Message{llm.NewUserMessage("x")}}); err != nil {
		t.Fatal(err)
	}
	u, err := r.GetUsage(context.Background(), "t1")
	if err != nil || u == nil || u.ThreadID != "t1" || u.Model != "fake:fake" {
		t.Fatalf("usage = %+v, %v", u, err)
	}
	if u, err := r.GetUsage(context.Background(), "missing"); err != nil || u != nil {
		t.Fatalf("missing = %+v, %v", u, err)
	}
}
