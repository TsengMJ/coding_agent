package tui

import (
	"context"
	"fmt"
	"iter"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/cursor"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/tsengmingchun/deepagents_golang/code/server"
	"github.com/tsengmingchun/deepagents_golang/code/sessions"
	"github.com/tsengmingchun/deepagents_golang/code/storage"
	"github.com/tsengmingchun/deepagents_golang/llm"
)

func newTestModel(t *testing.T, srv server.Server, cwd string) *model {
	t.Helper()
	m := newModel(context.Background(), Options{Server: srv, Cwd: cwd, MarkdownStyle: "notty"})
	m.input.Cursor.SetMode(cursor.CursorStatic)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	return m
}

func newService(t *testing.T, replies ...llm.Message) *server.Service {
	t.Helper()
	svc, err := server.New(context.Background(), server.Config{Storage: storage.NewMemory(), Model: llm.NewFakeModel(replies...)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	return svc
}

// drain runs cmd and every command it leads to, feeding messages to m.
// Spinner ticks and nil messages are dropped so the loop ends.
func drain(t *testing.T, m *model, cmd tea.Cmd) []tea.Msg {
	t.Helper()
	var seen []tea.Msg
	queue := []tea.Cmd{cmd}
	for len(queue) > 0 {
		c := queue[0]
		queue = queue[1:]
		if c == nil {
			continue
		}
		msg := runCmd(t, c)
		switch msg := msg.(type) {
		case nil, spinner.TickMsg:
		case tea.BatchMsg:
			queue = append(queue, msg...)
		default:
			seen = append(seen, msg)
			_, next := m.Update(msg)
			queue = append(queue, next)
		}
	}
	return seen
}

func runCmd(t *testing.T, c tea.Cmd) tea.Msg {
	t.Helper()
	done := make(chan tea.Msg, 1)
	go func() { done <- c() }()
	select {
	case msg := <-done:
		return msg
	case <-time.After(5 * time.Second):
		t.Fatal("command did not return")
		return nil
	}
}

func key(s string) tea.KeyMsg {
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "ctrl+c":
		return tea.KeyMsg{Type: tea.KeyCtrlC}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func press(t *testing.T, m *model, s string) {
	t.Helper()
	_, cmd := m.Update(key(s))
	drain(t, m, cmd)
}

func writeReply(path string) llm.Message {
	return llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "w", Name: "write_file",
		Args: map[string]any{"file_path": path, "content": "A"}}}}
}

func hasEntry(m *model, kind entryKind, substr string) bool {
	for _, e := range m.entries {
		if e.kind == kind && strings.Contains(e.text, substr) {
			return true
		}
	}
	return false
}

func TestStreamingDeltasThenMessage(t *testing.T) {
	m := newTestModel(t, newService(t), t.TempDir())
	m.Update(eventMsg{ev: server.Event{Kind: server.EventDelta, Text: "hel"}})
	if !strings.Contains(m.transcript(), "hel") {
		t.Fatalf("streamed text not shown:\n%s", m.transcript())
	}
	msg := llm.NewAssistantMessage("hello")
	m.Update(eventMsg{ev: server.Event{Kind: server.EventMessage, Message: &msg}})
	if m.streaming.Len() != 0 || !hasEntry(m, entryAssistant, "hello") {
		t.Fatalf("entries = %+v streaming=%q", m.entries, m.streaming.String())
	}
	if strings.Count(m.transcript(), "hel") != 1 {
		t.Fatalf("text shown twice:\n%s", m.transcript())
	}
}

func TestEntriesRenderOncePerWidth(t *testing.T) {
	m := newTestModel(t, newService(t), t.TempDir())
	for i := range 20 {
		m.info(strings.Repeat("line ", i+1))
	}
	m.Update(nil)
	base := m.renders
	for range 50 {
		m.Update(eventMsg{ev: server.Event{Kind: server.EventDelta, Text: "tok "}})
	}
	if m.renders != base {
		t.Fatalf("finished entries re-rendered: %d -> %d", base, m.renders)
	}
	m.Update(tea.WindowSizeMsg{Width: 60, Height: 30})
	if m.renders != base+len(m.entries) {
		t.Fatalf("resize renders = %d, want %d", m.renders-base, len(m.entries))
	}
}

