package client

import (
	"context"
	"iter"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tsengmingchun/deepagents_golang/code/server"
	"github.com/tsengmingchun/deepagents_golang/code/sessions"
	"github.com/tsengmingchun/deepagents_golang/llm"
)

func remoteFor(t *testing.T, srv server.Server) *Remote {
	t.Helper()
	hs := httptest.NewServer(server.NewHandler(srv))
	t.Cleanup(hs.Close)
	r, err := NewRemote(hs.URL)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
	return r
}

func newService(t *testing.T, replies ...llm.Message) *server.Service {
	t.Helper()
	svc, err := server.New(context.Background(), server.Config{Model: llm.NewFakeModel(replies...)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	return svc
}

func drain(r server.Server, req server.RunRequest) ([]server.Event, error) {
	var events []server.Event
	for ev, err := range r.Run(context.Background(), req) {
		if err != nil {
			return events, err
		}
		events = append(events, ev)
	}
	return events, nil
}

func TestRemoteRunWritesFileAndContinuesThread(t *testing.T) {
	cwd := t.TempDir()
	r := remoteFor(t, newService(t,
		llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "w", Name: "write_file",
			Args: map[string]any{"file_path": "/a.txt", "content": "hi"}}}},
		llm.NewAssistantMessage("wrote it"),
		llm.NewAssistantMessage("second answer"),
	))
	req := server.RunRequest{ThreadID: "t1", Cwd: cwd, Messages: []llm.Message{llm.NewUserMessage("write")}, AutoApprove: true}
	events, err := drain(r, req)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(cwd, "a.txt")); string(got) != "hi" {
		t.Fatalf("a.txt = %q", got)
	}
	last := events[len(events)-1]
	if last.Kind != server.EventMessage || last.Message.Text() != "wrote it" {
		t.Fatalf("last event = %+v", last)
	}

	req.Messages = []llm.Message{llm.NewUserMessage("again")}
	events, err = drain(r, req)
	if err != nil {
		t.Fatal(err)
	}
	var msgs []llm.Message
	var streamed strings.Builder
	for _, ev := range events {
		switch ev.Kind {
		case server.EventMessage:
			msgs = append(msgs, *ev.Message)
		case server.EventDelta:
			streamed.WriteString(ev.Text)
		}
	}
	if len(msgs) != 2 || msgs[1].Text() != "second answer" || streamed.String() != "second answer" {
		t.Fatalf("second run events = %+v", events)
	}
	ts, err := r.GetThread(context.Background(), "t1")
	if err != nil || ts == nil || len(ts.Messages) != 6 {
		t.Fatalf("thread = %+v, %v", ts, err)
	}
	if missing, err := r.GetThread(context.Background(), "nope"); err != nil || missing != nil {
		t.Fatalf("missing thread = %+v, %v", missing, err)
	}
	list, err := r.ListThreads(context.Background(), sessions.Filter{Cwd: cwd, Limit: 5})
	if err != nil || len(list) != 1 || list[0].Title != "write" {
		t.Fatalf("threads = %+v, %v", list, err)
	}
}

func TestRemoteReportsConflictAndBadRequest(t *testing.T) {
	r := remoteFor(t, newService(t, llm.NewAssistantMessage("ok")))
	cwd := t.TempDir()
	if _, err := drain(r, server.RunRequest{ThreadID: "t1", Cwd: cwd, Messages: []llm.Message{llm.NewUserMessage("x")}}); err != nil {
		t.Fatal(err)
	}
	_, err := drain(r, server.RunRequest{ThreadID: "t1", Cwd: t.TempDir(), Messages: []llm.Message{llm.NewUserMessage("y")}})
	if err == nil || !strings.Contains(err.Error(), "HTTP 409") || !strings.Contains(err.Error(), "different working directory") {
		t.Fatalf("want 409 cwd mismatch, got %v", err)
	}
	_, err = drain(r, server.RunRequest{ThreadID: "t2", Cwd: "relative", Messages: []llm.Message{llm.NewUserMessage("y")}})
	if err == nil || !strings.Contains(err.Error(), "HTTP 400") {
		t.Fatalf("want 400, got %v", err)
	}
}

func TestRemoteSurfacesMidStreamError(t *testing.T) {
	r := remoteFor(t, newService(t))
	events, err := drain(r, server.RunRequest{ThreadID: "t1", Cwd: t.TempDir(), Messages: []llm.Message{llm.NewUserMessage("x")}})
	if err == nil || !strings.Contains(err.Error(), "script exhausted") {
		t.Fatalf("want model error, got %v (events %+v)", err, events)
	}
}

// blockingServer blocks each run until its context is cancelled.
type blockingServer struct {
	fakeServer
	started   chan struct{}
	cancelled chan struct{}
}

func (b *blockingServer) Run(ctx context.Context, _ server.RunRequest) iter.Seq2[server.Event, error] {
	return func(yield func(server.Event, error) bool) {
		if !yield(server.Event{Kind: server.EventDelta, Text: "working"}, nil) {
			return
		}
		close(b.started)
		<-ctx.Done()
		close(b.cancelled)
	}
}

func TestRemoteCancelStopsServerRun(t *testing.T) {
	b := &blockingServer{started: make(chan struct{}), cancelled: make(chan struct{})}
	r := remoteFor(t, b)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range r.Run(ctx, server.RunRequest{}) {
		}
	}()
	<-b.started
	cancel()
	select {
	case <-b.cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("server run was not cancelled")
	}
	<-done
}
