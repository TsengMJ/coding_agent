package filesystem

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/tsengmingchun/quill/agent"
	"github.com/tsengmingchun/quill/backend"
	"github.com/tsengmingchun/quill/llm"
	"github.com/tsengmingchun/quill/runtime"
	"github.com/tsengmingchun/quill/tool"
)

func numberedLines(n, width int) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		line := fmt.Sprintf("line %d ", i)
		b.WriteString(line + strings.Repeat("x", max(width-len(line), 0)) + "\n")
	}
	return b.String()
}

func textTool(name, text string) tool.Tool {
	return &tool.Func{ToolName: name, ToolSchema: tool.Schema{"type": "object"}, Fn: func(context.Context, *tool.Runtime, map[string]any) (*tool.Result, error) {
		return &tool.Result{Content: []llm.ContentBlock{llm.TextBlock(text)}}, nil
	}}
}

func invoke(t *testing.T, m *Middleware, tools []tool.Tool, user string, script ...llm.Message) (*runtime.State, *llm.FakeModel) {
	t.Helper()
	model := llm.NewFakeModel(append(script, llm.NewAssistantMessage("done"))...)
	a, err := agent.CreateAgent(agent.Config{Model: model, Tools: tools, Middleware: []agent.Middleware{m}})
	if err != nil {
		t.Fatal(err)
	}
	state, err := a.Invoke(context.Background(), agent.Input{Messages: []llm.Message{llm.NewUserMessage(user)}}, runtime.RunConfig{})
	if err != nil {
		t.Fatal(err)
	}
	return state, model
}

func toolMessage(state *runtime.State, id string) llm.Message {
	for _, msg := range state.Messages {
		if msg.Role == llm.RoleTool && msg.ToolCallID == id {
			return msg
		}
	}
	return llm.Message{}
}

func TestSmallToolResultIsNotEvicted(t *testing.T) {
	b, _ := newShellBackend(t)
	m, _ := New(Options{Backend: b, ToolTokenLimitBeforeEvict: 100})
	state, _ := invoke(t, m, []tool.Tool{textTool("small", "hello")}, "go", call("c1", "small", nil))
	if got := toolMessage(state, "c1").Text(); got != "hello" {
		t.Fatalf("tool message = %q", got)
	}
}

