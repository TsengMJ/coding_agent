package filesystem

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/tsengmingchun/quill/agent"
	"github.com/tsengmingchun/quill/backend"
	"github.com/tsengmingchun/quill/llm"
	"github.com/tsengmingchun/quill/runtime"
	"github.com/tsengmingchun/quill/tool"
)

func call(id, name string, a map[string]any) llm.Message {
	return llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: id, Name: name, Args: a}}}
}

func newShellBackend(t *testing.T) (*backend.LocalShellBackend, string) {
	t.Helper()
	dir := t.TempDir()
	return backend.NewLocalShellBackend(backend.LocalShellOptions{
		FilesystemOptions: backend.FilesystemOptions{RootDir: dir, VirtualMode: true},
		InheritEnv:        true,
	}), dir
}

// runAgent runs a FakeModel-driven agent with the middleware and returns the
// tool messages keyed by tool call ID.
func runAgent(t *testing.T, m *Middleware, script ...llm.Message) (map[string]llm.Message, *llm.FakeModel) {
	t.Helper()
	model := llm.NewFakeModel(append(script, llm.NewAssistantMessage("done"))...)
	a, err := agent.CreateAgent(agent.Config{Model: model, Middleware: []agent.Middleware{m}})
	if err != nil {
		t.Fatal(err)
	}
	state, err := a.Invoke(context.Background(), agent.Input{Messages: []llm.Message{llm.NewUserMessage("go")}}, runtime.RunConfig{})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]llm.Message{}
	for _, msg := range state.Messages {
		if msg.Role == llm.RoleTool {
			out[msg.ToolCallID] = msg
		}
	}
	return out, model
}

func toolNames(tools []tool.Tool) []string {
	names := make([]string, len(tools))
	for i, t := range tools {
		names[i] = t.Name()
	}
	return names
}

func TestNewRequiresBackend(t *testing.T) {
	if _, err := New(Options{}); err == nil {
		t.Fatal("New without backend must fail")
	}
}

func TestDefaultToolsWithShellBackend(t *testing.T) {
	b, _ := newShellBackend(t)
	m, err := New(Options{Backend: b})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"ls", "read_file", "write_file", "edit_file", "delete", "glob", "grep", "execute"}
	if got := toolNames(m.Tools()); !slices.Equal(got, want) {
		t.Fatalf("tools = %v, want %v", got, want)
	}
}

func TestDeleteDroppedForNonDeleterBackend(t *testing.T) {
	fsb := backend.NewFilesystemBackend(backend.FilesystemOptions{RootDir: t.TempDir(), VirtualMode: true})
	m, err := New(Options{Backend: struct{ backend.Backend }{fsb}})
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(toolNames(m.Tools()), "delete") {
		t.Fatal("delete must not be offered when the backend cannot delete")
	}
}

func TestExecuteDroppedForNonSandboxBackend(t *testing.T) {
	b := backend.NewFilesystemBackend(backend.FilesystemOptions{RootDir: t.TempDir(), VirtualMode: true})
	m, err := New(Options{Backend: b})
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(toolNames(m.Tools()), "execute") {
		t.Fatal("execute must not be offered without a sandbox backend")
	}
}

func TestCodingRoundTrip(t *testing.T) {
	b, dir := newShellBackend(t)
	m, err := New(Options{Backend: b})
	if err != nil {
		t.Fatal(err)
	}
	msgs, _ := runAgent(t, m,
		call("w", "write_file", map[string]any{"file_path": "/hello/main.txt", "content": "hello world\n"}),
		call("r", "read_file", map[string]any{"file_path": "/hello/main.txt"}),
		call("e", "edit_file", map[string]any{"file_path": "/hello/main.txt", "old_string": "world", "new_string": "go"}),
		call("x", "execute", map[string]any{"command": "cat hello/main.txt"}),
		call("g", "grep", map[string]any{"pattern": "hello", "output_mode": "content"}),
		call("l", "ls", map[string]any{"path": "/"}),
		call("gl", "glob", map[string]any{"pattern": "*.txt"}),
	)
	want := map[string]string{
		"w":  "Updated file /hello/main.txt",
		"r":  "@@ lines 1-1 of 1 @@\nhello world",
		"e":  "Successfully replaced 1 instance(s) of the string in '/hello/main.txt'",
		"x":  "hello go\n\n[Command succeeded with exit code 0]",
		"g":  "/hello/main.txt:\n  1: hello go",
		"l":  "/hello/",
		"gl": "/hello/main.txt",
	}
	for id, text := range want {
		msg := msgs[id]
		if msg.Status != llm.ToolStatusSuccess || msg.Text() != text {
			t.Errorf("%s: status=%s text=%q, want %q", id, msg.Status, msg.Text(), text)
		}
	}
	got, _ := os.ReadFile(filepath.Join(dir, "hello", "main.txt"))
	if string(got) != "hello go\n" {
		t.Fatalf("file on disk = %q", got)
	}
	if art, ok := msgs["x"].Metadata["artifact"].(backend.ExecuteArtifact); !ok || *art.ExitCode != 0 {
		t.Fatalf("execute artifact = %#v", msgs["x"].Metadata["artifact"])
	}
}

