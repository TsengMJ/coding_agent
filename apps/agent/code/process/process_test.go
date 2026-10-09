package process

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tsengmingchun/quill/apps/agent/backend"
	"github.com/tsengmingchun/quill/apps/agent/runtime"
	"github.com/tsengmingchun/quill/apps/agent/tool"
)

func shell(t *testing.T) (*backend.LocalShellBackend, string) {
	t.Helper()
	dir := t.TempDir()
	return backend.NewLocalShellBackend(backend.LocalShellOptions{FilesystemOptions: backend.FilesystemOptions{RootDir: dir, VirtualMode: true}}), dir
}

func TestProcessKeepsRunningAndReportsNewOutput(t *testing.T) {
	sh, _ := shell(t)
	m := NewManager()
	defer m.Close()
	p, err := m.Start("t1", sh, "echo ready; sleep 30")
	if err != nil {
		t.Fatal(err)
	}
	p.Wait(2 * time.Second)
	if out, _ := p.Read(); !strings.Contains(out, "ready") {
		t.Fatalf("output = %q", out)
	}
	if running, _ := p.Status(); !running {
		t.Fatal("process must outlive the start call")
	}
	if out, _ := p.Read(); out != "" {
		t.Fatalf("second read repeats output: %q", out)
	}
}

func TestProcessRunsInTheProjectRoot(t *testing.T) {
	sh, dir := shell(t)
	if err := os.WriteFile(filepath.Join(dir, "marker.txt"), []byte("here"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := NewManager()
	defer m.Close()
	p, _ := m.Start("t1", sh, "cat marker.txt")
	p.Wait(5 * time.Second)
	out, _ := p.Read()
	if running, code := p.Status(); running || code != 0 || !strings.Contains(out, "here") {
		t.Fatalf("running=%v code=%d out=%q", running, code, out)
	}
}

func TestStopKillsTheWholeProcessGroup(t *testing.T) {
	sh, dir := shell(t)
	m := NewManager()
	defer m.Close()
	p, _ := m.Start("t1", sh, "(sleep 1; touch child-survived) & sleep 30")
	if err := m.Stop("t1", p.ID); err != nil {
		t.Fatal(err)
	}
	if running, _ := p.Status(); running {
		t.Fatal("still running after Stop")
	}
	time.Sleep(1500 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(dir, "child-survived")); err == nil {
		t.Fatal("background child outlived stop")
	}
}

// remoteShell runs commands locally but, like docker exec, needs StopCommand
// to end them.
type remoteShell struct {
	*backend.LocalShellBackend
	stopped []string
}

func (r *remoteShell) StopCommand(cmd *exec.Cmd) {
	r.stopped = append(r.stopped, cmd.Args[len(cmd.Args)-1])
}

func TestStopAlsoStopsTheRemoteSideOfAStopper(t *testing.T) {
	sh, _ := shell(t)
	r := &remoteShell{LocalShellBackend: sh}
	m := NewManager()
	defer m.Close()
	p, err := m.Start("t1", r, "sleep 30")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Stop("t1", p.ID); err != nil {
		t.Fatal(err)
	}
	if len(r.stopped) != 1 || !strings.Contains(r.stopped[0], "sleep 30") {
		t.Fatalf("StopCommand calls = %q", r.stopped)
	}
}

func TestProcessesBelongToTheirThread(t *testing.T) {
	sh, _ := shell(t)
	m := NewManager()
	defer m.Close()
	p, _ := m.Start("t1", sh, "sleep 30")
	if _, ok := m.Get("t2", p.ID); ok {
		t.Fatal("another thread can see the process")
	}
	m.StopThread("t1")
	if running, _ := p.Status(); running {
		t.Fatal("StopThread left the process running")
	}
}

func TestRunningProcessesPerThreadAreCapped(t *testing.T) {
	sh, _ := shell(t)
	m := NewManager()
	defer m.Close()
	for i := 0; i < MaxRunning; i++ {
		if _, err := m.Start("t1", sh, "sleep 30"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := m.Start("t1", sh, "sleep 30"); err == nil || !strings.Contains(err.Error(), "stop_process") {
		t.Fatalf("err = %v, want a hint to stop one", err)
	}
	if _, err := m.Start("t2", sh, "sleep 30"); err != nil {
		t.Fatalf("other thread: %v", err)
	}
}

func TestOutputBeyondTheBufferIsCountedNotKept(t *testing.T) {
	var b buffer
	b.limit = 10
	_, _ = b.Write([]byte("0123456789abcdef"))
	out, omitted := b.read()
	if out != "6789abcdef" || omitted != 6 {
		t.Fatalf("out=%q omitted=%d", out, omitted)
	}
}

func run(t *testing.T, tl tool.Tool, args map[string]any) string {
	t.Helper()
	res, err := tl.Invoke(context.Background(), &tool.Runtime{Config: runtime.RunConfig{ThreadID: "t1"}}, args)
	if err != nil {
		t.Fatal(err)
	}
	var text []string
	for _, c := range res.Content {
		text = append(text, c.Text)
	}
	return strings.Join(text, "\n")
}

func TestToolsStartReadAndStopADevServer(t *testing.T) {
	sh, _ := shell(t)
	m := NewManager()
	defer m.Close()
	tools := map[string]tool.Tool{}
	for _, tl := range Tools(m, sh) {
		tools[tl.Name()] = tl
	}
	out := run(t, tools[StartName], map[string]any{"command": "echo listening on 5173; sleep 1; echo GET /; sleep 30", "wait_seconds": 1})
	if !strings.Contains(out, "p1") || !strings.Contains(out, "listening on 5173") || !strings.Contains(out, "running") {
		t.Fatalf("start = %q", out)
	}
	if out := run(t, tools[ReadName], map[string]any{"id": "p1", "wait_seconds": 2}); !strings.HasSuffix(out, "Output:\nGET /\n") {
		t.Fatalf("read = %q", out)
	}
	if out := run(t, tools[StopName], map[string]any{"id": "p1"}); !strings.Contains(out, "stopped") {
		t.Fatalf("stop = %q", out)
	}
	if out := run(t, tools[ReadName], map[string]any{"id": "p9"}); !strings.Contains(out, "no process p9") {
		t.Fatalf("unknown id = %q", out)
	}
}
