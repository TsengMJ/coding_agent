package trace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tsengmingchun/quill/apps/agent/backend"
	"github.com/tsengmingchun/quill/apps/agent/llm"
	"github.com/tsengmingchun/quill/apps/agent/tool"
)

func newRecorder(t *testing.T) (*Store, *Recorder) {
	t.Helper()
	return newRecorderOn(t, NewMemoryLog())
}

func newRecorderOn(t *testing.T, l Log) (*Store, *Recorder) {
	t.Helper()
	s := NewStore(l)
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
	sys, user := llm.NewSystemMessage("you are quill"), llm.NewUserMessage("find handlers")
	seq, err := r.RecordLLMCall(call(sys, user))
	if err != nil {
		t.Fatal(err)
	}
	d, err := s.LLMCall("thread-1", seq)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Messages) != 2 || d.Messages[0].Text() != "you are quill" || d.Messages[1].Text() != "find handlers" {
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
	sys, user := llm.NewSystemMessage("you are quill"), llm.NewUserMessage("find handlers")
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

func TestFileLogReadsLegacyTraceFile(t *testing.T) {
	mem := NewMemoryLog()
	_, r := newRecorderOn(t, mem)
	if _, err := r.RecordLLMCall(call(llm.NewUserMessage("hi"))); err != nil {
		t.Fatal(err)
	}
	var data []byte
	for _, l := range mem.Lines("thread-1") {
		data = append(append(data, l...), '\n')
	}
	path := filepath.Join(t.TempDir(), "trace.jsonl")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	ds, err := NewStore(FileLog{Path: path}).Details("")
	if err != nil || len(ds) != 1 || ds[0].Messages[0].Text() != "hi" {
		t.Fatalf("details = %+v, %v", ds, err)
	}
	if err := (FileLog{Path: path}).Append(context.Background(), "", nil); err == nil {
		t.Fatal("FileLog should be read-only")
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
	l := NewMemoryLog()
	_, r := newRecorderOn(t, l)
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
	var raw strings.Builder
	for _, line := range l.Lines("thread-1") {
		raw.Write(line)
	}
	if n := strings.Count(raw.String(), "UNIQUE-PROMPT"); n != 1 {
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
	testReopen(t, NewMemoryLog(), "t")
}

func testReopen(t *testing.T, l Log, thread string) {
	t.Helper()
	first := NewStore(l)
	r, _ := first.Recorder(thread)
	msg := llm.NewUserMessage("same")
	if _, err := r.RecordLLMCall(call(msg)); err != nil {
		t.Fatal(err)
	}
	_ = first.Close()

	second := NewStore(l)
	defer second.Close()
	r2, err := second.Recorder(thread)
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
	tr, _ := second.Load(thread)
	if tr.LLMCalls[1].NewFrom != 1 {
		t.Fatalf("new_from after reopen = %d, want 1", tr.LLMCalls[1].NewFrom)
	}
	if _, err := second.LLMCall(thread, 2); err != nil {
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

func TestPostgresLog(t *testing.T) {
	conn := os.Getenv("POSTGRES_CONN_STRING")
	if conn == "" {
		t.Skip("POSTGRES_CONN_STRING not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, conn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	l, err := NewPostgresLog(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	thread := "trace-test-" + t.Name() + "-" + time.Now().Format(time.RFC3339Nano)
	t.Cleanup(func() { _ = l.DeleteThread(ctx, thread) })

	testReopen(t, l, thread)

	s := NewStore(l)
	r, err := s.Recorder(thread)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.RecordLLMCall(call(llm.NewUserMessage("nul \x00 byte"))); err != nil {
		t.Fatalf("record with NUL: %v", err)
	}
	d, err := s.LLMCall(thread, 3)
	if err != nil || d.Messages[0].Text() != "nul \x00 byte" {
		t.Fatalf("detail = %+v, %v", d, err)
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

func TestLabeledModelRecordsUnderToolCall(t *testing.T) {
	s, r := newRecorder(t)
	m := Labeled(WrapModel(llm.NewFakeModel(llm.NewAssistantMessage("answer"))), WebExtract)
	ctx := tool.WithCallID(WithRecorder(context.Background(), r), "call-7")
	if _, err := m.Generate(ctx, llm.ChatRequest{Messages: []llm.Message{llm.NewUserMessage("page")}}); err != nil {
		t.Fatal(err)
	}
	tr, _ := s.Load("thread-1")
	if len(tr.LLMCalls) != 1 || strings.Join(tr.LLMCalls[0].Namespace, "/") != "web_extract:call-7" {
		t.Fatalf("calls = %+v", tr.LLMCalls)
	}
}

func TestModelRecordsTheBrowserMCP(t *testing.T) {
	s, r := newRecorder(t)
	m := WrapModel(llm.NewFakeModel(llm.NewAssistantMessage("a")))
	ctx := WithBrowserMCP(WithRecorder(context.Background(), r), "playwright")
	if _, err := m.Generate(ctx, llm.ChatRequest{Messages: []llm.Message{llm.NewUserMessage("q")}}); err != nil {
		t.Fatal(err)
	}
	tr, err := s.Load("thread-1")
	if err != nil {
		t.Fatal(err)
	}
	if got := tr.LLMCalls[0].BrowserMCP; got != "playwright" {
		t.Fatalf("BrowserMCP = %q", got)
	}
}
