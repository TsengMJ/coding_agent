package summarization

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/tsengmingchun/deepagents_golang/agent"
	"github.com/tsengmingchun/deepagents_golang/backend"
	"github.com/tsengmingchun/deepagents_golang/llm"
	"github.com/tsengmingchun/deepagents_golang/runtime"
)

func assistantCall(id, name string, args map[string]any) llm.Message {
	return llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: id, Name: name, Args: args}}}
}

// history is user, assistant(tool call), tool result, assistant, user.
func history() []llm.Message {
	return []llm.Message{
		llm.NewUserMessage("first question"),
		assistantCall("c1", "ls", map[string]any{"path": "/"}),
		llm.NewToolMessage("c1", "ls", "a.txt"),
		llm.NewAssistantMessage("there is a.txt"),
		llm.NewUserMessage("second question"),
	}
}

type fixture struct {
	dir      string
	main     *llm.FakeModel
	summary  *llm.FakeModel
	mw       *Middleware
	agent    agent.Agent
	lastSent func() []llm.Message
}

func newFixture(t *testing.T, opts Options, mainModel llm.ChatModel, replies ...llm.Message) *fixture {
	t.Helper()
	f := &fixture{dir: t.TempDir(), summary: llm.NewFakeModel(llm.NewAssistantMessage("SUMMARY TEXT"))}
	opts.Backend = backend.NewFilesystemBackend(backend.FilesystemOptions{RootDir: f.dir, VirtualMode: true})
	opts.Model = f.summary
	mw, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	f.mw = mw
	if mainModel == nil {
		f.main = llm.NewFakeModel(append(replies, llm.NewAssistantMessage("done"))...)
		mainModel = f.main
	}
	f.agent, err = agent.CreateAgent(agent.Config{Model: mainModel, Middleware: []agent.Middleware{mw}})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *fixture) invoke(t *testing.T, msgs []llm.Message) *runtime.State {
	t.Helper()
	st, err := f.agent.Invoke(context.Background(), agent.Input{Messages: msgs}, runtime.RunConfig{})
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestBelowThresholdSendsEverything(t *testing.T) {
	f := newFixture(t, Options{Trigger: []TriggerClause{{{SizeMessages, 100}}}, Keep: ContextSize{SizeMessages, 2}}, nil)
	st := f.invoke(t, history())
	if _, ok := st.Values[EventStateKey]; ok {
		t.Fatal("unexpected summarization event")
	}
	if got := len(f.main.Requests()[0].Messages); got != 5 {
		t.Fatalf("sent %d messages, want 5", got)
	}
	if len(f.summary.Requests()) != 0 {
		t.Fatal("summary model must not be called")
	}
}

func TestSummarizeReplacesOldMessagesWithoutChangingState(t *testing.T) {
	f := newFixture(t, Options{Trigger: []TriggerClause{{{SizeMessages, 4}}}, Keep: ContextSize{SizeMessages, 2}}, nil)
	st := f.invoke(t, history())

	sent := f.main.Requests()[0].Messages
	if len(sent) != 3 || !strings.Contains(sent[0].Text(), "SUMMARY TEXT") || sent[1].Text() != "there is a.txt" || sent[2].Text() != "second question" {
		t.Fatalf("sent = %+v", sent)
	}
	if len(st.Messages) != 6 {
		t.Fatalf("state has %d messages, want 6 (history + reply)", len(st.Messages))
	}
	ev := eventFrom(st)
	if ev == nil || ev.CutoffIndex != 3 || ev.FilePath == "" {
		t.Fatalf("event = %+v", ev)
	}
	if !strings.Contains(sent[0].Text(), ev.FilePath) {
		t.Fatalf("summary must reference %s: %q", ev.FilePath, sent[0].Text())
	}
	saved, err := os.ReadFile(filepath.Join(f.dir, ev.FilePath))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"## Summarized at", "first question", `name="ls"`, "a.txt"} {
		if !strings.Contains(string(saved), want) {
			t.Errorf("history file missing %q:\n%s", want, saved)
		}
	}
	if strings.Contains(string(saved), "second question") {
		t.Error("kept messages must not be offloaded")
	}
	if p := f.summary.Requests()[0].Messages[0].Text(); !strings.Contains(p, "first question") || strings.Contains(p, "second question") {
		t.Fatalf("summary prompt:\n%s", p)
	}
}