func TestLargeToolResultIsOffloadedWithPreview(t *testing.T) {
	b, _ := newShellBackend(t)
	m, _ := New(Options{Backend: b, ToolTokenLimitBeforeEvict: 100})
	big := numberedLines(50, 20)
	state, _ := invoke(t, m, []tool.Tool{textTool("big", big)}, "go", call("call.1", "big", nil))
	got := toolMessage(state, "call.1").Text()
	for _, want := range []string{
		"Tool result too large, the result of this tool call call.1 was saved in the filesystem at this path: /large_tool_results/call_1",
		"head and tail of the result",
		"... [40 lines truncated] ...",
		"line 1 ", "line 50 ",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("stub missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "line 6 ") {
		t.Error("preview must omit middle lines")
	}
	r := b.Read(context.Background(), "/large_tool_results/call_1", 0, 100)
	if r.Error != "" || r.FileData.Content != big {
		t.Fatalf("offloaded file = %+v", r)
	}
}

func TestShortResultPreviewShowsAllLinesAndClipsLongOnes(t *testing.T) {
	p := createContentPreview("a\n" + strings.Repeat("y", previewLineCharLimit+50) + "\nc\n")
	if p.linesOmitted || !p.linesClipped {
		t.Fatalf("flags = %+v", p)
	}
	if strings.Contains(p.text, strings.Repeat("y", previewLineCharLimit+1)) {
		t.Fatal("long line not clipped")
	}
	if note := p.note("result"); !strings.HasPrefix(note, "Here is a preview of the result (the output contains lines longer than 1000") {
		t.Fatalf("note = %q", note)
	}
	if full := createContentPreview(numberedLines(10, 5)); full.linesOmitted || strings.Contains(full.text, "truncated") {
		t.Fatalf("10 lines must be shown whole: %+v", full)
	}
}

func TestExcludedToolIsTruncatedNotEvicted(t *testing.T) {
	b, _ := newShellBackend(t)
	m, _ := New(Options{Backend: b, ToolTokenLimitBeforeEvict: 10})
	req := &agent.ToolCallRequest{ToolCall: llm.ToolCall{ID: "r", Name: string(ToolReadFile)}}
	blocks, _, evicted := m.evictBlocks(context.Background(), req, "r", []llm.ContentBlock{llm.TextBlock(strings.Repeat("z", 200))})
	if evicted || blocks[0].Text != strings.Repeat("z", 200) {
		t.Fatalf("read_file result changed: evicted=%v %q", evicted, blocks[0].Text)
	}
}

type failingWrites struct{ backend.Backend }

func (failingWrites) Write(context.Context, string, string) backend.WriteResult {
	return backend.WriteResult{Error: "disk full"}
}

func TestWriteFailureFallsBackToTruncation(t *testing.T) {
	b, _ := newShellBackend(t)
	m, _ := New(Options{Backend: failingWrites{b}, ToolTokenLimitBeforeEvict: 10})
	big := strings.Repeat("q", backend.ToolResultTokenLimit*4+10)
	state, _ := invoke(t, m, []tool.Tool{textTool("big", big)}, "go", call("c", "big", nil))
	got := toolMessage(state, "c").Text()
	if strings.Contains(got, "Tool result too large") || !strings.Contains(got, backend.TruncationGuidance) {
		t.Fatalf("expected truncation fallback, got %d chars", len(got))
	}
}

func TestEvictionUsesArtifactsRootAndStateFiles(t *testing.T) {
	composite := backend.NewCompositeBackend(backend.NewStateBackend(), nil, "/.quill")
	m, _ := New(Options{Backend: composite, ToolTokenLimitBeforeEvict: 5})
	state, _ := invoke(t, m, []tool.Tool{textTool("big", numberedLines(3, 30))}, "go", call("c9", "big", nil))
	if got := toolMessage(state, "c9").Text(); !strings.Contains(got, "/.quill/large_tool_results/c9") {
		t.Fatalf("stub = %q", got)
	}
	files, _ := state.Values[backend.FilesStateKey].(map[string]backend.FileData)
	if _, ok := files["/.quill/large_tool_results/c9"]; !ok {
		t.Fatalf("StateBackend files = %v", files)
	}
}

func TestLargeUserMessageIsEvictedOnceAndStateKeepsText(t *testing.T) {
	b, dir := newShellBackend(t)
	m, _ := New(Options{Backend: b, HumanMessageTokenLimitBeforeEvict: 10})
	user := numberedLines(30, 10)
	state, model := invoke(t, m, []tool.Tool{textTool("noop", "ok")}, user, call("c", "noop", nil))

	first := state.Messages[0]
	if first.Text() != user {
		t.Fatal("state must keep the full user message")
	}
	p := evictedTo(first)
	if !strings.HasPrefix(p, "/conversation_history/") || !strings.HasSuffix(p, ".md") {
		t.Fatalf("user message not tagged: %v", first.Metadata)
	}
	for i, req := range model.Requests() {
		var got string
		for _, msg := range req.Messages {
			if msg.Role == llm.RoleUser {
				got = msg.Text()
			}
		}
		if !strings.Contains(got, "Message content too large and was saved to the filesystem at: "+p) {
			t.Fatalf("request %d user message = %q", i, got)
		}
	}
	entries := b.Ls(context.Background(), "/conversation_history").Entries
	if len(entries) != 1 {
		t.Fatalf("user message evicted %d times (%s)", len(entries), dir)
	}
	if n := countUser(state); n != 1 {
		t.Fatalf("tagging duplicated the user message: %d user messages", n)
	}
}

func TestUserEvictionAdaptsToSmallModelWindow(t *testing.T) {
	b, _ := newShellBackend(t)
	m, err := New(Options{Backend: b})
	if err != nil {
		t.Fatal(err)
	}
	model := llm.NewFakeModel(llm.NewAssistantMessage("done")).WithProfile(llm.ModelProfile{MaxInputTokens: 8000})
	a, err := agent.CreateAgent(agent.Config{Model: model, Middleware: []agent.Middleware{m}})
	if err != nil {
		t.Fatal(err)
	}
	full := strings.Repeat("x", 10000) // 2500 estimated tokens; below the old 50000 limit.
	state, err := a.Invoke(context.Background(), agent.Input{Messages: []llm.Message{llm.NewUserMessage(full)}}, runtime.RunConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if state.Messages[0].Text() != full || evictedTo(state.Messages[0]) == "" {
		t.Fatalf("full message was not preserved and offloaded")
	}
	if got := model.Requests()[0].Messages[0].Text(); !strings.Contains(got, "Message content too large") {
		t.Fatalf("model saw unbounded input")
	}
}

func countUser(state *runtime.State) int {
	n := 0
	for _, msg := range state.Messages {
		if msg.Role == llm.RoleUser {
			n++
		}
	}
	return n
}
