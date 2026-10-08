package hitl

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tsengmingchun/quill/apps/agent/agent"
	"github.com/tsengmingchun/quill/apps/agent/llm"
	"github.com/tsengmingchun/quill/apps/agent/runtime"
	"github.com/tsengmingchun/quill/apps/agent/tool"
)

type harness struct {
	agent  agent.Agent
	model  *llm.FakeModel
	mu     sync.Mutex
	calls  []string
	thread string
}

func (h *harness) record(call string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls = append(h.calls, call)
}

func newHarness(t *testing.T, replies ...llm.Message) *harness {
	t.Helper()
	return newHarnessWith(t, map[string]InterruptOnConfig{"write_file": {}}, replies...)
}

func newHarnessWith(t *testing.T, interruptOn map[string]InterruptOnConfig, replies ...llm.Message) *harness {
	t.Helper()
	return newHarnessOn(t, runtime.NewMemoryCheckpointer(), "t", interruptOn, replies...)
}

func newHarnessOn(t *testing.T, cp runtime.Checkpointer, thread string, interruptOn map[string]InterruptOnConfig, replies ...llm.Message) *harness {
	t.Helper()
	h := &harness{model: llm.NewFakeModel(replies...), thread: thread}
	write := &tool.Func{
		ToolName:   "write_file",
		ToolSchema: tool.Schema{"type": "object"},
		Fn: func(_ context.Context, _ *tool.Runtime, args map[string]any) (*tool.Result, error) {
			p, _ := args["file_path"].(string)
			h.record(p)
			return &tool.Result{Content: []llm.ContentBlock{llm.TextBlock("wrote " + p)}}, nil
		},
	}
	read := &tool.Func{
		ToolName:   "read_file",
		ToolSchema: tool.Schema{"type": "object"},
		Fn: func(context.Context, *tool.Runtime, map[string]any) (*tool.Result, error) {
			h.record("read")
			return &tool.Result{Content: []llm.ContentBlock{llm.TextBlock("contents")}}, nil
		},
	}
	a, err := agent.CreateAgent(agent.Config{
		Model:        h.model,
		Tools:        []tool.Tool{write, read},
		Middleware:   []agent.Middleware{New(interruptOn)},
		Checkpointer: cp,
	})
	if err != nil {
		t.Fatal(err)
	}
	h.agent = a
	return h
}

func writeCall(id, path string) llm.Message {
	return llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: id, Name: "write_file", Args: map[string]any{"file_path": path}}}}
}

func (h *harness) start(t *testing.T, cfg runtime.RunConfig) (*runtime.State, *runtime.InterruptError) {
	t.Helper()
	st, err := h.agent.Invoke(context.Background(), agent.Input{Messages: []llm.Message{llm.NewUserMessage("go")}}, cfg)
	var paused *runtime.InterruptError
	if errors.As(err, &paused) {
		return nil, paused
	}
	if err != nil {
		t.Fatal(err)
	}
	return st, nil
}

func (h *harness) resume(t *testing.T, decisions ...Decision) (*runtime.State, *runtime.InterruptError) {
	t.Helper()
	st, err := h.agent.Invoke(context.Background(), agent.Input{Resume: &runtime.Command{Resume: map[string]any{"after_model": decisions}}},
		runtime.RunConfig{ThreadID: h.thread})
	var paused *runtime.InterruptError
	if errors.As(err, &paused) {
		return nil, paused
	}
	if err != nil {
		t.Fatal(err)
	}
	return st, nil
}

func TestInterruptsBeforeReviewedToolAndRunsItOnApprove(t *testing.T) {
	h := newHarness(t, writeCall("w1", "/a.txt"), llm.NewAssistantMessage("done"))
	_, paused := h.start(t, runtime.RunConfig{ThreadID: "t"})
	if paused == nil {
		t.Fatal("expected an interrupt")
	}
	reqs := paused.Interrupts[0].Value.([]ActionRequest)
	if len(reqs) != 1 || reqs[0].Name != "write_file" || reqs[0].ToolCallID != "w1" || reqs[0].Args["file_path"] != "/a.txt" {
		t.Fatalf("action requests = %+v", reqs)
	}
	if len(h.calls) != 0 {
		t.Fatalf("tool ran before approval: %v", h.calls)
	}
	st, paused := h.resume(t, Decision{Type: DecisionApprove})
	if paused != nil {
		t.Fatalf("unexpected second interrupt: %+v", paused)
	}
	if strings.Join(h.calls, ",") != "/a.txt" {
		t.Fatalf("tool calls = %v", h.calls)
	}
	if n := len(h.model.Requests()); n != 2 {
		t.Fatalf("model calls = %d, want 2 (resume must not repeat the model call)", n)
	}
	if st.Messages[len(st.Messages)-1].Text() != "done" {
		t.Fatalf("final = %q", st.Messages[len(st.Messages)-1].Text())
	}
}

