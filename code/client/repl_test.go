package client

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tsengmingchun/deepagents_golang/code/sessions"
	"github.com/tsengmingchun/deepagents_golang/llm"
)

func TestREPLKeepsThreadAcrossTurnsAndNewStartsAnother(t *testing.T) {
	cwd := t.TempDir()
	svc := newService(t, llm.NewAssistantMessage("one"), llm.NewAssistantMessage("two"), llm.NewAssistantMessage("three"))
	var out, errw bytes.Buffer
	err := RunInteractive(context.Background(), svc, InteractiveOptions{
		Cwd: cwd, In: strings.NewReader("first\nsecond\n/new\nthird\n/quit\n"), Out: &out, Err: &errw,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"one\n", "two\n", "three\n"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output missing %q:\n%s", want, out.String())
		}
	}
	list, err := svc.ListThreads(context.Background(), sessions.Filter{Cwd: cwd})
	if err != nil || len(list) != 2 {
		t.Fatalf("threads = %+v, %v", list, err)
	}
	counts := map[int]bool{list[0].MessageCount: true, list[1].MessageCount: true}
	if !counts[4] || !counts[2] {
		t.Fatalf("message counts = %+v (want one thread with 4, one with 2)", list)
	}
}

func TestREPLAsksForApprovalInline(t *testing.T) {
	cwd := t.TempDir()
	svc := newService(t,
		llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "w", Name: "write_file",
			Args: map[string]any{"file_path": "/a.txt", "content": "A"}}}},
		llm.NewAssistantMessage("done"))
	var out, errw bytes.Buffer
	err := RunInteractive(context.Background(), svc, InteractiveOptions{
		Cwd: cwd, In: strings.NewReader("write it\ny\n/quit\n"), Out: &out, Err: &errw,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(cwd, "a.txt")); string(got) != "A" {
		t.Fatalf("a.txt = %q; stderr:\n%s", got, errw.String())
	}
	if !strings.Contains(errw.String(), "[approve 1/1] write_file /a.txt") {
		t.Fatalf("no approval prompt:\n%s", errw.String())
	}
}

func TestREPLYoloSkipsApproval(t *testing.T) {
	cwd := t.TempDir()
	svc := newService(t,
		llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "w", Name: "write_file",
			Args: map[string]any{"file_path": "/a.txt", "content": "A"}}}},
		llm.NewAssistantMessage("done"))
	var out, errw bytes.Buffer
	err := RunInteractive(context.Background(), svc, InteractiveOptions{
		Cwd: cwd, In: strings.NewReader("/yolo\nwrite it\n"), Out: &out, Err: &errw,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(cwd, "a.txt")); string(got) != "A" {
		t.Fatalf("a.txt = %q", got)
	}
	if strings.Contains(errw.String(), "approve?") || !strings.Contains(out.String(), "auto-approve on") {
		t.Fatalf("stdout=%q stderr=%q", out.String(), errw.String())
	}
}

func TestREPLResumeUnknownThreadReportsError(t *testing.T) {
	svc := newService(t)
	var out, errw bytes.Buffer
	err := RunInteractive(context.Background(), svc, InteractiveOptions{
		Cwd: t.TempDir(), In: strings.NewReader("/resume nope\n/bogus\n/quit\n"), Out: &out, Err: &errw,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errw.String(), "thread nope not found") || !strings.Contains(errw.String(), "unknown command /bogus") {
		t.Fatalf("stderr = %q", errw.String())
	}
}