func TestGrepRetriesRegexLikePatternAsRegexp(t *testing.T) {
	b, _ := newShellBackend(t)
	b.Write(context.Background(), "/a.js", "exports.posts = 1\n")
	m, _ := New(Options{Backend: b})
	msgs, _ := runAgent(t, m,
		call("re", "grep", map[string]any{"pattern": "export.*posts", "output_mode": "content"}),
		call("none", "grep", map[string]any{"pattern": "missing.*text"}),
	)
	if got := msgs["re"].Text(); !strings.HasPrefix(got, "/a.js:\n  1: exports.posts = 1") || !strings.Contains(got, "RE2 regular expression") {
		t.Fatalf("regexp retry = %q", got)
	}
	if got := msgs["none"].Text(); !strings.Contains(got, "grep matches literal text") {
		t.Fatalf("no-match hint = %q", got)
	}
}

func TestReadFilePagination(t *testing.T) {
	b, _ := newShellBackend(t)
	b.Write(context.Background(), "/f.txt", "a\nb\nc\n")
	m, _ := New(Options{Backend: b})
	msgs, _ := runAgent(t, m, call("r", "read_file", map[string]any{"file_path": "/f.txt", "offset": float64(1), "limit": float64(1)}))
	if got := msgs["r"].Text(); got != "@@ lines 2-2 of 3 | next offset 2 @@\nb" {
		t.Fatalf("got %q", got)
	}
}

func TestToolErrorsBecomeErrorMessages(t *testing.T) {
	b, _ := newShellBackend(t)
	m, _ := New(Options{Backend: b})
	msgs, _ := runAgent(t, m,
		call("missing", "read_file", map[string]any{"file_path": "/nope.txt"}),
		call("escape", "write_file", map[string]any{"file_path": "../x.txt", "content": "x"}),
		call("noargs", "edit_file", map[string]any{"file_path": "/nope.txt"}),
		call("fail", "execute", map[string]any{"command": "exit 2"}),
	)
	for id, want := range map[string]string{
		"missing": "not found",
		"escape":  "Path traversal not allowed",
		"noargs":  `missing required argument "old_string"`,
	} {
		if msgs[id].Status != llm.ToolStatusError || !strings.Contains(msgs[id].Text(), want) {
			t.Errorf("%s: status=%s text=%q, want %q", id, msgs[id].Status, msgs[id].Text(), want)
		}
	}
	if !strings.Contains(msgs["fail"].Text(), "[Command failed with exit code 2]") {
		t.Errorf("execute failure text = %q", msgs["fail"].Text())
	}
}

func TestSystemPromptAppended(t *testing.T) {
	b, _ := newShellBackend(t)
	m, _ := New(Options{Backend: b, SystemPrompt: "use tools"})
	model := llm.NewFakeModel(llm.NewAssistantMessage("ok"))
	sys := llm.NewSystemMessage("base")
	a, err := agent.CreateAgent(agent.Config{Model: model, SystemMessage: &sys, Middleware: []agent.Middleware{m}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Invoke(context.Background(), agent.Input{Messages: []llm.Message{llm.NewUserMessage("hi")}}, runtime.RunConfig{}); err != nil {
		t.Fatal(err)
	}
	if got := model.Requests()[0].Messages[0].Text(); got != "base\n\nuse tools" {
		t.Fatalf("system prompt = %q", got)
	}
	if sys.Text() != "base" {
		t.Fatal("original system message must not be mutated")
	}
}

func TestCustomToolDescription(t *testing.T) {
	b, _ := newShellBackend(t)
	m, _ := New(Options{Backend: b, CustomToolDescriptions: map[string]string{"ls": "custom"}})
	if m.Tools()[0].Description() != "custom" {
		t.Fatalf("description = %q", m.Tools()[0].Description())
	}
}

func TestParallelWritesToSamePathRejectLaterCalls(t *testing.T) {
	b, dir := newShellBackend(t)
	m, err := New(Options{Backend: b})
	if err != nil {
		t.Fatal(err)
	}
	batch := llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{
		{ID: "w1", Name: "write_file", Args: map[string]any{"file_path": "/a.txt", "content": "one"}},
		{ID: "w2", Name: "write_file", Args: map[string]any{"file_path": "/a.txt", "content": "two"}},
		{ID: "w3", Name: "write_file", Args: map[string]any{"file_path": "/b.txt", "content": "three"}},
	}}
	msgs, _ := runAgent(t, m, batch)
	if msgs["w1"].Status == llm.ToolStatusError || msgs["w3"].Status == llm.ToolStatusError {
		t.Fatalf("first writes must succeed: %+v / %+v", msgs["w1"], msgs["w3"])
	}
	if msgs["w2"].Status != llm.ToolStatusError || !strings.Contains(msgs["w2"].Text(), "parallel file mutations") {
		t.Fatalf("second write to same path = %+v", msgs["w2"])
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "a.txt")); string(got) != "one" {
		t.Fatalf("a.txt = %q", got)
	}
}