func TestScrolledUpViewStaysPutDuringStreaming(t *testing.T) {
	m := newTestModel(t, newService(t), t.TempDir())
	for i := range 100 {
		m.info(strings.Repeat("x", i%7+1))
	}
	m.Update(nil)
	if !m.vp.AtBottom() {
		t.Fatal("new output should start at the bottom")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	offset := m.vp.YOffset
	m.Update(eventMsg{ev: server.Event{Kind: server.EventDelta, Text: "new text"}})
	if m.vp.YOffset != offset || m.vp.AtBottom() {
		t.Fatalf("offset %d -> %d, at bottom %v", offset, m.vp.YOffset, m.vp.AtBottom())
	}
	m.Update(tea.KeyMsg{Type: tea.KeyPgDown})
	for !m.vp.AtBottom() {
		m.Update(tea.KeyMsg{Type: tea.KeyPgDown})
	}
	m.Update(eventMsg{ev: server.Event{Kind: server.EventDelta, Text: " more\nand more"}})
	if !m.vp.AtBottom() {
		t.Fatal("view at the bottom should follow new output")
	}
}

func TestAssistantMessageRendersMarkdown(t *testing.T) {
	m := newTestModel(t, newService(t), t.TempDir())
	m.mdStyle = "dark"
	msg := llm.NewAssistantMessage("# Title\n\n- **bold** item\n\n```go\nfmt.Println(1)\n```")
	m.Update(eventMsg{ev: server.Event{Kind: server.EventMessage, Message: &msg}})
	out := ansi.Strip(m.transcript())
	if strings.Contains(out, "**bold**") || strings.Contains(out, "```") {
		t.Fatalf("markdown syntax left in output:\n%s", out)
	}
	for _, want := range []string{"Title", "bold", "fmt.Println(1)"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q:\n%s", want, out)
		}
	}
}

func TestApprovalBoxShowsDiffAgainstFile(t *testing.T) {
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "a.txt"), []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := newTestModel(t, newService(t, writeReply("/a.txt"), llm.NewAssistantMessage("done")), cwd)
	drain(t, m, m.submit("write"))
	view := m.View()
	for _, want := range []string{"--- /a.txt", "-old", "+A"} {
		if !strings.Contains(view, want) {
			t.Fatalf("approval box missing %q:\n%s", want, view)
		}
	}
}

func TestSubmitRunsTurn(t *testing.T) {
	m := newTestModel(t, newService(t, llm.NewAssistantMessage("pong")), t.TempDir())
	drain(t, m, m.submit("ping"))
	if m.mode != modeIdle || !hasEntry(m, entryUser, "ping") || !hasEntry(m, entryAssistant, "pong") {
		t.Fatalf("mode=%v entries=%+v", m.mode, m.entries)
	}
	if !strings.Contains(m.View(), "pong") {
		t.Fatalf("view missing reply:\n%s", m.View())
	}
}

func TestApprovalBoxApprove(t *testing.T) {
	cwd := t.TempDir()
	m := newTestModel(t, newService(t, writeReply("/a.txt"), llm.NewAssistantMessage("done")), cwd)
	drain(t, m, m.submit("write"))
	if m.mode != modeApprove {
		t.Fatalf("mode = %v, entries %+v", m.mode, m.entries)
	}
	if view := m.View(); !strings.Contains(view, "Approve 1/1: write_file /a.txt") || !strings.Contains(view, "a approve") {
		t.Fatalf("approval box missing:\n%s", view)
	}
	press(t, m, "a")
	if got, _ := os.ReadFile(filepath.Join(cwd, "a.txt")); string(got) != "A" {
		t.Fatalf("a.txt = %q", got)
	}
	if m.mode != modeIdle || !hasEntry(m, entryInfo, "approved write_file /a.txt") || !hasEntry(m, entryAssistant, "done") {
		t.Fatalf("mode=%v entries=%+v", m.mode, m.entries)
	}
}

