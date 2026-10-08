package agent

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	core "github.com/tsengmingchun/quill/agent"
	"github.com/tsengmingchun/quill/backend"
	"github.com/tsengmingchun/quill/code/codetools"
	"github.com/tsengmingchun/quill/llm"
	rt "github.com/tsengmingchun/quill/runtime"
	"github.com/tsengmingchun/quill/tool"
)

// microWorkspace is a guarded shell backend over dir with the given files
// already read, as the agent's backend is after read_file.
func microWorkspace(t *testing.T, dir string, files map[string]string) backend.Backend {
	t.Helper()
	for name, content := range files {
		writeFile(t, filepath.Join(dir, name), content)
	}
	b := backend.WithVersionGuard(NewBackend(dir))
	for name := range files {
		if r := b.Read(context.Background(), "/"+name, 0, 0); r.Error != "" {
			t.Fatal(r.Error)
		}
	}
	return b
}

// runMicro runs one edit through MicroCheck; the edit itself is done by
// the real filesystem tool or apply_patch.
func runMicro(t *testing.T, m MicroCheck, b backend.Backend, name string, args map[string]any) *tool.Result {
	t.Helper()
	return runMicroAfter(t, m, b, nil, name, args)
}

// runMicroAfter is runMicro with history as the conversation so far.
func runMicroAfter(t *testing.T, m MicroCheck, b backend.Backend, history []llm.Message, name string, args map[string]any) *tool.Result {
	t.Helper()
	var target tool.Tool
	if name == codetools.ApplyPatchName {
		target = codetools.NewApplyPatch(b)
	}
	next := func(ctx context.Context, req *core.ToolCallRequest) (*tool.Result, error) {
		if target != nil {
			return target.Invoke(ctx, nil, req.ToolCall.Args)
		}
		p, _ := req.ToolCall.Args["file_path"].(string)
		if req.ToolCall.Name == "write_file" {
			if r := b.Write(ctx, p, req.ToolCall.Args["content"].(string)); r.Error != "" {
				return &tool.Result{Content: []llm.ContentBlock{llm.TextBlock("Error: " + r.Error)}, IsError: true}, nil
			}
			return &tool.Result{Content: []llm.ContentBlock{llm.TextBlock("Updated file " + p)}}, nil
		}
		r := b.Edit(ctx, p, req.ToolCall.Args["old_string"].(string), req.ToolCall.Args["new_string"].(string), false)
		if r.Error != "" {
			return &tool.Result{Content: []llm.ContentBlock{llm.TextBlock("Error: " + r.Error)}, IsError: true}, nil
		}
		return &tool.Result{Content: []llm.ContentBlock{llm.TextBlock("Successfully replaced 1 instance(s) of the string in '" + p + "'")}}, nil
	}
	req := &core.ToolCallRequest{ToolCall: llm.ToolCall{ID: "e1", Name: name, Args: args}, State: &rt.State{Messages: history}}
	res, err := m.WrapToolCall(context.Background(), req, next)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func edit(file, old, new string) map[string]any {
	return map[string]any{"file_path": "/" + file, "old_string": old, "new_string": new}
}

func readBack(t *testing.T, dir, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// microBlock decodes the [micro] annotation of a result.
func microBlock(t *testing.T, text string) map[string]any {
	t.Helper()
	_, raw, ok := strings.Cut(text, microMarker+" ")
	if !ok {
		t.Fatalf("no %s block in:\n%s", microMarker, text)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(strings.SplitN(raw, "\n", 2)[0]), &out); err != nil {
		t.Fatalf("bad block %q: %v", raw, err)
	}
	return out
}

const goodGo = "package m\n\nfunc Two() int {\n\treturn 3\n}\n"

func TestMicroCheckRollsBackANewGoSyntaxError(t *testing.T) {
	dir := t.TempDir()
	b := microWorkspace(t, dir, map[string]string{"a.go": goodGo})
	res := runMicro(t, MicroCheck{Files: b}, b, "edit_file", edit("a.go", "\treturn 3\n}", "\treturn 2\n"))
	text := resultText(res)
	if !res.IsError || !strings.HasPrefix(text, "Error: edit rolled back") {
		t.Fatalf("result = %v %q", res.IsError, text)
	}
	block := microBlock(t, text)
	if block["status"] != "rolled_back" {
		t.Fatalf("block = %v", block)
	}
	diags, _ := block["diagnostics"].([]any)
	if d, _ := diags[0].(map[string]any); len(diags) == 0 || d["file"] != "a.go" || d["line"] == nil {
		t.Fatalf("diagnostics = %v", block["diagnostics"])
	}
	if got := readBack(t, dir, "a.go"); got != goodGo {
		t.Fatalf("a.go = %q", got)
	}
	if r := b.Edit(context.Background(), "/a.go", "return 3", "return 2", false); r.Error != "" {
		t.Fatalf("the version guard should accept an edit after the rollback: %s", r.Error)
	}
}

func TestMicroCheckKeepsAnEditToAFileThatWasAlreadyBroken(t *testing.T) {
	dir := t.TempDir()
	broken := "package m\n\nfunc Two() int {\n\treturn 3\n"
	b := microWorkspace(t, dir, map[string]string{"a.go": broken})
	res := runMicro(t, MicroCheck{Files: b}, b, "edit_file", edit("a.go", "return 3", "return 2"))
	text := resultText(res)
	if res.IsError || !strings.HasPrefix(text, "Successfully") {
		t.Fatalf("result = %q", text)
	}
	if block := microBlock(t, text); block["status"] != "syntax_error" || !strings.Contains(block["note"].(string), "before this edit") {
		t.Fatalf("block = %v", block)
	}
	if got := readBack(t, dir, "a.go"); !strings.Contains(got, "return 2") {
		t.Fatalf("a.go = %q", got)
	}
}

func TestMicroCheckRollsBackTheWholePatch(t *testing.T) {
	dir := t.TempDir()
	b := microWorkspace(t, dir, map[string]string{"a.go": goodGo, "b.go": "package m\n\nfunc B() int { return 1 }\n"})
	patch := "*** Begin Patch\n" +
		"*** Update File: b.go\n@@\n-func B() int { return 1 }\n+func B() int { return 2 }\n" +
		"*** Add File: c.go\n+package m\n+\n+func C() int { return 3 }\n" +
		"*** Update File: a.go\n@@\n-\treturn 3\n-}\n+\treturn 2\n" +
		"*** End Patch"
	res := runMicro(t, MicroCheck{Files: b}, b, codetools.ApplyPatchName, map[string]any{"patch": patch})
	if !res.IsError {
		t.Fatalf("result = %s", resultText(res))
	}
	if got := readBack(t, dir, "b.go"); !strings.Contains(got, "return 1") {
		t.Fatalf("b.go = %q", got)
	}
	if got := readBack(t, dir, "a.go"); got != goodGo {
		t.Fatalf("a.go = %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "c.go")); !os.IsNotExist(err) {
		t.Fatalf("c.go should be removed, stat err = %v", err)
	}
}

func TestMicroCheckRollsBackPythonAndJavaScript(t *testing.T) {
	for _, tc := range []struct{ cmd, file, good, old, new string }{
		{"python3", "app.py", "def f():\n    return 1\n", "return 1", "return (1"},
		{"node", "app.js", "function f() {\n  return 1;\n}\n", "return 1;", "return 1;\n}\n}"},
	} {
		t.Run(tc.cmd, func(t *testing.T) {
			if _, err := exec.LookPath(tc.cmd); err != nil {
				t.Skip(tc.cmd + " not installed")
			}
			dir := t.TempDir()
			b := microWorkspace(t, dir, map[string]string{tc.file: tc.good})
			m := MicroCheck{Files: b, Workspace: b.(backend.SandboxBackend)}
			res := runMicro(t, m, b, "edit_file", edit(tc.file, tc.old, tc.new))
			if !res.IsError || microBlock(t, resultText(res))["status"] != "rolled_back" {
				t.Fatalf("result = %s", resultText(res))
			}
			if got := readBack(t, dir, tc.file); got != tc.good {
				t.Fatalf("%s = %q", tc.file, got)
			}
		})
	}
}

func TestMicroCheckKeepsAPythonEditWhenTheFileWasAlreadyBroken(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not installed")
	}
	dir := t.TempDir()
	b := microWorkspace(t, dir, map[string]string{"app.py": "def f(:\n    return 1\n"})
	m := MicroCheck{Files: b, Workspace: b.(backend.SandboxBackend)}
	res := runMicro(t, m, b, "edit_file", edit("app.py", "return 1", "return 2"))
	if res.IsError || microBlock(t, resultText(res))["status"] != "syntax_error" {
		t.Fatalf("result = %s", resultText(res))
	}
	if got := readBack(t, dir, "app.py"); !strings.Contains(got, "return 2") {
		t.Fatalf("app.py = %q", got)
	}
}

func TestMicroCheckReportsCompileErrorsWithoutRollingBack(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not installed")
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module m\n\ngo 1.21\n")
	b := microWorkspace(t, dir, map[string]string{"a.go": goodGo})
	m := MicroCheck{Files: b, Workspace: b.(backend.SandboxBackend), Timeout: time.Minute}
	res := runMicro(t, m, b, "edit_file", edit("a.go", "return 3", "return missing"))
	text := resultText(res)
	if res.IsError {
		t.Fatalf("result = %s", text)
	}
	block := microBlock(t, text)
	diags, _ := block["diagnostics"].([]any)
	if block["status"] != "failed" || len(diags) != 1 || !strings.Contains(diags[0].(map[string]any)["message"].(string), "undefined: missing") {
		t.Fatalf("block = %v", block)
	}
	if !strings.Contains(readBack(t, dir, "a.go"), "return missing") {
		t.Fatal("a compile error must not roll the edit back")
	}
	if strings.Contains(stripNotes(text), microMarker) {
		t.Fatal("stripNotes should drop the [micro] block")
	}
}

func TestMicroCheckReportsAnEnvironmentErrorOncePerTurn(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not installed")
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module m\n\ngo 1.21\n\nrequire example.com/dep v1.0.0\n")
	b := microWorkspace(t, dir, map[string]string{"a.go": "package m\n\nimport _ \"example.com/dep\"\n\nfunc Two() int { return 3 }\n"})
	m := MicroCheck{Files: b, Workspace: b.(backend.SandboxBackend), Timeout: time.Minute}

	first := resultText(runMicro(t, m, b, "edit_file", edit("a.go", "return 3", "return 2")))
	block := microBlock(t, first)
	if block["status"] != "environment" || block["env"] != "dependency:example.com/dep" || !strings.Contains(block["note"].(string), "go mod tidy") {
		t.Fatalf("block = %v", block)
	}
	history := []llm.Message{
		llm.NewUserMessage("make Two return 1"),
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "e0", Name: "edit_file", Args: edit("a.go", "return 3", "return 2")}}},
		llm.NewToolMessage("e0", "edit_file", first),
	}
	if second := resultText(runMicroAfter(t, m, b, history, "edit_file", edit("a.go", "return 2", "return 1"))); strings.Contains(second, microMarker) {
		t.Fatalf("reported the same environment error twice in a turn:\n%s", second)
	}
	nextTurn := append(history, llm.NewAssistantMessage("done"), llm.NewUserMessage("now return 0"))
	if third := resultText(runMicroAfter(t, m, b, nextTurn, "edit_file", edit("a.go", "return 1", "return 0"))); !strings.Contains(third, microMarker) {
		t.Fatalf("a new turn should report it again:\n%s", third)
	}
}

