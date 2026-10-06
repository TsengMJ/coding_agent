package agent

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	core "github.com/tsengmingchun/quill/agent"
	"github.com/tsengmingchun/quill/backend"
	"github.com/tsengmingchun/quill/backend/ossandbox"
	"github.com/tsengmingchun/quill/code/codetools"
	"github.com/tsengmingchun/quill/code/config"
	"github.com/tsengmingchun/quill/llm"
	"github.com/tsengmingchun/quill/tool"
)

func sandboxSettings(mode ossandbox.Mode) *config.Settings {
	return &config.Settings{Sandbox: config.SandboxSettings{
		Mode: mode, Network: ossandbox.NetworkNone, OnUnavailable: config.SandboxUnavailableError,
		WritableRoots: []string{"/opt/extra"}, DenyRead: []string{"/opt/secret"},
	}}
}

func TestNewShellSandboxOff(t *testing.T) {
	for _, s := range []*config.Settings{nil, {}, sandboxSettings(ossandbox.FullAccess)} {
		if sb, _, err := NewShellSandbox(s, "/proj"); sb != nil || err != nil {
			t.Errorf("NewShellSandbox(%+v) = %v, %v", s, sb, err)
		}
	}
}

func TestNewShellSandboxWorkspaceWritePolicy(t *testing.T) {
	sb, p, err := NewShellSandbox(sandboxSettings(ossandbox.WorkspaceWrite), "/proj")
	if _, nativeErr := ossandbox.Native(); nativeErr != nil {
		if err == nil {
			t.Fatal("expected the platform sandbox error")
		}
		return
	}
	if err != nil || sb == nil {
		t.Fatalf("NewShellSandbox = %v, %v", sb, err)
	}
	if p.WritableRoots[0] != "/proj" || !slices.Contains(p.WritableRoots, "/opt/extra") {
		t.Errorf("WritableRoots = %v", p.WritableRoots)
	}
	if !slices.Contains(p.DenyRead, "/opt/secret") || !slices.Contains(p.DenyWrite, "/proj/.git/hooks") {
		t.Errorf("policy = %+v", p)
	}
}

func TestNewShellSandboxReadOnlyIgnoresExtraWritableRoots(t *testing.T) {
	_, p, _ := NewShellSandbox(sandboxSettings(ossandbox.ReadOnly), "/proj")
	if len(p.WritableRoots) != 0 {
		t.Fatalf("WritableRoots = %v", p.WritableRoots)
	}
}

// guarded reports whether g refuses call, and the refusal text.
func guarded(g sandboxFileGuard, name string, args map[string]any) (bool, string) {
	called := false
	next := func(context.Context, *core.ToolCallRequest) (*tool.Result, error) {
		called = true
		return &tool.Result{}, nil
	}
	res, _ := g.WrapToolCall(context.Background(), &core.ToolCallRequest{ToolCall: llm.ToolCall{Name: name, Args: args}}, next)
	if called {
		return false, ""
	}
	return true, res.Content[0].Text
}

func TestSandboxFileGuardReadOnly(t *testing.T) {
	g := sandboxFileGuard{mode: ossandbox.ReadOnly}
	for _, name := range []string{"write_file", "edit_file", "delete", codetools.ApplyPatchName} {
		if blocked, msg := guarded(g, name, map[string]any{"file_path": "/a.go"}); !blocked || !strings.Contains(msg, "read-only") {
			t.Errorf("%s: blocked %v, %q", name, blocked, msg)
		}
	}
	if blocked, _ := guarded(g, "read_file", map[string]any{"file_path": "/a.go"}); blocked {
		t.Error("read_file was blocked")
	}
}

func TestSandboxFileGuardWorkspaceWriteProtectsGit(t *testing.T) {
	g := sandboxFileGuard{mode: ossandbox.WorkspaceWrite}
	cases := []struct {
		name string
		args map[string]any
		want bool
	}{
		{"write_file", map[string]any{"file_path": "/.git/hooks/pre-commit"}, true},
		{"edit_file", map[string]any{"file_path": "/.git/config"}, true},
		{"write_file", map[string]any{"file_path": "/src/a.go"}, false},
		{"write_file", map[string]any{"file_path": "/.github/workflows/ci.yml"}, false},
		{codetools.ApplyPatchName, map[string]any{"patch": "*** Begin Patch\n*** Add File: .git/hooks/post-checkout\n+echo hi\n*** End Patch"}, true},
		{codetools.ApplyPatchName, map[string]any{"patch": "*** Begin Patch\n*** Add File: src/b.go\n+package b\n*** End Patch"}, false},
	}
	for _, c := range cases {
		if blocked, msg := guarded(g, c.name, c.args); blocked != c.want {
			t.Errorf("%s %v: blocked %v (%q), want %v", c.name, c.args, blocked, msg, c.want)
		}
	}
}

func TestCreateCLIAgentWithSandboxModes(t *testing.T) {
	if _, err := ossandbox.Native(); err != nil {
		t.Skip(err)
	}
	for _, mode := range []ossandbox.Mode{ossandbox.ReadOnly, ossandbox.WorkspaceWrite} {
		dir := t.TempDir()
		s := sandboxSettings(mode)
		_, err := CreateCLIAgent(context.Background(), Options{Settings: s, Model: llm.NewFakeModel(), Cwd: dir, AutoApprove: true, NoWebTools: true, NoVerifyGate: true})
		if err != nil {
			t.Errorf("%s: %v", mode, err)
		}
	}
}

// The verify gate's HEAD baseline stashes, resets and restores the work
// tree through the shell, so it must keep working in workspace-write.
func TestSandboxVerifyGateBaseline(t *testing.T) {
	if os.Getenv(ossandbox.ITEnvVar) != "1" {
		t.Skipf("set %s=1 to run sandbox integration tests", ossandbox.ITEnvVar)
	}
	dir := goRepo(t, nil)
	s := sandboxSettings(ossandbox.WorkspaceWrite)
	s.Sandbox.WritableRoots, s.Sandbox.DenyRead = nil, nil
	b, err := NewSandboxedBackend(dir, s)
	if err != nil {
		t.Fatal(err)
	}
	if !b.(*backend.LocalShellBackend).Sandboxed() {
		t.Fatal("backend is not sandboxed")
	}
	broken := strings.Replace(mathGo, "a - b", "a + b", 1)
	writeFile(t, filepath.Join(dir, "m.go"), broken)
	writeFile(t, filepath.Join(dir, "notes.txt"), "untracked work\n")

	cmd := runGate(t, newVerifyGate(b, nil), transcript(editStep, goTestFailed))
	if cmd == nil || !strings.Contains(cmd.Update.Messages[0].Text(), "passes on the unchanged HEAD") {
		t.Fatalf("gate = %+v", cmd)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "m.go")); string(got) != broken {
		t.Fatalf("changes were not restored: %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "notes.txt")); string(got) != "untracked work\n" {
		t.Fatalf("untracked file was not restored: %q", got)
	}
}