func TestApprovalBoxRejectWithReason(t *testing.T) {
	cwd := t.TempDir()
	m := newTestModel(t, newService(t, writeReply("/a.txt"), llm.NewAssistantMessage("ok")), cwd)
	drain(t, m, m.submit("write"))
	press(t, m, "r")
	if m.mode != modeReason {
		t.Fatalf("mode = %v", m.mode)
	}
	press(t, m, "n")
	press(t, m, "o")
	press(t, m, "enter")
	if _, err := os.Stat(filepath.Join(cwd, "a.txt")); !os.IsNotExist(err) {
		t.Fatalf("rejected write happened: %v", err)
	}
	if m.mode != modeIdle || !hasEntry(m, entryInfo, "rejected write_file /a.txt: no") {
		t.Fatalf("mode=%v entries=%+v", m.mode, m.entries)
	}
}

func TestSlashCommands(t *testing.T) {
	m := newTestModel(t, newService(t), t.TempDir())
	first := m.thread
	drain(t, m, m.submit("/yolo"))
	if !m.auto || !strings.Contains(m.View(), "AUTO-APPROVE") {
		t.Fatal("/yolo did not turn auto-approve on")
	}
	drain(t, m, m.submit("/new"))
	if m.thread == first || len(m.entries) != 1 {
		t.Fatalf("/new: thread %s entries %+v", m.thread, m.entries)
	}
	drain(t, m, m.submit("/bogus"))
	if !hasEntry(m, entryError, "unknown command /bogus") {
		t.Fatalf("entries = %+v", m.entries)
	}
	cmd := m.submit("/quit")
	if _, ok := runCmd(t, cmd).(tea.QuitMsg); !ok {
		t.Fatal("/quit must quit")
	}
}

func TestThreadPickerSwitchesThread(t *testing.T) {
	cwd := t.TempDir()
	svc := newService(t, llm.NewAssistantMessage("first reply"))
	m := newTestModel(t, svc, cwd)
	old := m.thread
	drain(t, m, m.submit("hello"))
	drain(t, m, m.submit("/new"))
	drain(t, m, m.submit("/threads"))
	if m.mode != modePicker || !strings.Contains(m.View(), old) {
		t.Fatalf("picker not shown (mode %v):\n%s", m.mode, m.View())
	}
	press(t, m, "enter")
	if m.thread != old || !hasEntry(m, entryAssistant, "first reply") || !hasEntry(m, entryUser, "hello") {
		t.Fatalf("thread=%s entries=%+v", m.thread, m.entries)
	}
}

func TestPickerClosesOnAltModifiedEscOrCtrlC(t *testing.T) {
	m := newTestModel(t, newService(t), t.TempDir())
	for _, k := range []tea.KeyMsg{{Type: tea.KeyEsc, Alt: true}, {Type: tea.KeyCtrlC, Alt: true}} {
		m.threads, m.mode = []sessions.Info{{ThreadID: "x"}}, modePicker
		m.Update(k)
		if m.mode != modeIdle {
			t.Fatalf("%v did not close the picker", k)
		}
	}
}

func TestZeroWindowSizeKeepsLayout(t *testing.T) {
	m := newTestModel(t, newService(t), t.TempDir())
	m.Update(tea.WindowSizeMsg{})
	if m.width != 100 || m.height != 30 {
		t.Fatalf("size = %dx%d", m.width, m.height)
	}
}

func TestResumeLoadsPendingApproval(t *testing.T) {
	cwd := t.TempDir()
	svc := newService(t, writeReply("/a.txt"), llm.NewAssistantMessage("done"))
	for range svc.Run(context.Background(), server.RunRequest{ThreadID: "t1", Cwd: cwd, Messages: []llm.Message{llm.NewUserMessage("w")}}) {
	}
	m := newTestModel(t, svc, cwd)
	drain(t, m, m.submit("/resume t1"))
	if m.mode != modeApprove || m.thread != "t1" {
		t.Fatalf("mode=%v thread=%s", m.mode, m.thread)
	}
	press(t, m, "A")
	if got, _ := os.ReadFile(filepath.Join(cwd, "a.txt")); string(got) != "A" {
		t.Fatalf("a.txt = %q", got)
	}
}

// blockingServer streams one delta and then blocks until cancelled.
type blockingServer struct{ server.Server }

func (blockingServer) Run(ctx context.Context, _ server.RunRequest) iter.Seq2[server.Event, error] {
	return func(yield func(server.Event, error) bool) {
		if !yield(server.Event{Kind: server.EventDelta, Text: "partial"}, nil) {
			return
		}
		<-ctx.Done()
		yield(server.Event{}, ctx.Err())
	}
}

