package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tsengmingchun/quill/apps/agent/code/storage"
	"github.com/tsengmingchun/quill/apps/agent/code/trace"
	"github.com/tsengmingchun/quill/apps/agent/llm"
)

func newTracedService(t *testing.T, replies ...llm.Message) *Service {
	t.Helper()
	svc, err := New(context.Background(), Config{Storage: storage.NewMemory(), Model: llm.NewFakeModel(replies...), NoVerifyGate: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	return svc
}

func TestServiceTracesModelCallsDiffsAndSubagents(t *testing.T) {
	cwd := t.TempDir()
	svc := newTracedService(t,
		llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "w", Name: "write_file",
			Args: map[string]any{"file_path": "/a.txt", "content": "one\n"}}}},
		llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "t", Name: "task",
			Args: map[string]any{"subagent_type": "general-purpose", "description": "edit it"}}}},
		llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "e", Name: "edit_file",
			Args: map[string]any{"file_path": "/a.txt", "old_string": "one", "new_string": "two"}}}},
		llm.NewAssistantMessage("sub report"),
		llm.NewAssistantMessage("all done"),
	)
	events, err := collect(t, svc, userReq("tr", cwd, "go"))
	if err != nil {
		t.Fatal(err)
	}

	var traced int
	for _, ev := range events {
		if ev.Kind == EventTrace {
			traced++
		}
	}
	if traced < 6 {
		t.Fatalf("EventTrace count = %d, want at least 6 (4 model calls, 2 diffs)", traced)
	}
	for _, m := range messages(events) {
		if m.Role == llm.RoleAssistant {
			if _, ok := trace.SeqOf(m); !ok {
				t.Fatalf("assistant message %q has no trace seq", m.Text())
			}
		}
	}

	tr, err := svc.GetTrace(context.Background(), "tr")
	if err != nil {
		t.Fatal(err)
	}
	if len(tr.LLMCalls) != 5 {
		t.Fatalf("model calls = %d, want 5", len(tr.LLMCalls))
	}
	var sub int
	for _, c := range tr.LLMCalls {
		if len(c.Namespace) == 1 && c.Namespace[0] == "task:t" {
			sub++
		}
	}
	if sub != 2 {
		t.Fatalf("subagent model calls = %d, want 2", sub)
	}
	if len(tr.Diffs) != 2 || tr.Diffs[0].ToolCallID != "w" || tr.Diffs[1].ToolCallID != "e" ||
		len(tr.Diffs[1].Namespace) != 1 || !strings.Contains(tr.Diffs[1].Unified, "+two") {
		t.Fatalf("diffs = %+v", tr.Diffs)
	}
	if len(tr.Messages) == 0 || tr.Messages[0].Namespace[0] != "task:t" {
		t.Fatalf("subagent messages = %+v", tr.Messages)
	}

	d, err := svc.GetLLMCall(context.Background(), "tr", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Messages) < 2 || d.Messages[0].Role != llm.RoleSystem || d.Messages[len(d.Messages)-1].Text() != "go" {
		t.Fatalf("first call messages = %+v", d.Messages)
	}
	if len(d.Tools) == 0 {
		t.Fatal("first call recorded no tools")
	}
}

func TestUnknownThreadHasEmptyTrace(t *testing.T) {
	svc, _ := newService(t, llm.NewAssistantMessage("hi"))
	tr, err := svc.GetTrace(context.Background(), "nobody")
	if err != nil || len(tr.LLMCalls) != 0 {
		t.Fatalf("trace = %+v, %v", tr, err)
	}
	if _, err := svc.GetLLMCall(context.Background(), "nobody", 1); !errors.Is(err, trace.ErrNotFound) {
		t.Fatalf("GetLLMCall err = %v", err)
	}
}

func TestHTTPTraceRoutes(t *testing.T) {
	svc := newTracedService(t, llm.NewAssistantMessage("hi"))
	if _, err := collect(t, svc, userReq("h", t.TempDir(), "q")); err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(NewHandler(svc))
	defer hs.Close()

	resp, err := http.Get(hs.URL + "/threads/h/trace")
	if err != nil {
		t.Fatal(err)
	}
	var tr trace.Trace
	_ = json.NewDecoder(resp.Body).Decode(&tr)
	resp.Body.Close()
	if resp.StatusCode != 200 || len(tr.LLMCalls) != 1 {
		t.Fatalf("GET trace = %d %+v", resp.StatusCode, tr)
	}
	for path, want := range map[string]int{
		"/threads/h/trace/llm/1":   http.StatusOK,
		"/threads/h/trace/llm/9":   http.StatusNotFound,
		"/threads/h/trace/llm/abc": http.StatusBadRequest,
	} {
		resp, err := http.Get(hs.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("GET %s = %d, want %d", path, resp.StatusCode, want)
		}
	}
}