func TestSafeCutoffKeepsToolCallWithResults(t *testing.T) {
	msgs := []llm.Message{
		llm.NewUserMessage("q"),
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "a"}, {ID: "b"}}},
		llm.NewToolMessage("a", "t", "1"),
		llm.NewToolMessage("b", "t", "2"),
		llm.NewUserMessage("q2"),
	}
	cases := []struct{ cutoff, want int }{{0, 0}, {1, 1}, {2, 1}, {3, 1}, {4, 4}, {5, 5}}
	for _, c := range cases {
		if got := safeCutoffPoint(msgs, c.cutoff); got != c.want {
			t.Errorf("safeCutoffPoint(%d) = %d, want %d", c.cutoff, got, c.want)
		}
	}
	orphan := []llm.Message{llm.NewUserMessage("q"), llm.NewToolMessage("x", "t", "1"), llm.NewUserMessage("q2")}
	if got := safeCutoffPoint(orphan, 1); got != 2 {
		t.Errorf("orphan tool result cutoff = %d, want 2", got)
	}
}

func TestTruncateArgsAloneAvoidsSummary(t *testing.T) {
	long := strings.Repeat("x", 5000)
	msgs := []llm.Message{
		llm.NewUserMessage("write it"),
		assistantCall("w1", "write_file", map[string]any{"file_path": "/a", "content": long}),
		llm.NewToolMessage("w1", "write_file", "ok"),
		llm.NewUserMessage("next"),
	}
	trigger := ContextSize{SizeMessages, 3}
	f := newFixture(t, Options{
		Trigger:      []TriggerClause{{{SizeTokens, 1000}}},
		Keep:         ContextSize{SizeMessages, 2},
		TruncateArgs: &TruncateArgsSettings{Trigger: &trigger, Keep: ContextSize{SizeMessages, 1}},
	}, nil)
	st := f.invoke(t, msgs)
	sent := f.main.Requests()[0].Messages
	content := sent[1].ToolCalls[0].Args["content"].(string)
	if !strings.HasSuffix(content, DefaultTruncationText) || len(content) > 100 {
		t.Fatalf("content not clipped: %d chars", len(content))
	}
	if len(f.summary.Requests()) != 0 || st.Values[EventStateKey] != nil {
		t.Fatal("clipping got under the threshold; no summary expected")
	}
	if got := st.Messages[1].ToolCalls[0].Args["content"].(string); got != long {
		t.Fatal("state must keep the original arguments")
	}
}

// overflowOnce rejects its first request with ErrContextOverflow.
type overflowOnce struct {
	*llm.FakeModel
	calls atomic.Int32
}

func (m *overflowOnce) Generate(ctx context.Context, req llm.ChatRequest) (*llm.Message, error) {
	if m.calls.Add(1) == 1 {
		return nil, errors.Join(llm.ErrContextOverflow, errors.New("too long"))
	}
	return m.FakeModel.Generate(ctx, req)
}

func TestContextOverflowSummarizesAndRetriesOnce(t *testing.T) {
	model := &overflowOnce{FakeModel: llm.NewFakeModel(llm.NewAssistantMessage("recovered"))}
	f := newFixture(t, Options{Keep: ContextSize{SizeMessages, 2}}, model)
	st := f.invoke(t, history())
	if got := st.Messages[len(st.Messages)-1].Text(); got != "recovered" {
		t.Fatalf("final = %q", got)
	}
	if model.calls.Load() != 2 || eventFrom(st) == nil {
		t.Fatalf("calls = %d, event = %v", model.calls.Load(), eventFrom(st))
	}
	if sent := model.Requests()[0].Messages; len(sent) != 3 {
		t.Fatalf("retry sent %d messages, want 3", len(sent))
	}
}

// failingModel rejects every request with err.
type failingModel struct {
	*llm.FakeModel
	err error
}

func (m *failingModel) Generate(context.Context, llm.ChatRequest) (*llm.Message, error) {
	return nil, m.err
}