func TestDescribeRendersPerCallDescription(t *testing.T) {
	h := newHarnessWith(t, map[string]InterruptOnConfig{"write_file": {
		Description: "static",
		Describe:    func(call llm.ToolCall) string { return "Write " + call.Args["file_path"].(string) },
	}}, writeCall("w1", "/a.txt"))
	_, paused := h.start(t, runtime.RunConfig{ThreadID: "t"})
	if paused == nil {
		t.Fatal("expected an interrupt")
	}
	if got := paused.Interrupts[0].Value.([]ActionRequest)[0].Description; got != "Write /a.txt" {
		t.Fatalf("description = %q", got)
	}
}

func TestRejectSendsErrorToolMessageToModel(t *testing.T) {
	h := newHarness(t, writeCall("w1", "/a.txt"), llm.NewAssistantMessage("ok, not writing"))
	if _, paused := h.start(t, runtime.RunConfig{ThreadID: "t"}); paused == nil {
		t.Fatal("expected an interrupt")
	}
	st, _ := h.resume(t, Decision{Type: DecisionReject, Message: "use b.txt instead"})
	if len(h.calls) != 0 {
		t.Fatalf("rejected tool ran: %v", h.calls)
	}
	var rejection *llm.Message
	for i := range st.Messages {
		if st.Messages[i].ToolCallID == "w1" {
			rejection = &st.Messages[i]
		}
	}
	if rejection == nil || rejection.Status != llm.ToolStatusError || rejection.Text() != "use b.txt instead" {
		t.Fatalf("rejection message = %+v", rejection)
	}
	reqs := h.model.Requests()
	if len(reqs) != 2 {
		t.Fatalf("model calls = %d, want 2", len(reqs))
	}
	last := reqs[1].Messages[len(reqs[1].Messages)-1]
	if last.ToolCallID != "w1" {
		t.Fatalf("model did not see the rejection; last message = %+v", last)
	}
}

func TestAutoApproveSkipsReview(t *testing.T) {
	h := newHarness(t, writeCall("w1", "/a.txt"), llm.NewAssistantMessage("done"))
	_, paused := h.start(t, runtime.RunConfig{ThreadID: "t", Configurable: map[string]any{AutoApproveKey: true}})
	if paused != nil {
		t.Fatal("auto-approve must not interrupt")
	}
	if len(h.calls) != 1 {
		t.Fatalf("tool calls = %v", h.calls)
	}
}

func TestAlwaysInterruptsUnderAutoApprove(t *testing.T) {
	cfg := map[string]InterruptOnConfig{"write_file": {Always: true}}
	h := newHarnessWith(t, cfg, writeCall("w1", "/a.txt"), llm.NewAssistantMessage("done"))
	if _, paused := h.start(t, runtime.RunConfig{ThreadID: "t", Configurable: map[string]any{AutoApproveKey: true}}); paused == nil {
		t.Fatal("an Always config must interrupt even with auto-approve")
	}
}

func TestWhenLimitsReviewToMatchingCalls(t *testing.T) {
	onlyEtc := map[string]InterruptOnConfig{"write_file": {When: func(call llm.ToolCall) bool {
		p, _ := call.Args["file_path"].(string)
		return strings.HasPrefix(p, "/etc/")
	}}}
	h := newHarnessWith(t, onlyEtc, writeCall("w1", "/a.txt"), llm.NewAssistantMessage("done"))
	if _, paused := h.start(t, runtime.RunConfig{ThreadID: "t"}); paused != nil {
		t.Fatal("non-matching call must not need review")
	}
	h = newHarnessWith(t, onlyEtc, writeCall("w1", "/etc/hosts"), llm.NewAssistantMessage("done"))
	if _, paused := h.start(t, runtime.RunConfig{ThreadID: "t"}); paused == nil {
		t.Fatal("matching call must need review")
	}
}

func TestAlwaysWhenOverridesWhenAndAutoApprove(t *testing.T) {
	cfg := map[string]InterruptOnConfig{"write_file": {
		When: func(llm.ToolCall) bool { return false },
		AlwaysWhen: func(call llm.ToolCall) bool {
			p, _ := call.Args["file_path"].(string)
			return p == "/etc/hosts"
		},
	}}
	auto := runtime.RunConfig{ThreadID: "t", Configurable: map[string]any{AutoApproveKey: true}}
	h := newHarnessWith(t, cfg, writeCall("w1", "/a.txt"), llm.NewAssistantMessage("done"))
	if _, paused := h.start(t, auto); paused != nil {
		t.Fatal("a call outside AlwaysWhen must follow When and auto-approve")
	}
	h = newHarnessWith(t, cfg, writeCall("w1", "/etc/hosts"), llm.NewAssistantMessage("done"))
	if _, paused := h.start(t, auto); paused == nil {
		t.Fatal("an AlwaysWhen call must be reviewed under auto-approve")
	}
}