func (blockingServer) GetUsage(context.Context, string) (*server.ThreadUsage, error) {
	return nil, nil
}

func (blockingServer) ListThreads(context.Context, sessions.Filter) ([]sessions.Info, error) {
	return nil, nil
}

func TestCtrlCCancelsRunAndSecondCtrlCQuits(t *testing.T) {
	m := newTestModel(t, blockingServer{}, t.TempDir())
	cmd := m.submit("go")
	batch := runCmd(t, cmd).(tea.BatchMsg)
	first := runCmd(t, batch[0])
	_, next := m.Update(first)
	if m.mode != modeRunning || !strings.Contains(m.transcript(), "partial") {
		t.Fatalf("mode=%v transcript=%q", m.mode, m.transcript())
	}
	_, _ = m.Update(key("ctrl+c"))
	drain(t, m, next)
	if m.mode != modeIdle || !hasEntry(m, entryInfo, "cancelled") || !hasEntry(m, entryAssistant, "partial") {
		t.Fatalf("mode=%v entries=%+v", m.mode, m.entries)
	}
	_, quit := m.Update(key("ctrl+c"))
	if _, ok := runCmd(t, quit).(tea.QuitMsg); !ok {
		t.Fatal("ctrl+c on an empty idle prompt must quit")
	}
}

func TestWriteTodosRendersChecklist(t *testing.T) {
	m := newTestModel(t, newService(t), t.TempDir())
	msg := llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "t", Name: "write_todos", Args: map[string]any{
		"todos": []any{
			map[string]any{"content": "read code", "status": "completed"},
			map[string]any{"content": "write tests", "status": "in_progress"},
			map[string]any{"content": "ship", "status": "pending"},
		},
	}}}}
	m.Update(eventMsg{ev: server.Event{Kind: server.EventMessage, Message: &msg}})
	out := ansi.Strip(m.transcript())
	for _, want := range []string{"✓ read code", "▸ write tests", "○ ship"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "content") {
		t.Fatalf("raw JSON shown:\n%s", out)
	}
}

func TestCompactCommandReportsResult(t *testing.T) {
	svc := newService(t,
		llm.NewAssistantMessage("a1"), llm.NewAssistantMessage("a2"),
		llm.NewAssistantMessage("a3"), llm.NewAssistantMessage("a4"),
		llm.NewAssistantMessage("the summary"),
	)
	m := newTestModel(t, svc, t.TempDir())
	drain(t, m, m.submit("/compact"))
	if !hasEntry(m, entryError, "compact:") {
		t.Fatalf("empty thread: entries = %+v", m.entries)
	}
	for _, text := range []string{"q1", "q2", "q3", "q4"} {
		drain(t, m, m.submit(text))
	}
	drain(t, m, m.submit("/compact"))
	if !hasEntry(m, entryInfo, "/.deepagents/conversation_history/") {
		t.Fatalf("entries = %+v", m.entries)
	}
}

func TestTranscriptShowsModelCallsWithToolsNested(t *testing.T) {
	cwd := t.TempDir()
	reply := llm.Message{Role: llm.RoleAssistant, Usage: &llm.Usage{InputTokens: 1200, OutputTokens: 34, TotalTokens: 1234},
		ToolCalls: []llm.ToolCall{
			{ID: "a", Name: "execute", Args: map[string]any{"command": "sleep 0.3"}},
			{ID: "b", Name: "execute", Args: map[string]any{"command": "sleep 0.3"}},
		}}
	svc := newService(t, reply, llm.NewAssistantMessage("both done"))
	m := newModel(context.Background(), Options{Server: svc, Cwd: cwd, MarkdownStyle: "notty", AutoApprove: true})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	drain(t, m, m.submit("run two sleeps"))

	view := ansi.Strip(m.transcript())
	for _, want := range []string{"◆ main · LLM #1", "1.2k in / 34 out", "├─ execute sleep 0.3", "└─ execute sleep 0.3", "◆ main · LLM #2", "⇉ 2 tools", "sequential"} {
		if !strings.Contains(view, want) {
			t.Errorf("transcript missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "…") {
		t.Errorf("a tool row still shows as running:\n%s", view)
	}
	if !strings.Contains(m.statusLine(), "tokens") {
		t.Errorf("status line = %q", m.statusLine())
	}
}

func TestToolResultUpdatesItsRowInPlace(t *testing.T) {
	m := newTestModel(t, newService(t), t.TempDir())
	m.message(llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "c1", Name: "ls", Args: map[string]any{"path": "/"}}}})
	before := ansi.Strip(m.transcript())
	if !strings.Contains(before, "└─ ls /") || !strings.Contains(before, "…") {
		t.Fatalf("before result:\n%s", before)
	}
	res := llm.WithTiming(llm.NewToolMessage("c1", "ls", "a.txt"), llm.Timing{Start: time.Now(), Duration: 1500 * time.Millisecond})
	res.Status = llm.ToolStatusError
	res.Content = []llm.ContentBlock{llm.TextBlock("permission denied")}
	m.message(res)
	after := ansi.Strip(m.transcript())
	if !strings.Contains(after, "└─ ls / — permission denied") || !strings.Contains(after, "1.5s") || strings.Contains(after, "…") {
		t.Fatalf("after result:\n%s", after)
	}
}

