package client

import (
	"bufio"
	"bytes"
	"context"
	"iter"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tsengmingchun/quill/code/server"
	"github.com/tsengmingchun/quill/llm"
	"github.com/tsengmingchun/quill/middleware/hitl"
)

func actions(names ...string) []hitl.ActionRequest {
	out := make([]hitl.ActionRequest, len(names))
	for i, n := range names {
		out[i] = hitl.ActionRequest{Name: n, Args: map[string]any{"file_path": "/f" + n, "command": "ls", "content": "x"}}
	}
	return out
}

func decide(t *testing.T, input string, reqs []hitl.ActionRequest) ([]hitl.Decision, string) {
	t.Helper()
	var out bytes.Buffer
	d, err := PromptApprover{In: bufio.NewReader(strings.NewReader(input)), Out: &out}.Decide(context.Background(), reqs)
	if err != nil {
		t.Fatal(err)
	}
	return d, out.String()
}

func TestPromptApproverParsesAnswers(t *testing.T) {
	d, out := decide(t, "y\n\nuse b.txt\n", actions("write_file", "execute", "edit_file"))
	if len(d) != 3 || d[0].Type != hitl.DecisionApprove || d[1].Type != hitl.DecisionReject || d[1].Message != "" ||
		d[2].Type != hitl.DecisionReject || d[2].Message != "use b.txt" {
		t.Fatalf("decisions = %+v", d)
	}
	for _, want := range []string{"[approve 1/3] write_file /fwrite_file", "$ ls", "approve?"} {
		if !strings.Contains(out, want) {
			t.Fatalf("prompt missing %q:\n%s", want, out)
		}
	}
}

func TestPromptApproverApproveAll(t *testing.T) {
	d, _ := decide(t, "a\n", actions("write_file", "execute"))
	if len(d) != 2 || d[0].Type != hitl.DecisionApprove || d[1].Type != hitl.DecisionApprove {
		t.Fatalf("decisions = %+v", d)
	}
}

func TestPromptApproverFailsOnEOF(t *testing.T) {
	_, err := PromptApprover{In: bufio.NewReader(strings.NewReader("")), Out: &bytes.Buffer{}}.Decide(context.Background(), actions("execute"))
	if err == nil {
		t.Fatal("want error on EOF")
	}
}

func TestActionPreview(t *testing.T) {
	edit := hitl.ActionRequest{Name: "edit_file", Args: map[string]any{"old_string": "a", "new_string": "b\nc"}}
	if got := ActionPreview(edit); got != "- a\n+ b\n+ c" {
		t.Fatalf("edit preview = %q", got)
	}
	long := hitl.ActionRequest{Name: "write_file", Args: map[string]any{"content": strings.Repeat("line\n", 20)}}
	if got := ActionPreview(long); !strings.HasSuffix(got, "... (8 more lines)") {
		t.Fatalf("write preview = %q", got)
	}
}

func TestActionTitleAndPreviewForDelete(t *testing.T) {
	req := hitl.ActionRequest{Name: "delete", Args: map[string]any{"file_path": "/build"}}
	if got := ActionTitle(req); got != "delete /build" {
		t.Fatalf("title = %q", got)
	}
	if got := ActionPreviewIn(req, t.TempDir()); !strings.HasPrefix(got, "rm -r /build") {
		t.Fatalf("preview = %q", got)
	}
}

func TestActionTitleAndPreviewForWebTools(t *testing.T) {
	req := hitl.ActionRequest{Name: "web_search", Args: map[string]any{"query": "go news"}, Description: "Query: go news"}
	if got := ActionTitle(req); got != "web_search go news" {
		t.Fatalf("title = %q", got)
	}
	if got := ActionPreview(req); got != "Query: go news" {
		t.Fatalf("preview = %q", got)
	}
	fetch := hitl.ActionRequest{Name: "fetch_url", Args: map[string]any{"url": "https://go.dev"}}
	if got := ActionTitle(fetch); got != "fetch_url https://go.dev" {
		t.Fatalf("title = %q", got)
	}
}

// scriptedServer yields one scripted event list per Run call.
type scriptedServer struct {
	fakeServer
	runs [][]server.Event
}

func (s *scriptedServer) Run(_ context.Context, req server.RunRequest) iter.Seq2[server.Event, error] {
	s.got = append(s.got, req)
	events := s.runs[len(s.got)-1]
	return func(yield func(server.Event, error) bool) {
		for _, ev := range events {
			if !yield(ev, nil) {
				return
			}
		}
	}
}

func TestRunNonInteractiveResumesWithApproverDecisions(t *testing.T) {
	srv := &scriptedServer{runs: [][]server.Event{
		{{Kind: server.EventInterrupt, Interrupts: []server.Interrupt{{ID: "after_model", ActionRequests: actions("execute")}}}},
		{msg(llm.NewAssistantMessage("finished"))},
	}}
	code, stdout, _, err := runHeadless(t, srv, NonInteractiveOptions{Prompt: "go", ThreadID: "t1", Approver: RejectApprover{Reason: "nope"}})
	if err != nil || code != 0 {
		t.Fatalf("code=%d err=%v", code, err)
	}
	if len(srv.got) != 2 {
		t.Fatalf("runs = %d", len(srv.got))
	}
	resume := srv.got[1]
	if resume.ThreadID != "t1" || resume.Resume == nil || len(resume.Resume.Decisions) != 1 ||
		resume.Resume.Decisions[0].Type != hitl.DecisionReject || resume.Resume.Decisions[0].Message != "nope" || len(resume.Messages) != 0 {
		t.Fatalf("resume request = %+v", resume)
	}
	if stdout != "finished\n" {
		t.Fatalf("stdout = %q", stdout)
	}
}

func TestRunNonInteractiveWithoutApproverRejectsWithHint(t *testing.T) {
	srv := &scriptedServer{runs: [][]server.Event{
		{{Kind: server.EventInterrupt, Interrupts: []server.Interrupt{{ActionRequests: actions("write_file")}}}},
		{},
	}}
	if _, _, _, err := runHeadless(t, srv, NonInteractiveOptions{Prompt: "go", ThreadID: "t1"}); err != nil {
		t.Fatal(err)
	}
	if d := srv.got[1].Resume.Decisions[0]; d.Type != hitl.DecisionReject || d.Message != NoReviewerReason {
		t.Fatalf("decision = %+v", d)
	}
}

func TestRemoteInterruptAndResumeRoundTrip(t *testing.T) {
	cwd := t.TempDir()
	r := remoteFor(t, newService(t,
		llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "w", Name: "write_file",
			Args: map[string]any{"file_path": "/a.txt", "content": "A"}}}},
		llm.NewAssistantMessage("wrote it"),
	))
	var stdout, stderr bytes.Buffer
	code, err := RunNonInteractive(context.Background(), r, NonInteractiveOptions{
		Prompt: "write", ThreadID: "t1", Cwd: cwd,
		Approver: PromptApprover{In: bufio.NewReader(strings.NewReader("y\n")), Out: &stderr},
		Stdout:   &stdout, Stderr: &stderr,
	})
	if err != nil || code != 0 {
		t.Fatalf("code=%d err=%v stderr=%s", code, err, stderr.String())
	}
	if got, _ := os.ReadFile(filepath.Join(cwd, "a.txt")); string(got) != "A" {
		t.Fatalf("a.txt = %q", got)
	}
	if !strings.Contains(stderr.String(), "[approve 1/1] write_file /a.txt") || stdout.String() != "wrote it\n" {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}
