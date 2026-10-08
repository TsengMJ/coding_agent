package agent

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	core "github.com/tsengmingchun/quill/apps/agent/agent"
	"github.com/tsengmingchun/quill/apps/agent/code/codetools"
	"github.com/tsengmingchun/quill/apps/agent/llm"
	rt "github.com/tsengmingchun/quill/apps/agent/runtime"
	"github.com/tsengmingchun/quill/apps/agent/tool"
)

func repairCall(t *testing.T, state *rt.State, args map[string]any) (*tool.Result, error) {
	t.Helper()
	tools := RepairTracker{}.Tools()
	if len(tools) != 1 || tools[0].Name() != RepairToolName {
		t.Fatalf("tools = %v", tools)
	}
	if state == nil {
		state = &rt.State{Values: map[string]any{}}
	}
	return tools[0].Invoke(context.Background(), &tool.Runtime{ToolCallID: "r", State: state}, args)
}

// applyRepair applies a repair tool result to state through the reducer.
func applyRepair(t *testing.T, state *rt.State, res *tool.Result) {
	t.Helper()
	if res.Command == nil || res.Command.Update == nil {
		t.Fatalf("no update in %+v", res)
	}
	if err := state.Apply(*res.Command.Update, RepairTracker{}.StateSchema()); err != nil {
		t.Fatal(err)
	}
}

func TestRepairToolRecordsEachOp(t *testing.T) {
	state := &rt.State{Values: map[string]any{}}
	for _, args := range []map[string]any{
		{"op": "set_task", "expected": []any{"deleted user => 404"}, "constraints": []any{"keep the public API"}, "verification": []any{"go test ./internal/user -run TestGetDeletedUser"}},
		{"op": "set_suspects", "suspects": []any{map[string]any{"file": "internal/user/service.go", "symbol": "GetUser", "confidence": 0.8}}},
		{"op": "set_hypothesis", "hypothesis": "service maps ErrNotFound to InternalError", "files": []any{"internal/user/service.go"}, "scope": "service layer"},
		{"op": "note", "note": "repository returns sql.ErrNoRows"},
	} {
		res, err := repairCall(t, state, args)
		if err != nil {
			t.Fatalf("%v: %v", args["op"], err)
		}
		applyRepair(t, state, res)
	}
	rec, err := parseRepairRecord(state.Values[RepairStateKey])
	if err != nil {
		t.Fatal(err)
	}
	if rec.Task == nil || rec.Task.Expected[0] != "deleted user => 404" || rec.Task.Verification[0] == "" {
		t.Fatalf("task = %+v", rec.Task)
	}
	if len(rec.Suspects) != 1 || rec.Suspects[0].Symbol != "GetUser" || rec.Suspects[0].Confidence != 0.8 {
		t.Fatalf("suspects = %+v", rec.Suspects)
	}
	if rec.Hypothesis != "service maps ErrNotFound to InternalError" || rec.Intent == nil || rec.Intent.Scope != "service layer" {
		t.Fatalf("hypothesis = %q intent = %+v", rec.Hypothesis, rec.Intent)
	}
	if !slices.Equal(rec.Notes, []string{"repository returns sql.ErrNoRows"}) {
		t.Fatalf("notes = %q", rec.Notes)
	}
}

func TestRepairToolRejectsBadArgs(t *testing.T) {
	for _, args := range []map[string]any{
		{"op": "guess"},
		{"op": "set_task"},
		{"op": "set_hypothesis"},
		{"op": "set_suspects", "suspects": []any{map[string]any{"symbol": "X"}}},
		{"op": "note", "note": " "},
	} {
		res, err := repairCall(t, nil, args)
		if err == nil && (res == nil || !res.IsError) {
			t.Errorf("%v accepted", args)
		}
	}
}

func TestRepairReducerMergesPartialUpdates(t *testing.T) {
	cur := RepairRecord{Task: &TaskSpec{Expected: []string{"a"}}, Hypothesis: "h1", Notes: []string{"n1", "n2", "n3", "n4", "n5"}}
	// Checkpoints store state values as JSON, so the current value may be a map.
	raw, _ := json.Marshal(cur)
	var asMap map[string]any
	if err := json.Unmarshal(raw, &asMap); err != nil {
		t.Fatal(err)
	}
	merged, err := reduceRepair(asMap, RepairRecord{Hypothesis: "h2", Notes: []string{"n6"}})
	if err != nil {
		t.Fatal(err)
	}
	rec := merged.(RepairRecord)
	if rec.Task == nil || rec.Task.Expected[0] != "a" || rec.Hypothesis != "h2" {
		t.Fatalf("merged = %+v", rec)
	}
	if !slices.Equal(rec.Notes, []string{"n2", "n3", "n4", "n5", "n6"}) {
		t.Fatalf("notes = %q", rec.Notes)
	}
}

func goFailure(tests ...string) string {
	var b strings.Builder
	for _, t := range tests {
		b.WriteString("--- FAIL: " + t + " (0.01s)\n    x_test.go:9: wrong\n")
	}
	return b.String() + "FAIL\n\n[Command failed with exit code 1]"
}