func taskCall(id, desc string) llm.ToolCall {
	return llm.ToolCall{ID: id, Name: "task", Args: map[string]any{"subagent_type": "general-purpose", "description": desc}}
}

func subEvent(id string, msg llm.Message) eventMsg {
	return eventMsg{ev: server.Event{Kind: server.EventMessage, Message: &msg, Namespace: []string{"task:" + id}}}
}

// lineWith returns the transcript line containing substr.
func lineWith(t *testing.T, transcript, substr string) string {
	t.Helper()
	for _, l := range strings.Split(transcript, "\n") {
		if strings.Contains(l, substr) {
			return l
		}
	}
	t.Fatalf("no line with %q in:\n%s", substr, transcript)
	return ""
}

func TestParallelSubagentsNestUnderTheirTaskRows(t *testing.T) {
	m := newTestModel(t, newService(t), t.TempDir())
	m.message(llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{taskCall("t1", "find handlers"), taskCall("t2", "check coverage")}})
	m.Update(subEvent("t2", llm.NewUserMessage("check coverage")))
	m.Update(subEvent("t2", llm.Message{Role: llm.RoleAssistant, Usage: &llm.Usage{TotalTokens: 100},
		ToolCalls: []llm.ToolCall{{ID: "x", Name: "execute", Args: map[string]any{"command": "go test ./..."}}}}))
	m.Update(subEvent("t1", llm.Message{Role: llm.RoleAssistant,
		ToolCalls: []llm.ToolCall{{ID: "g", Name: "grep", Args: map[string]any{"pattern": "HandleFunc"}}}}))

	out := ansi.Strip(m.transcript())
	for _, want := range []string{"├─ ◇ task #1 general-purpose · find handlers", "└─ ◇ task #2 general-purpose · check coverage"} {
		lineWith(t, out, want)
	}
	lines := strings.Split(out, "\n")
	idx := func(s string) int {
		for i, l := range lines {
			if strings.Contains(l, s) {
				return i
			}
		}
		t.Fatalf("missing %q in:\n%s", s, out)
		return -1
	}
	if !(idx("task #1") < idx("grep HandleFunc") && idx("grep HandleFunc") < idx("task #2") && idx("task #2") < idx("execute go test")) {
		t.Fatalf("subagent rows not under their task rows:\n%s", out)
	}
	if !strings.HasPrefix(lineWith(t, out, "grep HandleFunc"), "  │      └─ ") || !strings.Contains(out, "◆ #1 · LLM 1") || !strings.Contains(out, "◆ #2 · LLM 1") {
		t.Fatalf("nested layout:\n%s", out)
	}
	if strings.Count(out, "check coverage") != 1 {
		t.Fatalf("subagent user message shown:\n%s", out)
	}
	m.mode = modeRunning
	if m.tokens != 100 || !strings.Contains(m.statusLine(), "2 subagents running") {
		t.Fatalf("tokens=%d status=%q", m.tokens, m.statusLine())
	}
}

