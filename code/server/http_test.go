package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"iter"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tsengmingchun/deepagents_golang/code/commands"
	"github.com/tsengmingchun/deepagents_golang/code/sessions"
	"github.com/tsengmingchun/deepagents_golang/code/storage"
	"github.com/tsengmingchun/deepagents_golang/code/trace"
	"github.com/tsengmingchun/deepagents_golang/llm"
)

// funcServer runs fn for every Run; the other methods are unused here.
type funcServer struct {
	run func(ctx context.Context, yield func(Event, error) bool)
}

func (f funcServer) Run(ctx context.Context, _ RunRequest) iter.Seq2[Event, error] {
	return func(yield func(Event, error) bool) { f.run(ctx, yield) }
}

func (funcServer) GetThread(context.Context, string) (*ThreadState, error) { return nil, nil }

func (funcServer) ListThreads(context.Context, sessions.Filter) ([]sessions.Info, error) {
	return nil, nil
}

func (funcServer) RenameThread(context.Context, string, string) (*sessions.Info, error) {
	return nil, nil
}

func (funcServer) Compact(context.Context, string) (*CompactResult, error) { return nil, nil }

func (funcServer) GetTrace(context.Context, string) (*trace.Trace, error) { return nil, nil }

func (funcServer) GetLLMCall(context.Context, string, int) (*trace.LLMCallDetail, error) {
	return nil, nil
}

func (funcServer) GetUsage(context.Context, string) (*ThreadUsage, error) { return nil, nil }

func (funcServer) Close() error { return nil }

func startServe(t *testing.T, srv Server) (string, context.CancelFunc, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	addrc := make(chan net.Addr, 1)
	errc := make(chan error, 1)
	go func() { errc <- Serve(ctx, "127.0.0.1:0", srv, func(a net.Addr) { addrc <- a }) }()
	select {
	case a := <-addrc:
		return "http://" + a.String(), cancel, errc
	case err := <-errc:
		t.Fatalf("serve: %v", err)
	}
	return "", cancel, errc
}

func postRun(t *testing.T, base string) *http.Response {
	t.Helper()
	body, _ := json.Marshal(RunRequest{ThreadID: "t", Cwd: "/", Messages: nil})
	resp, err := http.Post(base+"/runs", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestServeRefreshesProjectContextBetweenGUIRuns(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	cwd := t.TempDir()
	if out, err := exec.Command("git", "-C", cwd, "init", "-q", "-b", "trunk").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	model := llm.NewFakeModel(llm.NewAssistantMessage("first"), llm.NewAssistantMessage("second"))
	svc, err := New(context.Background(), Config{Storage: storage.NewMemory(), Model: model, NoVerifyGate: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	base, stop, _ := startServe(t, svc)
	t.Cleanup(stop)
	post := func(text string) {
		body, _ := json.Marshal(userReq("gui", cwd, text))
		resp, err := http.Post(base+"/runs", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(resp.Body)
		if err != nil || resp.StatusCode != http.StatusOK || !bytes.Contains(data, []byte(`"event":"done"`)) {
			t.Fatalf("run response: status %d, err %v, body %s", resp.StatusCode, err, data)
		}
	}
	post("one")
	if err := os.WriteFile(filepath.Join(cwd, "go.mod"), []byte("module x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	post("two")
	reqs := model.Requests()
	if len(reqs) != 2 || strings.Contains(reqs[0].Messages[0].Text(), "Project files: go.mod") || !strings.Contains(reqs[1].Messages[0].Text(), "Project files: go.mod") {
		t.Fatalf("GUI requests did not refresh project markers")
	}
}

func TestServeLetsInFlightRunFinishOnShutdown(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	srv := funcServer{run: func(ctx context.Context, yield func(Event, error) bool) {
		if !yield(Event{Kind: EventDelta, Text: "working"}, nil) {
			return
		}
		close(started)
		select {
		case <-release:
			yield(Event{Kind: EventDelta, Text: "finished"}, nil)
		case <-ctx.Done():
		}
	}}
	base, stop, errc := startServe(t, srv)
	resp := postRun(t, base)
	defer resp.Body.Close()
	<-started
	stop()
	time.AfterFunc(100*time.Millisecond, func() { close(release) })

	var kinds []string
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		var ev Event
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			t.Fatal(err)
		}
		kinds = append(kinds, string(ev.Kind)+":"+ev.Text)
	}
	if got := strings.Join(kinds, ","); got != "delta:working,delta:finished,done:" {
		t.Fatalf("events = %s", got)
	}
	select {
	case err := <-errc:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return")
	}
}

func TestServeCancelsRunsAfterGrace(t *testing.T) {
	old := shutdownGrace
	shutdownGrace = 200 * time.Millisecond
	t.Cleanup(func() { shutdownGrace = old })

	started := make(chan struct{})
	cancelled := make(chan struct{})
	srv := funcServer{run: func(ctx context.Context, yield func(Event, error) bool) {
		yield(Event{Kind: EventDelta, Text: "working"}, nil)
		close(started)
		<-ctx.Done()
		close(cancelled)
	}}
	base, stop, errc := startServe(t, srv)
	resp := postRun(t, base)
	defer resp.Body.Close()
	<-started
	stop()
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("run was not cancelled")
	}
	select {
	case <-errc:
	case <-time.After(10 * time.Second):
		t.Fatal("Serve did not return")
	}
}

func TestServeStopsRunWhenClientStopsReading(t *testing.T) {
	old := eventWriteTimeout
	eventWriteTimeout = 200 * time.Millisecond
	t.Cleanup(func() { eventWriteTimeout = old })

	stopped := make(chan struct{})
	chunk := strings.Repeat("x", 64<<10)
	srv := funcServer{run: func(ctx context.Context, yield func(Event, error) bool) {
		defer close(stopped)
		for {
			if !yield(Event{Kind: EventDelta, Text: chunk}, nil) {
				return
			}
		}
	}}
	base, stop, _ := startServe(t, srv)
	t.Cleanup(stop)

	conn, err := net.Dial("tcp", strings.TrimPrefix(base, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	body := `{"thread_id":"t","cwd":"/"}`
	req := "POST /runs HTTP/1.1\r\nHost: x\r\nContent-Type: application/json\r\nContent-Length: " +
		strconv.Itoa(len(body)) + "\r\n\r\n" + body
	if _, err := conn.Write([]byte(req)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-stopped:
	case <-time.After(20 * time.Second):
		t.Fatal("run kept going for a client that never reads")
	}
}

func TestCommandsRouteListsVisibleDCodeCommands(t *testing.T) {
	rec := httptest.NewRecorder()
	NewHandler(funcServer{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/commands", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var got []commands.Command
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != len(commands.Visible(commands.DCode)) {
		t.Fatalf("got %d commands", len(got))
	}
	for _, c := range got {
		if c.Hidden {
			t.Fatalf("hidden command listed: %+v", c)
		}
	}
	if _, ok := commands.Find(got, "/compact"); !ok {
		t.Fatalf("missing /compact: %+v", got)
	}
}
