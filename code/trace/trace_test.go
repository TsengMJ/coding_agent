package trace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tsengmingchun/deepagents_golang/backend"
	"github.com/tsengmingchun/deepagents_golang/llm"
	"github.com/tsengmingchun/deepagents_golang/tool"
)

func newRecorder(t *testing.T) (*Store, *Recorder) {
	t.Helper()
	s := NewStore(t.TempDir())
	t.Cleanup(func() { _ = s.Close() })
	r, err := s.Recorder("thread-1")
	if err != nil {
		t.Fatal(err)
	}
	return s, r
}

func call(msgs ...llm.Message) LLMCallInput {
	return LLMCallInput{
		Model: "fake:fake",
		Request: llm.ChatRequest{
			Messages: msgs,
			Tools:    []llm.ToolSpec{{Name: "grep", Description: "search"}},
		},
		Response: &llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("ok")},
			ToolCalls: []llm.ToolCall{{ID: "c1", Name: "grep"}}, Usage: &llm.Usage{InputTokens: 10, OutputTokens: 2}},
		Start:    time.Now(),
		Duration: 30 * time.Millisecond,
	}
}

func TestLLMCallDetailRestoresFullRequest(t *testing.T) {
	s, r := newRecorder(t)
	sys, user := llm.NewSystemMessage("you are dcode"), llm.NewUserMessage("find handlers")
	seq, err := r.RecordLLMCall(call(sys, user))
	if err != nil {
		t.Fatal(err)
	}
	d, err := s.LLMCall("thread-1", seq)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Messages) != 2 || d.Messages[0].Text() != "you are dcode" || d.Messages[1].Text() != "find handlers" {
		t.Fatalf("messages = %+v", d.Messages)
	}
	if len(d.Tools) != 1 || d.Tools[0].Name != "grep" {
		t.Fatalf("tools = %+v", d.Tools)
	}
	if d.Response == nil || d.Response.Text() != "ok" || d.Usage.InputTokens != 10 {
		t.Fatalf("response = %+v usage = %+v", d.Response, d.Usage)
	}
}

func TestDetailsReturnsEveryCallInOrder(t *testing.T) {
	s, r := newRecorder(t)
	sys, user := llm.NewSystemMessage("you are dcode"), llm.NewUserMessage("find handlers")
	for _, msgs := range [][]llm.Message{{sys, user}, {sys, user, llm.NewUserMessage("again")}} {
		if _, err := r.RecordLLMCall(call(msgs...)); err != nil {
			t.Fatal(err)
		}
	}
	ds, err := s.Details("thread-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(ds) != 2 || ds[0].Seq != 1 || ds[1].Seq != 2 || len(ds[1].Messages) != 3 || ds[1].Messages[2].Text() != "again" {
		t.Fatalf("details = %+v", ds)
	}
	if ds[0].Response == nil || ds[0].Response.ToolCalls[0].Name != "grep" {
		t.Fatalf("response = %+v", ds[0].Response)
	}
}

func TestLoadSummarizesCallsWithoutBodies(t *testing.T) {
	s, r := newRecorder(t)
	if _, err := r.RecordLLMCall(call(llm.NewUserMessage("hi"))); err != nil {
		t.Fatal(err)
	}
	tr, err := s.Load("thread-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(tr.LLMCalls) != 1 {
		t.Fatalf("calls = %+v", tr.LLMCalls)
	}
	c := tr.LLMCalls[0]
	if c.Seq != 1 || c.MessageCount != 1 || c.ToolCount != 1 || c.ResponseText != "ok" || c.DurationMs != 30 {
		t.Fatalf("summary = %+v", c)
	}
	if len(c.ToolCalls) != 1 || c.ToolCalls[0] != "grep" {
		t.Fatalf("tool calls = %v", c.ToolCalls)
	}
}