func TestDiagnosticsNoteSeparatesCodeAndEnvironmentErrors(t *testing.T) {
	code := codetools.Diagnostic{File: "a.go", Line: 4, Severity: "error", Source: "build", Message: "undefined: missing"}
	env := codetools.Diagnostic{File: "b.go", Line: 9, Severity: "error", Source: "build", Message: proxyUnreachable}

	note, ok := diagnosticsNote([]codetools.Diagnostic{code, env}, nil)
	if !ok || note.Status != "failed" || len(note.Diagnostics) != 1 || note.Diagnostics[0].Message != "undefined: missing" {
		t.Fatalf("mixed = %+v", note)
	}
	if note.Env != "network" || !strings.Contains(note.Note, "environment") {
		t.Fatalf("mixed note does not mention the environment error: %+v", note)
	}

	note, ok = diagnosticsNote([]codetools.Diagnostic{env}, nil)
	if !ok || note.Status != "environment" || note.Env != "network" || !strings.Contains(note.Note, "Editing the code will not fix") {
		t.Fatalf("environment only = %+v", note)
	}

	note, ok = diagnosticsNote([]codetools.Diagnostic{code}, nil)
	if !ok || note.Status != "failed" || note.Env != "" || note.Note != "Fix these errors in the changed files before running tests." {
		t.Fatalf("code only = %+v", note)
	}
}

