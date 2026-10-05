package client

import (
	"bytes"
	"context"
	"errors"
	"iter"
	"strings"
	"testing"
	"time"

	"github.com/tsengmingchun/quill/code/server"
	"github.com/tsengmingchun/quill/code/sessions"
	"github.com/tsengmingchun/quill/code/trace"
	"github.com/tsengmingchun/quill/llm"
)

type fakeServer struct {
	events []server.Event
	err    error
	got    []server.RunRequest
}

func (f *fakeServer) Run(_ context.Context, req server.RunRequest) iter.Seq2[server.Event, error] {
	f.got = append(f.got, req)
	return func(yield func(server.Event, error) bool) {
		for _, ev := range f.events {
			if !yield(ev, nil) {
				return
			}
		}
		if f.err != nil {
			yield(server.Event{}, f.err)
		}
	}
}

func (f *fakeServer) GetThread(context.Context, string) (*server.ThreadState, error) { return nil, nil }
func (f *fakeServer) GetHistory(context.Context, string, int, int) (*server.HistoryPage, error) {
	return nil, nil
}
func (f *fakeServer) ListThreads(context.Context, sessions.Filter) ([]sessions.Info, error) {
	return nil, nil
}
func (f *fakeServer) RenameThread(context.Context, string, string) (*sessions.Info, error) {
	return nil, nil
}
func (f *fakeServer) Compact(context.Context, string) (*server.CompactResult, error) {
	return nil, nil
}
func (f *fakeServer) GetTrace(context.Context, string) (*trace.Trace, error)        { return nil, nil }
func (f *fakeServer) GetTraceSummary(context.Context, string) (*trace.Trace, error) { return nil, nil }
func (f *fakeServer) GetLLMCall(context.Context, string, int) (*trace.LLMCallDetail, error) {
	return nil, nil
}
func (f *fakeServer) GetUsage(context.Context, string) (*server.ThreadUsage, error) {
	return nil, nil
}
func (f *fakeServer) Close() error { return nil }

func msg(m llm.Message) server.Event { return server.Event{Kind: server.EventMessage, Message: &m} }

func runHeadless(t *testing.T, srv server.Server, opts NonInteractiveOptions) (int, string, string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	opts.Stdout, opts.Stderr = &stdout, &stderr
	if opts.Cwd == "" {
		opts.Cwd = "/project"
	}
	code, err := RunNonInteractive(context.Background(), srv, opts)
	return code, stdout.String(), stderr.String(), err
}

func TestRunNonInteractivePrintsAssistantTextAndTools(t *testing.T) {
	srv := &fakeServer{events: []server.Event{
		msg(llm.NewUserMessage("hi")),
		msg(llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{
			{ID: "w", Name: "write_file", Args: map[string]any{"file_path": "/main.go", "content": "x"}},
			{ID: "x", Name: "execute", Args: map[string]any{"command": "go build\ngo test"}},
		}}),
		msg(llm.Message{Role: llm.RoleTool, Name: "write_file", ToolCallID: "w", Status: llm.ToolStatusSuccess}),
		msg(llm.Message{Role: llm.RoleTool, Name: "execute", ToolCallID: "x", Status: llm.ToolStatusError,
			Content: []llm.ContentBlock{llm.TextBlock("exit 1\nmore")}}),
		msg(llm.NewAssistantMessage("done")),
	}}
	code, stdout, stderr, err := runHeadless(t, srv, NonInteractiveOptions{Prompt: "hi", ThreadID: "t1"})
	if err != nil || code != 0 {
		t.Fatalf("code=%d err=%v", code, err)
	}
	want := "[tool] write_file /main.go\n[tool] execute go build ...\n[tool error] execute: exit 1 ...\n"
	if stderr != want {
		t.Fatalf("stderr = %q, want %q", stderr, want)
	}
	if stdout != "done\n" {
		t.Fatalf("stdout = %q", stdout)
	}
	got := srv.got[0]
	if got.ThreadID != "t1" || got.Cwd != "/project" || got.Messages[0].Text() != "hi" {
		t.Fatalf("request = %+v", got)
	}
}

func TestRunNonInteractiveStreamsDeltasWithoutRepeating(t *testing.T) {
	srv := &fakeServer{events: []server.Event{
		{Kind: server.EventDelta, Text: "hel"},
		{Kind: server.EventDelta, Text: "lo"},
		msg(llm.NewAssistantMessage("hello")),
	}}
	_, stdout, _, _ := runHeadless(t, srv, NonInteractiveOptions{Prompt: "hi", ThreadID: "t1"})
	if stdout != "hello\n" {
		t.Fatalf("stdout = %q", stdout)
	}
}

func TestRunNonInteractiveIgnoresSubagentEvents(t *testing.T) {
	sub := msg(llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("inner")},
		ToolCalls: []llm.ToolCall{{ID: "g", Name: "grep", Args: map[string]any{"pattern": "x"}}}})
	sub.Namespace = []string{"task:t"}
	srv := &fakeServer{events: []server.Event{sub, msg(llm.NewAssistantMessage("done"))}}
	_, stdout, stderr, _ := runHeadless(t, srv, NonInteractiveOptions{Prompt: "hi", ThreadID: "t1"})
	if stdout != "done\n" || stderr != "" {
		t.Fatalf("stdout = %q, stderr = %q", stdout, stderr)
	}
}