func TestSubagentRunShowsEndToEnd(t *testing.T) {
	delegate := llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{taskCall("t1", "look around")}}
	svc := newService(t, delegate, llm.NewAssistantMessage("sub report"), llm.NewAssistantMessage("all done"))
	m := newModel(context.Background(), Options{Server: svc, Cwd: t.TempDir(), MarkdownStyle: "notty", AutoApprove: true})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	drain(t, m, m.submit("delegate"))

	out := ansi.Strip(m.transcript())
	for _, want := range []string{"└─ ◇ task #1 general-purpose · look around", "◆ #1 · LLM 1", "↳ sub report", "◆ main · LLM #2", "all done"} {
		if !strings.Contains(out, want) {
			t.Errorf("transcript missing %q:\n%s", want, out)
		}
	}
	if strings.Count(out, "sub report") != 1 || strings.Contains(out, "thinking") || !strings.Contains(lineWith(t, out, "task #1"), "✓") {
		t.Errorf("transcript:\n%s", out)
	}
}

func TestTaskResultMarksSubagentDoneWithReport(t *testing.T) {
	m := newTestModel(t, newService(t), t.TempDir())
	m.message(llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{taskCall("t1", "find handlers")}})
	m.Update(subEvent("t1", llm.NewAssistantMessage("Found 5 handlers")))
	res := llm.WithTiming(llm.NewToolMessage("t1", "task", "Found 5 handlers\nin http.go"), llm.Timing{Start: time.Now(), Duration: 3400 * time.Millisecond})
	m.message(res)

	out := ansi.Strip(m.transcript())
	if row := lineWith(t, out, "task #1"); !strings.HasSuffix(strings.TrimRight(row, " "), "3.4s ✓") {
		t.Fatalf("task row = %q", row)
	}
	if !strings.Contains(out, "↳ Found 5 handlers ...") || strings.Contains(out, "thinking") {
		t.Fatalf("transcript:\n%s", out)
	}
	if m.runningSubs() != 0 || len(m.live) != 0 {
		t.Fatalf("running subs=%d live=%v", m.runningSubs(), m.live)
	}
}

func TestBackgroundTaskRowFinishesOnNotification(t *testing.T) {
	m := newTestModel(t, newService(t), t.TempDir())
	call := taskCall("t1", "research pgx")
	call.Args["run_in_background"] = true
	m.message(llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{call}})
	m.message(llm.NewToolMessage("t1", "task", "Started background task t1 (general-purpose)."))

	out := ansi.Strip(m.transcript())
	if row := lineWith(t, out, "task #1"); !strings.Contains(row, "background") || strings.Contains(row, "✓") {
		t.Fatalf("started row = %q", row)
	}
	if m.runningSubs() != 1 || strings.Contains(out, "thinking") {
		t.Fatalf("running subs = %d, transcript:\n%s", m.runningSubs(), out)
	}
	m.message(llm.NewUserMessage("<task-notification>\n<task-id>t1</task-id>\n<subagent-type>general-purpose</subagent-type>\n<status>completed</status>\n<result>\npgx v5.7 is current\n</result>\n</task-notification>"))
	out = ansi.Strip(m.transcript())
	if row := lineWith(t, out, "task #1"); !strings.Contains(row, "✓") {
		t.Fatalf("finished row = %q", row)
	}
	if !strings.Contains(out, "↳ pgx v5.7 is current") || strings.Contains(out, "<task-notification>") || m.runningSubs() != 0 {
		t.Fatalf("transcript:\n%s", out)
	}
}

func TestSubmitShowsThinkingRowUntilTheModelReplies(t *testing.T) {
	m := newTestModel(t, blockingServer{}, t.TempDir())
	cmd := m.submit("go")
	out := ansi.Strip(m.transcript())
	last := lineWith(t, out, "thinking")
	if !strings.Contains(last, "◆ main · LLM #1") || !strings.Contains(last, "s") {
		t.Fatalf("thinking row = %q", last)
	}
	m.message(llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "c1", Name: "ls", Args: map[string]any{"path": "/"}}}})
	out = ansi.Strip(m.transcript())
	if strings.Contains(out, "thinking") {
		t.Fatalf("thinking row shown while a tool runs:\n%s", out)
	}
	if row := lineWith(t, out, "└─ ls /"); strings.Contains(row, "…") || !strings.HasSuffix(strings.TrimRight(row, " "), "s") {
		t.Fatalf("running tool row = %q", row)
	}
	m.message(llm.NewToolMessage("c1", "ls", "a.txt"))
	if !strings.Contains(ansi.Strip(m.transcript()), "◆ main · LLM #2  thinking") {
		t.Fatalf("no thinking row after tool results:\n%s", ansi.Strip(m.transcript()))
	}
	m.stopRun()
	batch := runCmd(t, cmd).(tea.BatchMsg)
	drain(t, m, batch[0])
}