var (
	goTest   = map[string]any{"command": "go test ./..."}
	goPassed = "ok\tm\t0.1s\n\n[Command succeeded with exit code 0]"
	hypo     = step{name: RepairToolName, args: map[string]any{"op": "set_hypothesis", "hypothesis": "off by one in Two"}, result: `{"ok":true}`}
	editA    = step{name: "edit_file", args: map[string]any{"file_path": "/a.go"}, result: "Successfully replaced 1 instance(s)"}
)

func TestDeriveRepairTracksBaselineAndAttempts(t *testing.T) {
	msgs := transcript(
		step{name: "execute", args: goTest, result: goFailure("TestA", "TestB")},
		hypo,
		editA,
		step{name: "execute", args: goTest, result: goFailure("TestB")},
		step{name: "edit_file", args: map[string]any{"file_path": "/b.go"}, result: "Successfully replaced 1 instance(s)"},
		step{name: "execute", args: goTest, result: goPassed},
	)
	v := deriveRepair(msgs)
	if v.Baseline == nil || !slices.Equal(v.Baseline.Tests, []string{"TestA", "TestB"}) {
		t.Fatalf("baseline = %+v", v.Baseline)
	}
	if len(v.Attempts) != 2 {
		t.Fatalf("attempts = %+v", v.Attempts)
	}
	first, second := v.Attempts[0], v.Attempts[1]
	if first.Hypothesis != "off by one in Two" || !slices.Equal(first.Files, []string{"/a.go"}) || first.Delta == nil || first.Delta.Kind != DeltaProgressed {
		t.Fatalf("first attempt = %+v", first)
	}
	if second.Delta == nil || second.Delta.Kind != DeltaFixed || second.Result.Category != FailurePassed {
		t.Fatalf("second attempt = %+v", second)
	}
	if v.Phase != PhaseVerified {
		t.Fatalf("phase = %q", v.Phase)
	}
}

func TestDeriveRepairPhases(t *testing.T) {
	tests := []struct {
		name  string
		steps []step
		phase RepairPhase
		next  string
	}{
		{"nothing yet", []step{{name: "read_file", args: map[string]any{"file_path": "/a.go"}, result: "x"}}, PhaseUnderstand, ""},
		{"reproduced", []step{{name: "execute", args: goTest, result: goFailure("TestA")}}, PhaseReproduced, "hypothesis"},
		{"patching", []step{{name: "execute", args: goTest, result: goFailure("TestA")}, editA}, PhasePatching, "go test ./..."},
		{"unchanged failure", []step{{name: "execute", args: goTest, result: goFailure("TestA")}, editA, {name: "execute", args: goTest, result: goFailure("TestA")}}, PhaseDiagnosing, "did not change the failure"},
		{"regressed", []step{{name: "execute", args: goTest, result: goFailure("TestA")}, editA, {name: "execute", args: goTest, result: goFailure("TestA", "TestC")}}, PhaseDiagnosing, "worse"},
		{"edit without baseline", []step{editA}, PhasePatching, "Run the relevant tests"},
		{"check blocked by the environment", []step{{name: "execute", args: goTest, result: goFailure("TestA")}, editA, offline()}, PhaseDiagnosing, "could not run because of the environment (network)"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v := deriveRepair(transcript(tc.steps...))
			if v.Phase != tc.phase {
				t.Fatalf("phase = %q, want %q", v.Phase, tc.phase)
			}
			if !strings.Contains(v.Next, tc.next) {
				t.Fatalf("next = %q, want it to mention %q", v.Next, tc.next)
			}
		})
	}
}

func TestDeriveRepairOnlyReadsCurrentTurn(t *testing.T) {
	old := transcript(step{name: "execute", args: goTest, result: goFailure("TestOld")}, editA)
	msgs := append(old, llm.NewUserMessage("now something else"))
	v := deriveRepair(msgs)
	if v.Baseline != nil || len(v.Attempts) != 0 || v.Phase != PhaseUnderstand {
		t.Fatalf("view = %+v", v)
	}
}

func TestDeriveRepairIgnoresFailedEdits(t *testing.T) {
	msgs := transcript(
		step{name: "execute", args: goTest, result: goFailure("TestA")},
		step{name: "edit_file", args: map[string]any{"file_path": "/a.go"}, result: "Error: string not found", failed: true},
	)
	if v := deriveRepair(msgs); v.Phase != PhaseReproduced {
		t.Fatalf("phase = %q", v.Phase)
	}
}