func TestRunNonInteractiveStartsNewThread(t *testing.T) {
	srv := &fakeServer{}
	_, _, stderr, err := runHeadless(t, srv, NonInteractiveOptions{Prompt: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	id := srv.got[0].ThreadID
	if len(id) != 12 || stderr != "thread: "+id+"\n" {
		t.Fatalf("thread id %q, stderr %q", id, stderr)
	}
}

func TestRunNonInteractiveReportsRunError(t *testing.T) {
	code, _, stderr, err := runHeadless(t, &fakeServer{err: errors.New("model down")}, NonInteractiveOptions{Prompt: "hi", ThreadID: "t"})
	if err != nil || code != 1 || !strings.Contains(stderr, "model down") {
		t.Fatalf("code=%d err=%v stderr=%q", code, err, stderr)
	}
}

func TestRunNonInteractiveQuietPrintsOnlyTheReply(t *testing.T) {
	srv := &fakeServer{events: []server.Event{
		msg(llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "x", Name: "execute", Args: map[string]any{"command": "ls"}}}}),
		msg(llm.Message{Role: llm.RoleTool, Name: "execute", ToolCallID: "x", Status: llm.ToolStatusError}),
		{Kind: server.EventDelta, Text: "done"},
		msg(llm.NewAssistantMessage("done")),
	}}
	code, stdout, stderr, _ := runHeadless(t, srv, NonInteractiveOptions{Prompt: "hi", Quiet: true})
	if code != 0 || stdout != "done\n" || stderr != "" {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

func TestRunNonInteractiveNoStreamPrintsWholeMessage(t *testing.T) {
	var got []string
	srv := &fakeServer{events: []server.Event{
		{Kind: server.EventDelta, Text: "hel"},
		{Kind: server.EventDelta, Text: "lo"},
		msg(llm.NewAssistantMessage("hello")),
	}}
	w := writerFunc(func(p []byte) (int, error) { got = append(got, string(p)); return len(p), nil })
	code, err := RunNonInteractive(context.Background(), srv, NonInteractiveOptions{
		Prompt: "hi", ThreadID: "t", Cwd: "/p", NoStream: true, Stdout: w, Stderr: &bytes.Buffer{},
	})
	if err != nil || code != 0 {
		t.Fatalf("code=%d err=%v", code, err)
	}
	if len(got) != 1 || got[0] != "hello\n" {
		t.Fatalf("writes = %q, want one write of the whole reply", got)
	}
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

// stallServer runs until its context ends.
type stallServer struct{ fakeServer }

func (b *stallServer) Run(ctx context.Context, _ server.RunRequest) iter.Seq2[server.Event, error] {
	return func(yield func(server.Event, error) bool) {
		<-ctx.Done()
		yield(server.Event{}, ctx.Err())
	}
}

func TestRunNonInteractiveTimeoutExits124(t *testing.T) {
	code, _, stderr, err := runHeadless(t, &stallServer{}, NonInteractiveOptions{
		Prompt: "hi", ThreadID: "t", Timeout: 20 * time.Millisecond,
	})
	if err != nil || code != ExitTimeout || !strings.Contains(stderr, "timed out after 20ms") {
		t.Fatalf("code=%d err=%v stderr=%q", code, err, stderr)
	}
}

func TestRunNonInteractiveInterruptExits130(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(10*time.Millisecond, cancel)
	var stderr bytes.Buffer
	code, err := RunNonInteractive(ctx, &stallServer{}, NonInteractiveOptions{
		Prompt: "hi", ThreadID: "t", Cwd: "/p", Stdout: &bytes.Buffer{}, Stderr: &stderr,
	})
	if err != nil || code != ExitInterrupted || !strings.Contains(stderr.String(), "interrupted") {
		t.Fatalf("code=%d err=%v stderr=%q", code, err, stderr.String())
	}
}

func TestWriteJSONEnvelope(t *testing.T) {
	var b bytes.Buffer
	if err := WriteJSON(&b, "threads list", []sessions.Info{}); err != nil {
		t.Fatal(err)
	}
	want := `{"schema_version":1,"command":"threads list","data":[]}` + "\n"
	if b.String() != want {
		t.Fatalf("json = %q, want %q", b.String(), want)
	}
}

func TestPrintThreads(t *testing.T) {
	var b bytes.Buffer
	PrintThreads(&b, []sessions.Info{{ThreadID: "abc", Title: "fix bug", MessageCount: 3, Cwd: "/p", UpdatedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.Local)}}, true)
	out := b.String()
	for _, want := range []string{"THREAD", "CWD", "abc", "2026-01-02 03:04:05", "fix bug", "/p"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
	b.Reset()
	PrintThreads(&b, nil, false)
	if b.String() != "no threads\n" {
		t.Fatalf("empty = %q", b.String())
	}
}