func TestMicroCheckIsQuietWhenTheFilesAreClean(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not installed")
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module m\n\ngo 1.21\n")
	b := microWorkspace(t, dir, map[string]string{"a.go": goodGo})
	m := MicroCheck{Files: b, Workspace: b.(backend.SandboxBackend), Timeout: time.Minute}
	res := runMicro(t, m, b, "edit_file", edit("a.go", "return 3", "return 2"))
	if text := resultText(res); res.IsError || strings.Contains(text, microMarker) {
		t.Fatalf("result = %s", text)
	}
}

func TestMicroCheckSkipsChecksThatDoNotFinish(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module m\n\ngo 1.21\n")
	b := microWorkspace(t, dir, map[string]string{"a.go": goodGo})
	m := MicroCheck{Files: b, Workspace: silentShell{}, Timeout: time.Second}
	res := runMicro(t, m, b, "edit_file", edit("a.go", "return 3", "return missing"))
	if text := resultText(res); res.IsError || strings.Contains(text, microMarker) {
		t.Fatalf("result = %s", text)
	}
}

func TestMicroCheckIgnoresFailedEditsAndOtherTools(t *testing.T) {
	dir := t.TempDir()
	b := microWorkspace(t, dir, map[string]string{"a.go": goodGo})
	res := runMicro(t, MicroCheck{Files: b}, b, "edit_file", edit("a.go", "not there", "x"))
	if text := resultText(res); !res.IsError || strings.Contains(text, microMarker) {
		t.Fatalf("failed edit = %s", text)
	}
	called := false
	req := &core.ToolCallRequest{ToolCall: llm.ToolCall{Name: "read_file", Args: map[string]any{"file_path": "/a.go"}}}
	_, _ = MicroCheck{Files: b}.WrapToolCall(context.Background(), req, func(context.Context, *core.ToolCallRequest) (*tool.Result, error) {
		called = true
		return &tool.Result{Content: []llm.ContentBlock{llm.TextBlock("x")}}, nil
	})
	if !called {
		t.Fatal("read_file should pass through")
	}
}