func TestSummaryModelErrorFallsBackToOffloadNote(t *testing.T) {
	f := newFixture(t, Options{Trigger: []TriggerClause{{{SizeMessages, 4}}}, Keep: ContextSize{SizeMessages, 2}}, nil)
	f.mw.opts.Model = &failingModel{FakeModel: llm.NewFakeModel(), err: errors.New("openrouter: model returned an empty response after 3 retries")}
	st := f.invoke(t, history())

	ev := eventFrom(st)
	if ev == nil || ev.FilePath == "" {
		t.Fatalf("event = %+v", ev)
	}
	sent := f.main.Requests()[0].Messages
	if len(sent) != 3 || !strings.Contains(sent[0].Text(), "Summary unavailable") || !strings.Contains(sent[0].Text(), ev.FilePath) {
		t.Fatalf("sent = %+v", sent)
	}
	if got := st.Messages[len(st.Messages)-1].Text(); got != "done" {
		t.Fatalf("final = %q", got)
	}
}

func TestSummaryContextCanceledStillFails(t *testing.T) {
	f := newFixture(t, Options{Trigger: []TriggerClause{{{SizeMessages, 4}}}, Keep: ContextSize{SizeMessages, 2}}, nil)
	f.mw.opts.Model = &failingModel{FakeModel: llm.NewFakeModel(), err: context.Canceled}
	_, err := f.agent.Invoke(context.Background(), agent.Input{Messages: history()}, runtime.RunConfig{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestSecondSummaryTranslatesCutoffToStateIndex(t *testing.T) {
	f := newFixture(t, Options{Trigger: []TriggerClause{{{SizeMessages, 4}}}, Keep: ContextSize{SizeMessages, 2}}, nil)
	first := Event{CutoffIndex: 3, SummaryMessage: summaryMessage("old", "")}
	state := &runtime.State{Messages: append(history(), llm.NewAssistantMessage("a"), llm.NewUserMessage("b"), llm.NewAssistantMessage("c")), Values: map[string]any{EventStateKey: first}}
	upd, err := f.mw.Compact(context.Background(), state, nil)
	if err != nil {
		t.Fatal(err)
	}
	// effective = [summary, m3..m7] (6 messages); keep 2 -> effective cutoff 4 -> state 3+4-1 = 6.
	if got := upd.Values[EventStateKey].(Event).CutoffIndex; got != 6 {
		t.Fatalf("cutoff = %d, want 6", got)
	}
}

func TestCompactNothingToDo(t *testing.T) {
	f := newFixture(t, Options{Keep: ContextSize{SizeMessages, 10}}, nil)
	_, err := f.mw.Compact(context.Background(), &runtime.State{Messages: history()}, nil)
	if !errors.Is(err, ErrNothingToCompact) {
		t.Fatalf("err = %v", err)
	}
}

func TestCompactOffloadsIntoStateFiles(t *testing.T) {
	mw, err := New(Options{
		Backend: backend.NewStateBackend(),
		Model:   llm.NewFakeModel(llm.NewAssistantMessage("SUMMARY TEXT")),
		Keep:    ContextSize{SizeMessages, 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	upd, err := mw.Compact(context.Background(), &runtime.State{Messages: history()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	path := upd.Values[EventStateKey].(Event).FilePath
	files, _ := upd.Values[backend.FilesStateKey].(map[string]*backend.FileData)
	if path == "" || files[path] == nil || !strings.Contains(files[path].Content, "## Summarized at") {
		t.Fatalf("path = %q, files = %+v", path, files)
	}
}

func TestEventSurvivesJSONRoundTrip(t *testing.T) {
	ev := Event{CutoffIndex: 4, SummaryMessage: summaryMessage("s", "/p.md"), FilePath: "/p.md"}
	state := &runtime.State{Values: map[string]any{EventStateKey: map[string]any{
		"cutoff_index": float64(4), "file_path": "/p.md",
		"summary_message": map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": ev.SummaryMessage.Text()}}},
	}}}
	got := eventFrom(state)
	if got == nil || got.CutoffIndex != 4 || got.SummaryMessage.Text() != ev.SummaryMessage.Text() {
		t.Fatalf("event = %+v", got)
	}
}

func TestComputeDefaults(t *testing.T) {
	d, err := ComputeDefaults(llm.NewFakeModel())
	if err != nil {
		t.Fatal(err)
	}
	if d.Trigger != (ContextSize{SizeTokens, 170000}) || d.Keep != (ContextSize{SizeMessages, 6}) {
		t.Fatalf("defaults = %+v", d)
	}
}

func TestComputeDefaultsUsesFractionsWhenContextWindowKnown(t *testing.T) {
	d, err := ComputeDefaults(llm.NewFakeModel().WithProfile(llm.ModelProfile{MaxInputTokens: 128000}))
	if err != nil {
		t.Fatal(err)
	}
	if d.Trigger != (ContextSize{SizeFraction, 0.80}) || d.Keep != (ContextSize{SizeFraction, 0.10}) {
		t.Fatalf("defaults = %+v", d)
	}
}

func TestDefaultOptionsForKnownOpenAIModelTriggerAtFraction(t *testing.T) {
	opts, err := DefaultOptions(llm.NewOpenAIModel(llm.ProviderConfig{Model: "gpt-4o"}, false), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(opts.Trigger) != 1 || opts.Trigger[0][0] != (ContextSize{SizeFraction, 0.80}) {
		t.Fatalf("trigger = %+v", opts.Trigger)
	}
}

type boundedSummaryModel struct {
	*llm.FakeModel
	requests []llm.ChatRequest
	success  int
}

type selectedSummaryModel struct {
	llm.ChatModel
	selected llm.ChatModel
}

func (m *selectedSummaryModel) ForContext(context.Context) llm.ChatModel { return m.selected }

func (m *selectedSummaryModel) Generate(ctx context.Context, req llm.ChatRequest) (*llm.Message, error) {
	return m.selected.Generate(ctx, req)
}

func (m *boundedSummaryModel) Generate(_ context.Context, req llm.ChatRequest) (*llm.Message, error) {
	m.requests = append(m.requests, req)
	if summaryInputCost(req.Messages[0].Text()) > 1800 {
		return nil, llm.ErrContextOverflow
	}
	m.success++
	reply := llm.NewAssistantMessage("rolling summary")
	return &reply, nil
}

func TestLongHistorySummaryIsBoundedAndOffloaded(t *testing.T) {
	dir := t.TempDir()
	model := &boundedSummaryModel{FakeModel: llm.NewFakeModel().WithProfile(llm.ModelProfile{MaxInputTokens: 16000})}
	mw, err := New(Options{Backend: backend.NewFilesystemBackend(backend.FilesystemOptions{RootDir: dir, VirtualMode: true}), Model: model, Keep: ContextSize{SizeMessages, 2}})
	if err != nil {
		t.Fatal(err)
	}
	var msgs []llm.Message
	for i := 0; i < 14; i++ {
		msgs = append(msgs, llm.NewUserMessage(strings.Repeat("漢", 400)))
	}
	upd, err := mw.Compact(context.Background(), &runtime.State{Messages: msgs}, model)
	if err != nil {
		t.Fatal(err)
	}
	if len(model.requests) < 3 || model.success < 2 {
		t.Fatalf("summary calls = %d, successful = %d", len(model.requests), model.success)
	}
	for _, req := range model.requests {
		if n := summaryInputCost(req.Messages[0].Text()); n > 4000 {
			t.Fatalf("summary input = %d, want <= 4000", n)
		}
	}
	ev := upd.Values[EventStateKey].(Event)
	if ev.FilePath == "" || !strings.Contains(ev.SummaryMessage.Text(), "rolling summary") {
		t.Fatalf("event = %+v", ev)
	}
	saved, err := os.ReadFile(filepath.Join(dir, ev.FilePath))
	if err != nil || strings.Count(string(saved), strings.Repeat("漢", 400)) != 12 {
		t.Fatalf("offloaded history lost messages: %v", err)
	}
}

func TestFixedPromptOverBudgetReturnsClearError(t *testing.T) {
	mw, err := New(Options{Backend: backend.NewStateBackend(), Keep: ContextSize{SizeMessages, 2}})
	if err != nil {
		t.Fatal(err)
	}
	system := llm.NewSystemMessage(strings.Repeat("x", 28000))
	called := false
	_, err = mw.WrapModelCall(context.Background(), &agent.ModelRequest{
		Model: llm.NewFakeModel().WithProfile(llm.ModelProfile{MaxInputTokens: 8000}), SystemMessage: &system,
		State: &runtime.State{Messages: []llm.Message{llm.NewUserMessage("go")}},
	}, func(context.Context, *agent.ModelRequest) (*agent.ModelResponse, error) {
		called = true
		return &agent.ModelResponse{}, nil
	})
	if !errors.Is(err, llm.ErrContextOverflow) || !strings.Contains(err.Error(), "system prompt and tool definitions") || called {
		t.Fatalf("err = %v, called = %v", err, called)
	}
}

func TestUnknownWindowAcceptsCodingAgentSizedPrompt(t *testing.T) {
	mw, err := New(Options{Backend: backend.NewStateBackend(), Keep: ContextSize{SizeMessages, 2}})
	if err != nil {
		t.Fatal(err)
	}
	system := llm.NewSystemMessage(strings.Repeat("x", 80000))
	called := false
	_, err = mw.WrapModelCall(context.Background(), &agent.ModelRequest{
		Model: llm.NewFakeModel(), SystemMessage: &system,
		State: &runtime.State{Messages: []llm.Message{llm.NewUserMessage("go")}},
	}, func(context.Context, *agent.ModelRequest) (*agent.ModelResponse, error) {
		called = true
		return &agent.ModelResponse{}, nil
	})
	if err != nil || !called {
		t.Fatalf("a 20k-token prompt must reach a model without a known window: err = %v", err)
	}
}

func TestLongHistoryIsSummarizedInOneCallWhenTheWindowAllows(t *testing.T) {
	cases := map[string]llm.ModelProfile{
		"1M window":      {MaxInputTokens: 1_000_000},
		"unknown window": {},
	}
	for name, profile := range cases {
		t.Run(name, func(t *testing.T) {
			model := llm.NewFakeModel(llm.NewAssistantMessage("one summary")).WithProfile(profile)
			mw, err := New(Options{Backend: backend.NewStateBackend(), Model: model, Keep: ContextSize{SizeMessages, 2}})
			if err != nil {
				t.Fatal(err)
			}
			var msgs []llm.Message
			for i := 0; i < 20; i++ {
				msgs = append(msgs, llm.NewUserMessage(strings.Repeat("x", 8000)))
			}
			if _, err := mw.Compact(context.Background(), &runtime.State{Messages: msgs}, model); err != nil {
				t.Fatal(err)
			}
			if n := len(model.Requests()); n != 1 {
				t.Fatalf("~36k tokens of history took %d sequential summary calls, want 1", n)
			}
		})
	}
}

func TestSelectedModelRecomputesDefaultCompactionThreshold(t *testing.T) {
	selected := &boundedSummaryModel{FakeModel: llm.NewFakeModel().WithProfile(llm.ModelProfile{MaxInputTokens: 8000})}
	model := &selectedSummaryModel{ChatModel: llm.NewFakeModel().WithProfile(llm.ModelProfile{MaxInputTokens: 50000}), selected: selected}
	mw, err := NewForModel(model, backend.NewStateBackend())
	if err != nil {
		t.Fatal(err)
	}
	var msgs []llm.Message
	for i := 0; i < 18; i++ {
		msgs = append(msgs, llm.NewUserMessage(strings.Repeat("x", 1600)))
	}
	seenSummary := false
	resp, err := mw.WrapModelCall(context.Background(), &agent.ModelRequest{Model: model, Messages: msgs, State: &runtime.State{Messages: msgs}},
		func(_ context.Context, req *agent.ModelRequest) (*agent.ModelResponse, error) {
			seenSummary = len(req.Messages) < len(msgs) && isSummaryMessage(req.Messages[0])
			return &agent.ModelResponse{Messages: []llm.Message{llm.NewAssistantMessage("done")}}, nil
		})
	if err != nil || !seenSummary || resp.Update == nil {
		t.Fatalf("selected 8k model did not compact: err=%v, summary=%v", err, seenSummary)
	}
}