func TestRepeatedMessagesAreStoredOnce(t *testing.T) {
	s, r := newRecorder(t)
	sys := llm.NewSystemMessage("UNIQUE-PROMPT " + strings.Repeat("long system prompt ", 200))
	for i := range 5 {
		msgs := []llm.Message{sys}
		for j := range i + 1 {
			msgs = append(msgs, llm.NewUserMessage(strings.Repeat("x", j+1)))
		}
		if _, err := r.RecordLLMCall(call(msgs...)); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(s.path("thread-1"))
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(raw), "UNIQUE-PROMPT"); n != 1 {
		t.Fatalf("system prompt stored %d times, want 1", n)
	}
}

func TestNewFromMarksMessagesAddedSincePreviousCall(t *testing.T) {
	s, r := newRecorder(t)
	sys, u1 := llm.NewSystemMessage("s"), llm.NewUserMessage("u1")
	a1 := llm.NewAssistantMessage("a1")
	u2 := llm.NewUserMessage("u2")
	for _, msgs := range [][]llm.Message{{sys, u1}, {sys, u1, a1, u2}} {
		if _, err := r.RecordLLMCall(call(msgs...)); err != nil {
			t.Fatal(err)
		}
	}
	sub := call(llm.NewSystemMessage("sub"), llm.NewUserMessage("task"))
	sub.Namespace = []string{"task:t1"}
	if _, err := r.RecordLLMCall(sub); err != nil {
		t.Fatal(err)
	}
	tr, _ := s.Load("thread-1")
	got := []int{tr.LLMCalls[0].NewFrom, tr.LLMCalls[1].NewFrom, tr.LLMCalls[2].NewFrom}
	if got[0] != 0 || got[1] != 2 || got[2] != 0 {
		t.Fatalf("new_from = %v, want [0 2 0] (namespaces compared separately)", got)
	}
}

func TestReopenedRecorderContinuesSequence(t *testing.T) {
	dir := t.TempDir()
	first := NewStore(dir)
	r, _ := first.Recorder("t")
	msg := llm.NewUserMessage("same")
	if _, err := r.RecordLLMCall(call(msg)); err != nil {
		t.Fatal(err)
	}
	_ = first.Close()

	second := NewStore(dir)
	defer second.Close()
	r2, err := second.Recorder("t")
	if err != nil {
		t.Fatal(err)
	}
	seq, err := r2.RecordLLMCall(call(msg))
	if err != nil {
		t.Fatal(err)
	}
	if seq != 2 {
		t.Fatalf("seq after reopen = %d, want 2", seq)
	}
	tr, _ := second.Load("t")
	if tr.LLMCalls[1].NewFrom != 1 {
		t.Fatalf("new_from after reopen = %d, want 1", tr.LLMCalls[1].NewFrom)
	}
	if _, err := second.LLMCall("t", 2); err != nil {
		t.Fatalf("detail after reopen: %v", err)
	}
}

func TestUnknownCallAndThread(t *testing.T) {
	s, _ := newRecorder(t)
	if _, err := s.LLMCall("thread-1", 9); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	tr, err := s.Load("nobody")
	if err != nil || len(tr.LLMCalls) != 0 {
		t.Fatalf("Load(unknown) = %+v, %v", tr, err)
	}
}

func TestThreadIDCannotEscapeDirectory(t *testing.T) {
	s := NewStore(t.TempDir())
	for _, id := range []string{"../../etc/passwd", "a/b", ".hidden", ""} {
		if p := s.path(id); filepath.Dir(p) != s.dir {
			t.Errorf("path(%q) = %s, outside %s", id, p, s.dir)
		}
	}
}

