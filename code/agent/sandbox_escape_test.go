package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	core "github.com/tsengmingchun/quill/agent"
	"github.com/tsengmingchun/quill/backend"
	"github.com/tsengmingchun/quill/backend/ossandbox"
	"github.com/tsengmingchun/quill/code/codetools"
	"github.com/tsengmingchun/quill/llm"
	rt "github.com/tsengmingchun/quill/runtime"
	"github.com/tsengmingchun/quill/tool"
)

func execCall(args map[string]any) llm.ToolCall {
	return llm.ToolCall{ID: "c", Name: executeToolName, Args: args}
}

func TestReviewSandboxedShell(t *testing.T) {
	cfg := DefaultInterruptOn()
	reviewSandboxedShell(cfg)
	review := cfg[executeToolName]
	if review.When(execCall(map[string]any{"command": "go test ./..."})) {
		t.Error("plain sandboxed command is reviewed")
	}
	for _, args := range []map[string]any{
		{"command": "git reset --hard"},
		{"command": "ls", sandboxArg: false},
	} {
		if !review.AlwaysWhen(execCall(args)) {
			t.Errorf("%v is not always reviewed", args)
		}
	}
	if review.AlwaysWhen(execCall(map[string]any{"command": "ls", sandboxArg: true})) {
		t.Error("sandbox: true is reviewed")
	}
	for _, name := range []string{codetools.RunTestsName, codetools.DiagnosticsName} {
		if _, ok := cfg[name]; ok {
			t.Errorf("%s still reviewed", name)
		}
	}
	if _, ok := cfg["write_file"]; !ok {
		t.Error("write_file lost its review")
	}
}

func TestDescribeExecuteMarksEscapes(t *testing.T) {
	if d := describeExecute(execCall(map[string]any{"command": "curl x", sandboxArg: false})); !strings.Contains(d, "OUTSIDE the sandbox") {
		t.Fatalf("description = %q", d)
	}
	if d := describeExecute(execCall(map[string]any{"command": "ls"})); strings.Contains(d, "OUTSIDE") {
		t.Fatalf("description = %q", d)
	}
}

func execResult(code int, text string) *tool.Result {
	return &tool.Result{Content: []llm.ContentBlock{llm.TextBlock(text)}, Artifact: backend.ExecuteArtifact{ExitCode: &code}}
}

func wrapExecute(m sandboxedShell, args map[string]any, res *tool.Result) (*tool.Result, context.Context) {
	var got context.Context
	out, _ := m.WrapToolCall(context.Background(), &core.ToolCallRequest{ToolCall: execCall(args)},
		func(ctx context.Context, _ *core.ToolCallRequest) (*tool.Result, error) {
			got = ctx
			return res, nil
		})
	return out, got
}

func TestSandboxedShellRefusesEscapes(t *testing.T) {
	escape := map[string]any{"command": "curl x", sandboxArg: false}
	for _, m := range []sandboxedShell{
		{mode: ossandbox.WorkspaceWrite, allowUnsandboxed: false},
		{mode: ossandbox.WorkspaceWrite, allowUnsandboxed: true, noReviewer: true},
	} {
		res, ctx := wrapExecute(m, escape, execResult(0, "ran"))
		if ctx != nil || !res.IsError || !strings.HasPrefix(res.Content[0].Text, "Not run:") {
			t.Errorf("%+v: ran %v, result %+v", m, ctx != nil, res)
		}
	}
}

type recordingSandbox struct{ used bool }

func (s *recordingSandbox) Name() string { return "recording" }

func (s *recordingSandbox) Command(ctx context.Context, p ossandbox.Policy, cmd string) (*exec.Cmd, error) {
	s.used = true
	return ossandbox.None().Command(ctx, p, cmd)
}

func TestSandboxedShellRunsApprovedEscapeUnconfined(t *testing.T) {
	sb := &recordingSandbox{}
	b := backend.NewLocalShellBackend(backend.LocalShellOptions{
		FilesystemOptions: backend.FilesystemOptions{RootDir: t.TempDir(), VirtualMode: true},
		Sandbox:           sb, Policy: ossandbox.Policy{Mode: ossandbox.WorkspaceWrite},
	})
	m := sandboxedShell{mode: ossandbox.WorkspaceWrite, allowUnsandboxed: true}
	res, ctx := wrapExecute(m, map[string]any{"command": "echo hi", sandboxArg: false}, execResult(0, "hi"))
	b.Execute(ctx, "echo hi", nil)
	if sb.used {
		t.Fatal("escape still ran in the sandbox")
	}
	if a := res.Artifact.(backend.ExecuteArtifact); a.Sandbox != unsandboxedLabel {
		t.Fatalf("artifact = %+v", a)
	}
}

func TestSandboxedShellExplainsDenials(t *testing.T) {
	m := sandboxedShell{mode: ossandbox.WorkspaceWrite, allowUnsandboxed: true}
	res, _ := wrapExecute(m, map[string]any{"command": "touch ~/x"}, execResult(1, "touch: /Users/u/x: Operation not permitted"))
	a := res.Artifact.(backend.ExecuteArtifact)
	if !a.SandboxBlocked || a.Sandbox != string(ossandbox.WorkspaceWrite) {
		t.Fatalf("artifact = %+v", a)
	}
	if hint := res.Content[len(res.Content)-1].Text; !strings.Contains(hint, "[sandbox]") || !strings.Contains(hint, `"sandbox": false`) {
		t.Fatalf("hint = %q", hint)
	}
}