func TestUnreviewedToolsRunWithoutInterrupt(t *testing.T) {
	h := newHarness(t,
		llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "r", Name: "read_file"}}},
		llm.NewAssistantMessage("done"))
	if _, paused := h.start(t, runtime.RunConfig{ThreadID: "t"}); paused != nil {
		t.Fatal("read_file must not need review")
	}
}

func TestResumeValueIsUsedOnlyOnce(t *testing.T) {
	h := newHarness(t, writeCall("w1", "/a.txt"), writeCall("w2", "/b.txt"), llm.NewAssistantMessage("done"))
	if _, paused := h.start(t, runtime.RunConfig{ThreadID: "t"}); paused == nil {
		t.Fatal("expected first interrupt")
	}
	_, paused := h.resume(t, Decision{Type: DecisionApprove})
	if paused == nil {
		t.Fatal("second write must pause again instead of reusing the first approval")
	}
	if strings.Join(h.calls, ",") != "/a.txt" {
		t.Fatalf("tool calls = %v", h.calls)
	}
	if _, paused := h.resume(t, Decision{Type: DecisionApprove}); paused != nil {
		t.Fatal("unexpected third interrupt")
	}
	if strings.Join(h.calls, ",") != "/a.txt,/b.txt" {
		t.Fatalf("tool calls = %v", h.calls)
	}
}

func TestDisallowedAndMiscountedDecisionsFail(t *testing.T) {
	h := newHarness(t, writeCall("w1", "/a.txt"))
	if _, paused := h.start(t, runtime.RunConfig{ThreadID: "t"}); paused == nil {
		t.Fatal("expected an interrupt")
	}
	for _, ds := range [][]Decision{{{Type: DecisionEdit}}, {}, {{Type: DecisionApprove}, {Type: DecisionApprove}}} {
		_, err := h.agent.Invoke(context.Background(), agent.Input{Resume: &runtime.Command{Resume: map[string]any{"after_model": ds}}},
			runtime.RunConfig{ThreadID: "t"})
		if err == nil {
			t.Errorf("decisions %+v: want error", ds)
		}
	}
}

var allDecisions = map[string]InterruptOnConfig{"write_file": {
	AllowedDecisions: []DecisionType{DecisionApprove, DecisionEdit, DecisionReject, DecisionRespond},
}}

func toolMessage(st *runtime.State, id string) *llm.Message {
	for i := range st.Messages {
		if st.Messages[i].Role == llm.RoleTool && st.Messages[i].ToolCallID == id {
			return &st.Messages[i]
		}
	}
	return nil
}

func TestEditRunsToolWithEditedArgsAndReplacesAIMessage(t *testing.T) {
	h := newHarnessWith(t, allDecisions, writeCall("w1", "/a.txt"), llm.NewAssistantMessage("done"))
	if _, paused := h.start(t, runtime.RunConfig{ThreadID: "t"}); paused == nil {
		t.Fatal("expected an interrupt")
	}
	st, _ := h.resume(t, Decision{Type: DecisionEdit, EditedArgs: map[string]any{"file_path": "/b.txt"}})
	if strings.Join(h.calls, ",") != "/b.txt" {
		t.Fatalf("tool calls = %v", h.calls)
	}
	var ais []llm.Message
	for _, m := range st.Messages {
		if m.Role == llm.RoleAssistant && len(m.ToolCalls) > 0 {
			ais = append(ais, m)
		}
	}
	if len(ais) != 1 || ais[0].ToolCalls[0].Args["file_path"] != "/b.txt" {
		t.Fatalf("assistant tool-call messages = %+v", ais)
	}
}

func TestEditWithoutArgsFails(t *testing.T) {
	h := newHarnessWith(t, allDecisions, writeCall("w1", "/a.txt"))
	if _, paused := h.start(t, runtime.RunConfig{ThreadID: "t"}); paused == nil {
		t.Fatal("expected an interrupt")
	}
	_, err := h.agent.Invoke(context.Background(), agent.Input{Resume: &runtime.Command{Resume: map[string]any{
		"after_model": []Decision{{Type: DecisionEdit}},
	}}}, runtime.RunConfig{ThreadID: "t"})
	if err == nil || !strings.Contains(err.Error(), "edited_args") {
		t.Fatalf("err = %v", err)
	}
}

