package client

import (
	"bytes"
	"context"
	"errors"
	"iter"
	"strings"
	"testing"

	"github.com/tsengmingchun/deepagents_golang/code/server"
	"github.com/tsengmingchun/deepagents_golang/llm"
	"github.com/tsengmingchun/deepagents_golang/runtime"
)

type fakeServer struct {
	state *runtime.State
	err   error
	got   server.RunRequest
}

func (f *fakeServer) Run(_ context.Context, req server.RunRequest) iter.Seq2[runtime.Event, error] {
	f.got = req
	return func(yield func(runtime.Event, error) bool) {
		if f.err != nil {
			yield(runtime.Event{}, f.err)
			return
		}
		yield(runtime.Event{Mode: runtime.StreamValues, Data: f.state}, nil)
	}
}

func (f *fakeServer) GetState(context.Context, string) (*runtime.State, error) { return nil, nil }
func (f *fakeServer) Cancel(context.Context, string) error                     { return nil }
func (f *fakeServer) Close() error                                             { return nil }

func run(t *testing.T, srv server.Server, opts NonInteractiveOptions) (int, string, string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	opts.Stdout, opts.Stderr = &stdout, &stderr
	code, err := RunNonInteractive(context.Background(), srv, opts)
	return code, stdout.String(), stderr.String(), err
}

func TestRunNonInteractivePrintsLastAssistantText(t *testing.T) {
	srv := &fakeServer{state: &runtime.State{Messages: []llm.Message{
		llm.NewUserMessage("hi"),
		llm.NewAssistantMessage("hello there"),
	}}}
	code, stdout, _, err := run(t, srv, NonInteractiveOptions{Prompt: "hi"})
	if err != nil || code != 0 {
		t.Fatalf("code=%d err=%v", code, err)
	}
	if stdout != "hello there\n" {
		t.Fatalf("stdout = %q", stdout)
	}
	if srv.got.Input.Messages[0].Text() != "hi" {
		t.Fatalf("prompt not sent: %+v", srv.got.Input)
	}
}

func TestRunNonInteractiveReportsRunError(t *testing.T) {
	code, _, stderr, err := run(t, &fakeServer{err: errors.New("model down")}, NonInteractiveOptions{Prompt: "hi"})
	if err != nil || code != 1 || !strings.Contains(stderr, "model down") {
		t.Fatalf("code=%d err=%v stderr=%q", code, err, stderr)
	}
}

func TestRunNonInteractiveRejectsThread(t *testing.T) {
	code, _, _, err := run(t, &fakeServer{}, NonInteractiveOptions{Prompt: "hi", ThreadID: "t1"})
	if code != 1 || !errors.Is(err, ErrThreadResumeUnsupported) {
		t.Fatalf("code=%d err=%v", code, err)
	}
}
