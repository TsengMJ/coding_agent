package backend

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tsengmingchun/quill/backend/ossandbox"
)

type stubSandbox struct{ got string }

func (s *stubSandbox) Name() string { return "stub" }

func (s *stubSandbox) Command(ctx context.Context, p ossandbox.Policy, shellCmd string) (*exec.Cmd, error) {
	s.got = shellCmd
	return ossandbox.None().Command(ctx, p, "echo sandboxed")
}

type failingSandbox struct{}

func (failingSandbox) Name() string { return "failing" }

func (failingSandbox) Command(context.Context, ossandbox.Policy, string) (*exec.Cmd, error) {
	return nil, errors.New("bwrap not found")
}

func TestLocalShellRoutesThroughSandbox(t *testing.T) {
	sb := &stubSandbox{}
	b := newShell(t, LocalShellOptions{Sandbox: sb, Policy: ossandbox.Policy{Mode: ossandbox.WorkspaceWrite}})
	res := b.Execute(context.Background(), "echo hi", nil)
	if sb.got != "echo hi" || strings.TrimSpace(res.Output) != "sandboxed" {
		t.Fatalf("sandbox got %q, output %q", sb.got, res.Output)
	}
	if !b.Sandboxed() {
		t.Error("Sandboxed() = false")
	}
}

func TestLocalShellWithoutSandboxBypassesIt(t *testing.T) {
	sb := &stubSandbox{}
	b := newShell(t, LocalShellOptions{Sandbox: sb, Policy: ossandbox.Policy{Mode: ossandbox.WorkspaceWrite}})
	res := b.Execute(WithoutSandbox(context.Background()), "echo direct", nil)
	if sb.got != "" || strings.TrimSpace(res.Output) != "direct" {
		t.Fatalf("sandbox got %q, output %q", sb.got, res.Output)
	}
}

// envSandbox runs the real command unconfined so tests can inspect its environment.
type envSandbox struct{}

func (envSandbox) Name() string { return "env" }

func (envSandbox) Command(ctx context.Context, p ossandbox.Policy, shellCmd string) (*exec.Cmd, error) {
	return ossandbox.None().Command(ctx, p, shellCmd)
}

func TestLocalShellPointsSandboxedCommandsAtProxy(t *testing.T) {
	p := ossandbox.Policy{Mode: ossandbox.WorkspaceWrite, Network: ossandbox.NetworkAllowlist, Proxy: ossandbox.ProxyEndpoint{Port: 4321}}
	b := newShell(t, LocalShellOptions{Sandbox: envSandbox{}, Policy: p})
	if out := b.Execute(context.Background(), "echo $HTTPS_PROXY", nil).Output; strings.TrimSpace(out) != "http://127.0.0.1:4321" {
		t.Fatalf("sandboxed HTTPS_PROXY = %q", out)
	}
	if out := b.Execute(WithoutSandbox(context.Background()), "echo \"[$HTTPS_PROXY]\"", nil).Output; strings.TrimSpace(out) != "[]" {
		t.Fatalf("unsandboxed HTTPS_PROXY = %q", out)
	}
}

func TestLocalShellReportsSandboxError(t *testing.T) {
	b := newShell(t, LocalShellOptions{Sandbox: failingSandbox{}, Policy: ossandbox.Policy{Mode: ossandbox.WorkspaceWrite}})
	res := b.Execute(context.Background(), "echo hi", nil)
	if *res.ExitCode == 0 || !strings.Contains(res.Output, "bwrap not found") {
		t.Fatalf("unexpected response: %+v", res)
	}
}

func TestLocalShellFullAccessIsNotSandboxed(t *testing.T) {
	b := newShell(t, LocalShellOptions{Sandbox: &stubSandbox{}, Policy: ossandbox.Policy{Mode: ossandbox.FullAccess}})
	if b.Sandboxed() {
		t.Fatal("full-access reported as sandboxed")
	}
}

func newSandboxedShell(t *testing.T) *LocalShellBackend {
	t.Helper()
	if os.Getenv(ossandbox.ITEnvVar) != "1" {
		t.Skipf("set %s=1 to run sandbox integration tests", ossandbox.ITEnvVar)
	}
	sb, err := ossandbox.Native()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	return NewLocalShellBackend(LocalShellOptions{
		FilesystemOptions: FilesystemOptions{RootDir: root, VirtualMode: true},
		InheritEnv:        true,
		Sandbox:           sb,
		Policy:            ossandbox.Policy{Mode: ossandbox.WorkspaceWrite, WritableRoots: []string{root}, Network: ossandbox.NetworkNone},
	})
}

func TestSandboxedShellConfinesWrites(t *testing.T) {
	b := newSandboxedShell(t)
	outside := filepath.Join(t.TempDir(), "x")
	res := b.Execute(context.Background(), "echo ok > in.txt && touch "+ossandbox.ShellJoin([]string{outside}), nil)
	if *res.ExitCode == 0 {
		t.Fatalf("write outside the root succeeded: %+v", res)
	}
	if _, err := os.Stat(filepath.Join(b.root, "in.txt")); err != nil {
		t.Fatalf("write inside the root failed: %v (%s)", err, res.Output)
	}
}

// The pid namespace on Linux makes pids printed inside the sandbox
// meaningless outside, so this checks that a child never gets to act.
func TestSandboxedShellTimeoutKillsBackgroundChildren(t *testing.T) {
	b := newSandboxedShell(t)
	timeout := 500 * time.Millisecond
	res := b.Execute(context.Background(), "(sleep 2; touch late) & sleep 30", &timeout)
	if *res.ExitCode != timeoutExitCode {
		t.Fatalf("unexpected response: %+v", res)
	}
	time.Sleep(3 * time.Second)
	if _, err := os.Stat(filepath.Join(b.root, "late")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("background child survived the timeout: %v", err)
	}
}