func TestSandboxedShellHintOmitsEscapeWithoutReviewer(t *testing.T) {
	m := sandboxedShell{mode: ossandbox.WorkspaceWrite, allowUnsandboxed: true, noReviewer: true}
	res, _ := wrapExecute(m, map[string]any{"command": "curl x"}, execResult(6, "curl: (6) Could not resolve host: x"))
	if hint := res.Content[len(res.Content)-1].Text; !strings.Contains(hint, "[sandbox]") || strings.Contains(hint, `"sandbox": false`) {
		t.Fatalf("hint = %q", hint)
	}
}

func TestSandboxedShellExplainsAllowlistRefusals(t *testing.T) {
	m := sandboxedShell{mode: ossandbox.WorkspaceWrite, network: ossandbox.NetworkAllowlist, allowUnsandboxed: true}
	res, _ := wrapExecute(m, map[string]any{"command": "curl https://example.com"},
		execResult(56, "curl: (56) CONNECT tunnel failed, response 403"))
	if !res.Artifact.(backend.ExecuteArtifact).SandboxBlocked {
		t.Fatal("proxy refusal not detected")
	}
	if hint := res.Content[len(res.Content)-1].Text; !strings.Contains(hint, "sandbox.allowed_domains") || strings.Contains(hint, "no network access") {
		t.Fatalf("hint = %q", hint)
	}
}

func TestSandboxedShellNoHintForOrdinaryFailures(t *testing.T) {
	m := sandboxedShell{mode: ossandbox.WorkspaceWrite, allowUnsandboxed: true}
	for _, res := range []*tool.Result{
		execResult(1, "--- FAIL: TestAdd\nFAIL"),
		execResult(1, "cat: x: Permission denied"),
		execResult(0, "grep found: Operation not permitted"),
	} {
		out, _ := wrapExecute(m, map[string]any{"command": "x"}, res)
		if len(out.Content) != 1 || out.Artifact.(backend.ExecuteArtifact).SandboxBlocked {
			t.Errorf("hint added to %q", out.Content[0].Text)
		}
	}
}

func TestEscapableExecuteSchema(t *testing.T) {
	base := &tool.Func{ToolName: "execute", ToolDescription: "Run a command.",
		ToolSchema: tool.Schema{"type": "object", "properties": map[string]any{"command": map[string]any{"type": "string"}}}}
	props := escapableExecute{base}.Schema()["properties"].(map[string]any)
	if _, ok := props[sandboxArg]; !ok || props["command"] == nil {
		t.Fatalf("properties = %v", props)
	}
	if _, leaked := base.Schema()["properties"].(map[string]any)[sandboxArg]; leaked {
		t.Fatal("original schema was modified")
	}
}

func TestHeadlessSandboxedAgentRefusesEscape(t *testing.T) {
	if _, err := ossandbox.Native(); err != nil {
		t.Skip(err)
	}
	dir := t.TempDir()
	model := llm.NewFakeModel(
		llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{execCall(map[string]any{"command": "echo hi", sandboxArg: false})}},
		llm.NewAssistantMessage("ok"),
	)
	s := sandboxSettings(ossandbox.WorkspaceWrite)
	s.Sandbox.AllowUnsandboxed = true
	a, err := CreateCLIAgent(context.Background(), Options{Settings: s, Model: model, Cwd: dir, AutoApprove: true, NoWebTools: true, NoVerifyGate: true})
	if err != nil {
		t.Fatal(err)
	}
	st, err := a.Invoke(context.Background(), core.Input{Messages: []llm.Message{llm.NewUserMessage("go")}}, rt.RunConfig{})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range st.Messages {
		if m.Role == llm.RoleTool {
			if !strings.Contains(m.Text(), "nobody can approve") {
				t.Fatalf("result = %q", m.Text())
			}
			return
		}
	}
	t.Fatal("no tool result")
}

// A real denial reaches the model with the hint, and the tool message
// metadata (which the trace records) carries the sandbox mode.
func TestSandboxAgentReportsBlockedCommand(t *testing.T) {
	if os.Getenv(ossandbox.ITEnvVar) != "1" {
		t.Skipf("set %s=1 to run sandbox integration tests", ossandbox.ITEnvVar)
	}
	dir := t.TempDir()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip(err)
	}
	// Temp dirs are writable in workspace-write; the home directory is not.
	outside := filepath.Join(home, ".quill-sandbox-e2e-probe")
	t.Cleanup(func() { os.Remove(outside) })
	model := llm.NewFakeModel(
		llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{execCall(map[string]any{"command": "touch " + ossandbox.ShellJoin([]string{outside})})}},
		llm.NewAssistantMessage("ok"),
	)
	s := sandboxSettings(ossandbox.WorkspaceWrite)
	s.Sandbox.WritableRoots, s.Sandbox.AllowUnsandboxed = nil, true
	a, err := CreateCLIAgent(context.Background(), Options{Settings: s, Model: model, Cwd: dir, AutoApprove: true, NoWebTools: true, NoVerifyGate: true})
	if err != nil {
		t.Fatal(err)
	}
	st, err := a.Invoke(context.Background(), core.Input{Messages: []llm.Message{llm.NewUserMessage("go")}}, rt.RunConfig{})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range st.Messages {
		if m.Role != llm.RoleTool {
			continue
		}
		if !strings.Contains(m.Text(), "[sandbox]") {
			t.Fatalf("result = %q", m.Text())
		}
		art, _ := m.Metadata["artifact"].(backend.ExecuteArtifact)
		if art.Sandbox != string(ossandbox.WorkspaceWrite) || !art.SandboxBlocked {
			t.Fatalf("artifact = %#v", m.Metadata["artifact"])
		}
		if _, err := os.Stat(outside); err == nil {
			t.Fatal("the command escaped the sandbox")
		}
		return
	}
	t.Fatal("no tool result")
}