func TestTickRerendersOnlyLiveEntries(t *testing.T) {
	m := newTestModel(t, blockingServer{}, t.TempDir())
	for i := range 20 {
		m.info(fmt.Sprintf("line %d", i))
	}
	cmd := m.submit("go")
	m.message(llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "c1", Name: "ls", Args: map[string]any{"path": "/"}}}})
	m.Update(nil)
	base := m.renders
	m.Update(m.spin.Tick())
	if got := m.renders - base; got != 1 {
		t.Fatalf("tick re-rendered %d entries, want 1", got)
	}
	m.stopRun()
	batch := runCmd(t, cmd).(tea.BatchMsg)
	drain(t, m, batch[0])
}

func TestCancelLeavesNoSpinner(t *testing.T) {
	m := newTestModel(t, blockingServer{}, t.TempDir())
	cmd := m.submit("go")
	batch := runCmd(t, cmd).(tea.BatchMsg)
	_, next := m.Update(runCmd(t, batch[0]))
	m.message(llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{taskCall("t1", "dig")}})
	m.Update(subEvent("t1", llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "g", Name: "grep", Args: map[string]any{"pattern": "x"}}}}))
	m.Update(key("ctrl+c"))
	drain(t, m, next)

	out := ansi.Strip(m.transcript())
	if m.mode != modeIdle || len(m.live) != 0 || m.runningSubs() != 0 {
		t.Fatalf("mode=%v live=%v running=%d", m.mode, m.live, m.runningSubs())
	}
	if strings.Contains(out, "thinking") || !strings.Contains(lineWith(t, out, "task #1"), "cancelled") || !strings.Contains(lineWith(t, out, "grep x"), "cancelled") {
		t.Fatalf("transcript after cancel:\n%s", out)
	}
}

func TestResumedThreadSettlesCallsWithoutResults(t *testing.T) {
	m := newTestModel(t, newService(t), t.TempDir())
	m.threadLoaded(threadLoadedMsg{id: "t1", ts: &server.ThreadState{Messages: []llm.Message{
		llm.NewUserMessage("go"),
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{taskCall("t1", "dig")}},
	}}})
	if len(m.live) != 0 || m.runningSubs() != 0 || strings.Contains(ansi.Strip(m.transcript()), "…") {
		t.Fatalf("live=%v running=%d:\n%s", m.live, m.runningSubs(), ansi.Strip(m.transcript()))
	}
}

func TestBatchSummaryVerdict(t *testing.T) {
	now := time.Now()
	par := batchSummary([]llm.Timing{{Start: now, Duration: time.Second}, {Start: now.Add(10 * time.Millisecond), Duration: time.Second}})
	seq := batchSummary([]llm.Timing{{Start: now, Duration: time.Second}, {Start: now.Add(time.Second), Duration: time.Second}})
	if !strings.HasSuffix(par, "parallel") || !strings.Contains(par, "wall 1.0s") || !strings.Contains(par, "sum 2.0s") {
		t.Errorf("parallel summary = %q", par)
	}
	if !strings.HasSuffix(seq, "sequential") {
		t.Errorf("sequential summary = %q", seq)
	}
}

func TestStatusShowsContextShareAndCostCommand(t *testing.T) {
	reply := llm.NewAssistantMessage("hi")
	reply.Usage = &llm.Usage{InputTokens: 2500, OutputTokens: 10, TotalTokens: 2510}
	model := llm.NewFakeModel(reply).WithProfile(llm.ModelProfile{MaxInputTokens: 50000})
	svc, err := server.New(context.Background(), server.Config{Storage: storage.NewMemory(), Model: model})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	m := newTestModel(t, svc, t.TempDir())
	drain(t, m, m.submit("hello"))
	if !strings.Contains(m.statusLine(), "context 5%") {
		t.Fatalf("status line = %q", m.statusLine())
	}
	drain(t, m, m.submit("/cost"))
	last := m.entries[len(m.entries)-1].text
	if !strings.Contains(last, "fake:fake  1 call · in 2.5k · out 10") {
		t.Fatalf("/cost = %q", last)
	}
}