func trackerRequest(t *testing.T, state *rt.State) (*core.ModelRequest, *core.ModelRequest) {
	t.Helper()
	sys := llm.NewSystemMessage("sys")
	req := &core.ModelRequest{SystemMessage: &sys, Messages: slices.Clone(state.Messages), State: state}
	var sent *core.ModelRequest
	_, err := RepairTracker{}.WrapModelCall(context.Background(), req, func(_ context.Context, r *core.ModelRequest) (*core.ModelResponse, error) {
		sent = r
		return &core.ModelResponse{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return req, sent
}

func TestRepairStateIsAddedAfterTheConversation(t *testing.T) {
	msgs := transcript(step{name: "execute", args: goTest, result: goFailure("TestA")}, editA)
	msgs = msgs[:len(msgs)-1] // drop the closing assistant reply
	state := &rt.State{Messages: msgs, Values: map[string]any{RepairStateKey: RepairRecord{Hypothesis: "off by one"}}}
	req, sent := trackerRequest(t, state)
	if len(req.Messages) != len(msgs) {
		t.Fatalf("original request changed: %d messages", len(req.Messages))
	}
	if len(sent.Messages) != len(msgs)+1 || sent.SystemMessage.Text() != "sys" {
		t.Fatalf("sent %d messages, system %q", len(sent.Messages), sent.SystemMessage.Text())
	}
	for i := range msgs {
		if sent.Messages[i].Text() != msgs[i].Text() {
			t.Fatalf("message %d changed", i)
		}
	}
	tail := sent.Messages[len(sent.Messages)-1]
	if tail.Role != llm.RoleSystem || tail.Metadata[rt.SyntheticSourceKey] != RepairStateSource || rt.IsUserTurn(tail) {
		t.Fatalf("tail = %+v", tail)
	}
	text := tail.Text()
	for _, want := range []string{"<repair-state>", "phase: patching", `hypothesis: "off by one"`, "baseline:", "TestA", "next:"} {
		if !strings.Contains(text, want) {
			t.Fatalf("repair state lacks %q:\n%s", want, text)
		}
	}
}

func TestRepairStateIsOmittedWithNothingToTrack(t *testing.T) {
	state := &rt.State{Messages: []llm.Message{llm.NewUserMessage("what does main.go do?")}, Values: map[string]any{}}
	if _, sent := trackerRequest(t, state); len(sent.Messages) != 1 {
		t.Fatalf("added %d messages", len(sent.Messages)-1)
	}
}

func TestRenderRepairStateShowsRecentAttempts(t *testing.T) {
	var steps []step
	steps = append(steps, step{name: "execute", args: goTest, result: goFailure("TestA")})
	for i := 0; i < 7; i++ {
		steps = append(steps, editA, step{name: "execute", args: goTest, result: goFailure("TestA")})
	}
	text := renderRepairState(RepairRecord{}, deriveRepair(transcript(steps...)))
	if !strings.Contains(text, "7 attempts") || strings.Count(text, "- attempt ") != maxShownAttempts {
		t.Fatalf("render:\n%s", text)
	}
}

func TestAgentKeepsRepairRecord(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module m\n\ngo 1.21\n")
	writeFile(t, filepath.Join(dir, "a.go"), "package m\n\nfunc Two() int { return 3 }\n")
	writeFile(t, filepath.Join(dir, "a_test.go"), "package m\n\nimport \"testing\"\n\nfunc TestTwo(t *testing.T) {\n\tif Two() != 2 {\n\t\tt.Fatal(\"want 2\")\n\t}\n}\n")
	call := func(id, name string, args map[string]any) llm.Message {
		return llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: id, Name: name, Args: args}}}
	}
	model := llm.NewFakeModel(
		call("k", RepairToolName, map[string]any{"op": "set_task", "expected": []any{"Two returns 2"}, "verification": []any{"go test ./..."}}),
		call("t1", "execute", goTest),
		call("h", RepairToolName, map[string]any{"op": "set_hypothesis", "hypothesis": "Two returns a literal 3", "files": []any{"a.go"}}),
		call("r", "read_file", map[string]any{"file_path": "/a.go"}),
		call("e", "edit_file", map[string]any{"file_path": "/a.go", "old_string": "return 3", "new_string": "return 2"}),
		call("t2", "execute", goTest),
		llm.NewAssistantMessage("Fixed."),
	)
	a, err := CreateCLIAgent(context.Background(), Options{Model: model, Cwd: dir, Backend: NewBackend(dir), AutoApprove: true, NoWebTools: true, NoVerifyGate: true})
	if err != nil {
		t.Fatal(err)
	}
	st, err := a.Invoke(context.Background(), core.Input{Messages: []llm.Message{llm.NewUserMessage("make Two return 2")}}, rt.RunConfig{})
	if err != nil {
		t.Fatal(err)
	}
	rec, err := parseRepairRecord(st.Values[RepairStateKey])
	if err != nil || rec.Task == nil || rec.Hypothesis != "Two returns a literal 3" {
		t.Fatalf("record = %+v, %v", rec, err)
	}
	reqs := model.Requests()
	last := reqs[len(reqs)-1]
	var state string
	for _, m := range last.Messages {
		if m.Metadata[rt.SyntheticSourceKey] == RepairStateSource {
			state = m.Text()
		}
	}
	for _, want := range []string{"phase: verified", "Two returns 2", "delta: fixed", "hypothesis: \"Two returns a literal 3\""} {
		if !strings.Contains(state, want) {
			t.Fatalf("last request repair state lacks %q:\n%s", want, state)
		}
	}
	if !slices.ContainsFunc(reqs[0].Tools, func(s llm.ToolSpec) bool { return s.Name == RepairToolName }) {
		t.Fatalf("repair tool not offered")
	}
	_ = codetools.RunTestsName
}
