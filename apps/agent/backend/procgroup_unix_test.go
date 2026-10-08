//go:build unix

package backend

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestLocalShellExecuteKillsBackgroundChildren(t *testing.T) {
	b := newShell(t, LocalShellOptions{})
	start := time.Now()
	res := b.Execute(context.Background(), "sleep 30 & echo $!", nil)
	if time.Since(start) > 10*time.Second {
		t.Fatalf("execute blocked on background child: %s", time.Since(start))
	}
	pid, err := strconv.Atoi(strings.TrimSpace(res.Output))
	if err != nil {
		t.Fatalf("output = %q", res.Output)
	}
	assertProcessGone(t, pid)
}

func TestLocalShellExecuteTimeoutKillsProcessGroup(t *testing.T) {
	b := newShell(t, LocalShellOptions{})
	timeout := 300 * time.Millisecond
	res := b.Execute(context.Background(), "sh -c 'echo $$ > pid; sleep 30'", &timeout)
	if *res.ExitCode != timeoutExitCode {
		t.Fatalf("unexpected response: %+v", res)
	}
	raw, err := os.ReadFile(filepath.Join(b.root, "pid"))
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	assertProcessGone(t, pid)
}

func assertProcessGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); err != nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("process %d still running", pid)
}