func TestRespondSkipsToolAndReturnsMessageToModel(t *testing.T) {
	h := newHarnessWith(t, allDecisions, writeCall("w1", "/a.txt"), llm.NewAssistantMessage("thanks"))
	if _, paused := h.start(t, runtime.RunConfig{ThreadID: "t"}); paused == nil {
		t.Fatal("expected an interrupt")
	}
	st, _ := h.resume(t, Decision{Type: DecisionRespond, Message: "already written"})
	if len(h.calls) != 0 {
		t.Fatalf("responded tool ran: %v", h.calls)
	}
	msg := toolMessage(st, "w1")
	if msg == nil || msg.Status != llm.ToolStatusSuccess || msg.Text() != "already written" {
		t.Fatalf("respond message = %+v", msg)
	}
	if n := len(h.model.Requests()); n != 2 {
		t.Fatalf("model calls = %d, want 2", n)
	}
}

func TestMixedDecisionsInOneTurn(t *testing.T) {
	turn := llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{
		{ID: "a", Name: "write_file", Args: map[string]any{"file_path": "/a"}},
		{ID: "e", Name: "write_file", Args: map[string]any{"file_path": "/e"}},
		{ID: "r", Name: "write_file", Args: map[string]any{"file_path": "/r"}},
	}}
	h := newHarnessWith(t, allDecisions, turn, llm.NewAssistantMessage("done"))
	if _, paused := h.start(t, runtime.RunConfig{ThreadID: "t"}); paused == nil {
		t.Fatal("expected an interrupt")
	}
	st, _ := h.resume(t,
		Decision{Type: DecisionApprove},
		Decision{Type: DecisionEdit, EditedArgs: map[string]any{"file_path": "/edited"}},
		Decision{Type: DecisionReject},
	)
	slices.Sort(h.calls)
	if strings.Join(h.calls, ",") != "/a,/edited" {
		t.Fatalf("tool calls = %v", h.calls)
	}
	if msg := toolMessage(st, "r"); msg == nil || msg.Status != llm.ToolStatusError {
		t.Fatalf("rejected call message = %+v", msg)
	}
	if msg := toolMessage(st, "e"); msg == nil || msg.Text() != "wrote /edited" {
		t.Fatalf("edited call message = %+v", msg)
	}
}

func TestEditSurvivesPostgresResume(t *testing.T) {
	conn := os.Getenv("POSTGRES_CONN_STRING")
	if conn == "" {
		t.Skip("POSTGRES_CONN_STRING is not set")
	}
	cp, err := runtime.NewPostgresCheckpointer(context.Background(), conn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cp.Close)
	thread := fmt.Sprintf("pg-hitl-edit-%d", time.Now().UnixNano())
	t.Cleanup(func() { _ = cp.DeleteThread(context.Background(), thread) })

	h := newHarnessOn(t, cp, thread, allDecisions, writeCall("w1", "/a.txt"), llm.NewAssistantMessage("done"))
	if _, paused := h.start(t, runtime.RunConfig{ThreadID: thread}); paused == nil {
		t.Fatal("expected an interrupt")
	}
	st, _ := h.resume(t, Decision{Type: DecisionEdit, EditedArgs: map[string]any{"file_path": "/b.txt"}})
	if strings.Join(h.calls, ",") != "/b.txt" {
		t.Fatalf("tool calls = %v", h.calls)
	}
	if msg := toolMessage(st, "w1"); msg == nil || msg.Text() != "wrote /b.txt" {
		t.Fatalf("edited call message = %+v", msg)
	}
}

func TestDecodeDecisionsAcceptsGenericJSON(t *testing.T) {
	d, err := DecodeDecisions([]any{map[string]any{"type": "reject", "message": "no"}})
	if err != nil || len(d) != 1 || d[0].Type != DecisionReject || d[0].Message != "no" {
		t.Fatalf("decoded = %+v, %v", d, err)
	}
	d, err = DecodeDecisions([]any{map[string]any{"type": "edit", "edited_args": map[string]any{"file_path": "/b"}}})
	if err != nil || len(d) != 1 || d[0].Type != DecisionEdit || d[0].EditedArgs["file_path"] != "/b" {
		t.Fatalf("decoded edit = %+v, %v", d, err)
	}
}

func TestMerge(t *testing.T) {
	if Merge(nil, nil) != nil {
		t.Fatal("empty merge must be nil")
	}
	got := Merge(map[string]InterruptOnConfig{"a": {Description: "gen"}, "b": {}}, map[string]InterruptOnConfig{"a": {Description: "user"}})
	if len(got) != 2 || got["a"].Description != "user" {
		t.Fatalf("merge = %+v", got)
	}
}