func TestAgentRollsBackAnEditThatBreaksTheSyntax(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module m\n\ngo 1.21\n")
	writeFile(t, filepath.Join(dir, "a.go"), goodGo)
	call := func(id, name string, args map[string]any) llm.Message {
		return llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: id, Name: name, Args: args}}}
	}
	model := llm.NewFakeModel(
		call("r1", "read_file", map[string]any{"file_path": "/a.go"}),
		call("e1", "edit_file", edit("a.go", "\treturn 3\n}", "\treturn 2\n")),
		llm.NewAssistantMessage("Done."),
	)
	a, err := CreateCLIAgent(context.Background(), Options{Model: model, Cwd: dir, Backend: NewBackend(dir), AutoApprove: true, NoWebTools: true, NoVerifyGate: true})
	if err != nil {
		t.Fatal(err)
	}
	st, err := a.Invoke(context.Background(), core.Input{Messages: []llm.Message{llm.NewUserMessage("make Two return 2")}}, rt.RunConfig{})
	if err != nil {
		t.Fatal(err)
	}
	var edited string
	for _, m := range st.Messages {
		if m.Role == llm.RoleTool && m.ToolCallID == "e1" {
			edited = m.Text()
		}
	}
	if !strings.HasPrefix(edited, "Error: edit rolled back") {
		t.Fatalf("edit result = %q", edited)
	}
	if got := readBack(t, dir, "a.go"); got != goodGo {
		t.Fatalf("a.go = %q", got)
	}
}

// silentShell never reports an exit code, as when every command times out.
type silentShell struct{ backend.SandboxBackend }

func (silentShell) ID() string { return "silent" }

func (silentShell) Execute(context.Context, string, *time.Duration) backend.ExecuteResponse {
	return backend.ExecuteResponse{}
}