func TestModelTagsReplyWithSeq(t *testing.T) {
	s, r := newRecorder(t)
	m := WrapModel(llm.NewFakeModel(llm.NewAssistantMessage("streamed"), llm.NewAssistantMessage("generated")))
	ctx := WithNamespace(WithRecorder(context.Background(), r), "task:x")
	req := llm.ChatRequest{Messages: []llm.Message{llm.NewUserMessage("q")}}

	var final llm.Message
	for c, err := range m.Stream(ctx, req) {
		if err != nil {
			t.Fatal(err)
		}
		if c.Done {
			final = c.Delta
		}
	}
	if seq, ok := SeqOf(final); !ok || seq != 1 {
		t.Fatalf("streamed reply seq = %d, %v", seq, ok)
	}
	reply, err := m.Generate(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if seq, ok := SeqOf(*reply); !ok || seq != 2 {
		t.Fatalf("generated reply seq = %d, %v", seq, ok)
	}
	tr, _ := s.Load("thread-1")
	if len(tr.LLMCalls) != 2 || tr.LLMCalls[0].Namespace[0] != "task:x" || tr.LLMCalls[0].Model != "fake:fake" {
		t.Fatalf("calls = %+v", tr.LLMCalls)
	}
}

func TestModelWithoutRecorderPassesThrough(t *testing.T) {
	m := WrapModel(llm.NewFakeModel(llm.NewAssistantMessage("plain")))
	reply, err := m.Generate(context.Background(), llm.ChatRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := SeqOf(*reply); ok {
		t.Fatal("reply tagged without a recorder")
	}
}

func newFS(t *testing.T) (string, backend.Backend) {
	t.Helper()
	dir := t.TempDir()
	b := backend.NewLocalShellBackend(backend.LocalShellOptions{
		FilesystemOptions: backend.FilesystemOptions{RootDir: dir, VirtualMode: true},
	})
	return dir, WrapBackend(b)
}

func TestBackendRecordsFullDiffs(t *testing.T) {
	s, r := newRecorder(t)
	dir, b := newFS(t)
	ctx := tool.WithCallID(WithRecorder(context.Background(), r), "call-1")

	if res := b.Write(ctx, "/a.go", "package a\n\nfunc A() {}\n"); res.Error != "" {
		t.Fatal(res.Error)
	}
	ctx = tool.WithCallID(ctx, "call-2")
	if res := b.Edit(ctx, "/a.go", "func A() {}", "func A() int { return 1 }", false); res.Error != "" {
		t.Fatal(res.Error)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "a.go")); !strings.Contains(string(got), "return 1") {
		t.Fatalf("file = %q", got)
	}
	tr, _ := s.Load("thread-1")
	if len(tr.Diffs) != 2 {
		t.Fatalf("diffs = %+v", tr.Diffs)
	}
	create, edit := tr.Diffs[0], tr.Diffs[1]
	if create.ToolCallID != "call-1" || create.Op != "write" || create.Added != 3 || create.Removed != 0 ||
		!strings.Contains(create.Unified, "--- /dev/null") {
		t.Fatalf("create diff = %+v", create)
	}
	if edit.ToolCallID != "call-2" || edit.Added != 1 || edit.Removed != 1 ||
		!strings.Contains(edit.Unified, "-func A() {}") || !strings.Contains(edit.Unified, "+func A() int { return 1 }") {
		t.Fatalf("edit diff = %+v", edit)
	}
}

func TestBackendDeleteRecordsRemoval(t *testing.T) {
	s, r := newRecorder(t)
	_, b := newFS(t)
	ctx := WithRecorder(context.Background(), r)
	b.Write(ctx, "/gone.txt", "bye\n")
	d, ok := b.(backend.Deleter)
	if !ok {
		t.Fatal("wrapped backend lost Delete")
	}
	if res := d.Delete(ctx, "/gone.txt"); res.Error != "" {
		t.Fatal(res.Error)
	}
	tr, _ := s.Load("thread-1")
	last := tr.Diffs[len(tr.Diffs)-1]
	if last.Op != "delete" || last.Removed != 1 || !strings.Contains(last.Unified, "+++ /dev/null") {
		t.Fatalf("delete diff = %+v", last)
	}
}

func TestWrappedBackendKeepsExecution(t *testing.T) {
	_, b := newFS(t)
	if !backend.SupportsExecution(b) || !backend.SupportsDelete(b) {
		t.Fatal("wrapper hid execute or delete")
	}
}

func TestCountChangesIgnoresHeadersOnly(t *testing.T) {
	d := "--- a\n+++ b\n@@ -1,2 +1,2 @@\n--- removed sql comment\n+++ added\n ctx\n"
	if a, r := countChanges(d); a != 1 || r != 1 {
		t.Fatalf("added, removed = %d, %d; want 1, 1", a, r)
	}
}
