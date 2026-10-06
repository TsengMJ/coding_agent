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
	"github.com/tsengmingchun/quill/middleware/permissions"
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

func TestSandboxPermissions(t *testing.T) {
	if perms := sandboxPermissions(sandboxSettings(ossandbox.FullAccess)); perms != nil {
		t.Errorf("full-access perms = %v", perms)
	}
	ro := sandboxPermissions(sandboxSettings(ossandbox.ReadOnly))
	if permissions.Check(ro, permissions.OpWrite, "/src/a.go") != permissions.ModeDeny ||
		permissions.Check(ro, permissions.OpRead, "/src/a.go") == permissions.ModeDeny {
		t.Errorf("read-only perms = %+v", ro)
	}
	ww := sandboxPermissions(sandboxSettings(ossandbox.WorkspaceWrite))
	for path, want := range map[string]permissions.Mode{
		"/.git/hooks/pre-commit": permissions.ModeDeny,
		"/.git/config":           permissions.ModeDeny,
		"/src/a.go":              permissions.ModeAllow,
	} {
		if got := permissions.Check(ww, permissions.OpWrite, path); got != want && !(want == permissions.ModeAllow && got == "") {
			t.Errorf("write %s = %q, want %q", path, got, want)
		}
	}
}

func TestReadOnlyGuardBlocksApplyPatchOnly(t *testing.T) {
	called := false
	next := func(context.Context, *core.ToolCallRequest) (*tool.Result, error) {
		called = true
		return &tool.Result{}, nil
	}
	res, _ := readOnlyGuard{}.WrapToolCall(context.Background(), &core.ToolCallRequest{ToolCall: llm.ToolCall{Name: codetools.ApplyPatchName}}, next)
	if called || !res.IsError || !strings.Contains(res.Content[0].Text, "read-only") {
		t.Fatalf("apply_patch: called %v, result %+v", called, res)
	}
	if _, _ = (readOnlyGuard{}).WrapToolCall(context.Background(), &core.ToolCallRequest{ToolCall: llm.ToolCall{Name: "read_file"}}, next); !called {
		t.Fatal("read_file was blocked")
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
