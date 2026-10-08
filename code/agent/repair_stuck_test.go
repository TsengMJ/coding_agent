package agent

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	core "github.com/tsengmingchun/quill/agent"
	"github.com/tsengmingchun/quill/llm"
	rt "github.com/tsengmingchun/quill/runtime"
	"github.com/tsengmingchun/quill/tool"
)

// editN is a distinct successful edit, so repeats are not the same patch.
func editN(n int) step {
	return step{name: "edit_file", args: map[string]any{"file_path": "/a.go", "old_string": fmt.Sprint(n), "new_string": fmt.Sprint(n + 1)}, result: "Successfully replaced 1 instance(s)"}
}

func failing(tests ...string) step {
	return step{name: "execute", args: goTest, result: goFailure(tests...)}
}

// attempts is a baseline failure of TestA followed by n attempts that
// leave it failing.
func attempts(n int) []step {
	steps := []step{failing("TestA")}
	for i := 0; i < n; i++ {
		steps = append(steps, editN(i), failing("TestA"))
	}
	return steps
}

func readStep(file string) step {
	return step{name: "read_file", args: map[string]any{"file_path": file}, result: "x"}
}

func TestDetectStuckSameFailure(t *testing.T) {
	if r, ok := detectStuck(transcript(attempts(2)...)); ok {
		t.Fatalf("stuck after 2 attempts: %+v", r)
	}
	r, ok := detectStuck(transcript(attempts(3)...))
	if !ok || r.Rule != stuckSameFailure || !strings.Contains(r.Detail, "TestA") {
		t.Fatalf("report = %+v, %v", r, ok)
	}
}

func TestDetectStuckIgnoresProgress(t *testing.T) {
	msgs := transcript(failing("TestA", "TestB"), editN(0), failing("TestB"), editN(1), failing("TestB"), editN(2), failing("TestB"))
	if r, ok := detectStuck(msgs); ok {
		t.Fatalf("stuck although the first attempt progressed: %+v", r)
	}
}

func TestDetectStuckNoProgress(t *testing.T) {
	msgs := transcript(failing("TestA"), editN(0), failing("TestB"), editN(1), failing("TestC"), editN(2), failing("TestD"))
	r, ok := detectStuck(msgs)
	if !ok || r.Rule != stuckNoProgress {
		t.Fatalf("report = %+v, %v", r, ok)
	}
}

func TestDetectStuckSamePatch(t *testing.T) {
	write := step{name: "write_file", args: map[string]any{"file_path": "/a.go", "content": "package m\n"}, result: "Updated file /a.go"}
	r, ok := detectStuck(transcript(failing("TestA"), write, failing("TestA"), write))
	if !ok || r.Rule != stuckSamePatch || !strings.Contains(r.Detail, "/a.go") {
		t.Fatalf("report = %+v, %v", r, ok)
	}
	if r, ok := detectStuck(transcript(write, step{name: "execute", args: goTest, result: goPassed}, write)); ok {
		t.Fatalf("stuck although the first patch passed: %+v", r)
	}
}

// offline is a go test run that could not download a module.
func offline() step {
	return step{name: "execute", args: goTest, result: "go: downloading example.com/dep v1.0.0\ndial tcp: lookup proxy.golang.org: no such host\n\n[Command failed with exit code 1]"}
}

func TestDetectStuckEnvironment(t *testing.T) {
	r, ok := detectStuck(transcript(failing("TestA"), editN(0), offline(), editN(1), offline(), editN(2), offline()))
	if !ok || r.Rule != stuckEnvironment || !strings.Contains(r.Detail, "network") {
		t.Fatalf("report = %+v, %v", r, ok)
	}
	note := stuckNote(r, "")
	if strings.Contains(note, "set_hypothesis") || !strings.Contains(note, "Stop editing the code") || !strings.Contains(note, "ask the user") {
		t.Fatalf("note:\n%s", note)
	}
}

func TestDetectStuckLeavesEnvironmentAttemptsOutOfTheCodeRules(t *testing.T) {
	msgs := transcript(failing("TestA"), editN(0), failing("TestB"), editN(1), offline(), editN(2), failing("TestC"))
	if r, ok := detectStuck(msgs); ok {
		t.Fatalf("stuck with only 2 attempts that ran the code: %+v", r)
	}
}

func TestDetectStuckSamePatchIgnoresEnvironmentChecks(t *testing.T) {
	write := step{name: "write_file", args: map[string]any{"file_path": "/a.go", "content": "package m\n"}, result: "Updated file /a.go"}
	if r, ok := detectStuck(transcript(failing("TestA"), write, offline(), write)); ok {
		t.Fatalf("an environment failure rejected the patch: %+v", r)
	}
}

func TestStuckRefusalLetsEditsThroughAfterAnEnvironmentNote(t *testing.T) {
	msgs := transcript(failing("TestA"), editN(0), offline(), editN(1), offline(), editN(2), offline())
	msgs = withStuckNote(msgs[:len(msgs)-1], stuckReport{Rule: stuckEnvironment, Detail: "network", Resets: 1})
	if msg := stuckRefusal(msgs); msg != "" {
		t.Fatalf("refused an edit after an environment note: %s", msg)
	}
	if next := deriveRepair(msgs).Next; strings.Contains(next, "set_hypothesis") {
		t.Fatalf("next = %q", next)
	}
}

func TestDetectStuckAlternating(t *testing.T) {
	var steps []step
	for i := 0; i < stuckAlternatingCalls; i++ {
		steps = append(steps, readStep([]string{"/a.go", "/b.go"}[i%2]))
	}
	if r, ok := detectStuck(transcript(steps[:len(steps)-1]...)); ok {
		t.Fatalf("stuck after %d calls: %+v", len(steps)-1, r)
	}
	r, ok := detectStuck(transcript(steps...))
	if !ok || r.Rule != stuckAlternating || !strings.Contains(r.Detail, "read_file") {
		t.Fatalf("report = %+v, %v", r, ok)
	}
	withEdit := slices.Insert(slices.Clone(steps), 4, editN(0))
	if r, ok := detectStuck(transcript(withEdit...)); ok {
		t.Fatalf("stuck although an edit broke the cycle: %+v", r)
	}
}

// withStuckNote appends a reset note to the last tool result, as the
// detector does.
func withStuckNote(msgs []llm.Message, r stuckReport) []llm.Message {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == llm.RoleTool {
			msgs[i] = llm.NewToolMessage(msgs[i].ToolCallID, msgs[i].Name, msgs[i].Text()+stuckNote(r, ""))
			break
		}
	}
	return msgs
}

func TestDetectStuckStartsOverAfterAReset(t *testing.T) {
	steps := attempts(3)
	msgs := withStuckNote(transcript(steps...)[:len(steps)*2+1], stuckReport{Rule: stuckSameFailure, Resets: 1})
	more := transcript(editN(10), failing("TestA"), editN(11), failing("TestA"))
	msgs = append(msgs, relabel(more[1:len(more)-1], "y")...)
	if r, ok := detectStuck(msgs); ok {
		t.Fatalf("stuck again after 2 attempts: %+v", r)
	}
	third := transcript(editN(12), failing("TestA"))
	msgs = append(msgs, relabel(third[1:len(third)-1], "z")...)
	r, ok := detectStuck(msgs)
	if !ok || r.Resets != 2 {
		t.Fatalf("report = %+v, %v", r, ok)
	}
}

// relabel gives the tool calls in msgs fresh ids so they do not collide
// with earlier ones.
func relabel(msgs []llm.Message, prefix string) []llm.Message {
	out := slices.Clone(msgs)
	for i := range out {
		if len(out[i].ToolCalls) > 0 {
			calls := slices.Clone(out[i].ToolCalls)
			for j := range calls {
				calls[j].ID = prefix + calls[j].ID
			}
			out[i].ToolCalls = calls
		}
		if out[i].ToolCallID != "" {
			out[i].ToolCallID = prefix + out[i].ToolCallID
		}
	}
	return out
}

// runStuck runs call through the detector with history as the state; the
// tool returns result. It reports whether the tool ran.
func runStuck(t *testing.T, d StuckDetector, history []llm.Message, call llm.ToolCall, result string) (*tool.Result, bool) {
	t.Helper()
	ran := false
	res, err := d.WrapToolCall(context.Background(), &core.ToolCallRequest{ToolCall: call, State: &rt.State{Messages: history}},
		func(context.Context, *core.ToolCallRequest) (*tool.Result, error) {
			ran = true
			return &tool.Result{Content: []llm.ContentBlock{llm.TextBlock(result)}}, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	return res, ran
}

// stuckHistory is three failed attempts with the reset note on the last
// check, without the closing reply.
func stuckHistory() []llm.Message {
	msgs := transcript(append([]step{hypo}, attempts(3)...)...)
	return withStuckNote(msgs[:len(msgs)-1], stuckReport{Rule: stuckSameFailure, Detail: "TestA", Resets: 1})
}

func TestStuckDetectorAppendsResetNote(t *testing.T) {
	msgs := transcript(attempts(3)...)
	history := msgs[:len(msgs)-2] // the third check has not run yet
	last := msgs[len(msgs)-3].ToolCalls[0]
	res, ran := runStuck(t, StuckDetector{}, history, last, goFailure("TestA"))
	text := resultText(res)
	if !ran || !strings.Contains(text, "\n\n"+stuckMarker+" {") {
		t.Fatalf("ran=%v result:\n%s", ran, text)
	}
	for _, want := range []string{`"rule":"same_failure"`, `"resets":1`, "repair set_hypothesis", "explorer", "git log -p"} {
		if !strings.Contains(text, want) {
			t.Fatalf("note lacks %q:\n%s", want, text)
		}
	}
	if stripNotes(text) != goFailure("TestA") {
		t.Fatalf("stripNotes left:\n%s", stripNotes(text))
	}
}

func TestStuckDetectorRefusesTheNextEditOnce(t *testing.T) {
	history := stuckHistory()
	call := llm.ToolCall{ID: "e1", Name: "edit_file", Args: map[string]any{"file_path": "/a.go", "old_string": "x", "new_string": "y"}}
	res, ran := runStuck(t, StuckDetector{Enforce: true}, history, call, "Successfully replaced 1 instance(s)")
	if ran || !res.IsError || !strings.Contains(resultText(res), "repair set_hypothesis") {
		t.Fatalf("ran=%v result=%+v", ran, res)
	}
	history = append(history, llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{call}}, toolResult(call, res))
	call.ID = "e2"
	if _, ran := runStuck(t, StuckDetector{Enforce: true}, history, call, "Successfully replaced 1 instance(s)"); !ran {
		t.Fatal("refused twice")
	}
}

func toolResult(call llm.ToolCall, res *tool.Result) llm.Message {
	m := llm.NewToolMessage(call.ID, call.Name, resultText(res))
	if res.IsError {
		m.Status = llm.ToolStatusError
	}
	return m
}

func TestStuckDetectorAllowsAnEditAfterANewHypothesis(t *testing.T) {
	call := llm.ToolCall{ID: "e1", Name: "edit_file", Args: map[string]any{"file_path": "/a.go"}}
	for _, tc := range []struct {
		hypothesis string
		allowed    bool
	}{
		{"Two reads the wrong config key", true},
		{"off by one in Two", false}, // the hypothesis before the reset
	} {
		h := step{name: RepairToolName, args: map[string]any{"op": "set_hypothesis", "hypothesis": tc.hypothesis}, result: `{"ok":true}`}
		tail := transcript(h)
		history := append(stuckHistory(), relabel(tail[1:len(tail)-1], "h")...)
		if _, ran := runStuck(t, StuckDetector{Enforce: true}, history, call, "ok"); ran != tc.allowed {
			t.Errorf("hypothesis %q: ran = %v", tc.hypothesis, ran)
		}
	}
}

func TestStuckDetectorOnlyNotesWhenNotEnforcing(t *testing.T) {
	call := llm.ToolCall{ID: "e1", Name: "edit_file", Args: map[string]any{"file_path": "/a.go"}}
	if _, ran := runStuck(t, StuckDetector{}, stuckHistory(), call, "ok"); !ran {
		t.Fatal("interactive edit refused")
	}
}

func TestRepairStateShowsStuck(t *testing.T) {
	text := renderRepairState(RepairRecord{}, deriveRepair(stuckHistory()))
	for _, want := range []string{"stuck: {rule: same_failure, resets: 1", "different"} {
		if !strings.Contains(text, want) {
			t.Fatalf("repair state lacks %q:\n%s", want, text)
		}
	}
}

func TestAgentResetsStrategyWhenStuck(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module m\n\ngo 1.21\n")
	writeFile(t, filepath.Join(dir, "a.go"), "package m\n\nfunc Two() int { return 3 }\n")
	writeFile(t, filepath.Join(dir, "a_test.go"), "package m\n\nimport \"testing\"\n\nfunc TestTwo(t *testing.T) {\n\tif Two() != 2 {\n\t\tt.Fatal(\"want 2\")\n\t}\n}\n")
	call := func(id, name string, args map[string]any) llm.Message {
		return llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: id, Name: name, Args: args}}}
	}
	edit := func(id string, from, to int) llm.Message {
		return call(id, "edit_file", map[string]any{"file_path": "/a.go", "old_string": fmt.Sprintf("return %d", from), "new_string": fmt.Sprintf("return %d", to)})
	}
	test := func(id string) llm.Message { return call(id, "execute", map[string]any{"command": "go test ./..."}) }
	model := llm.NewFakeModel(
		test("t0"),
		call("h1", RepairToolName, map[string]any{"op": "set_hypothesis", "hypothesis": "Two is off by one"}),
		call("r", "read_file", map[string]any{"file_path": "/a.go"}),
		edit("e1", 3, 4), test("t1"),
		edit("e2", 4, 5), test("t2"),
		edit("e3", 5, 6), test("t3"), // third unchanged failure: stuck
		edit("e4", 6, 7), // refused: no new hypothesis
		call("h2", RepairToolName, map[string]any{"op": "set_hypothesis", "hypothesis": "Two should return the constant 2"}),
		edit("e5", 6, 2), test("t4"),
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
	results := map[string]llm.Message{}
	for _, m := range st.Messages {
		if m.Role == llm.RoleTool {
			results[m.ToolCallID] = m
		}
	}
	if !strings.Contains(results["t3"].Text(), stuckMarker+" {") {
		t.Fatalf("t3 has no reset note:\n%s", results["t3"].Text())
	}
	if e4 := results["e4"]; e4.Status != llm.ToolStatusError || !strings.HasPrefix(e4.Text(), stuckRefusalPrefix) {
		t.Fatalf("e4 = %s %q", e4.Status, e4.Text())
	}
	if e5 := results["e5"]; e5.Status == llm.ToolStatusError {
		t.Fatalf("e5 refused: %s", e5.Text())
	}
	if !strings.Contains(results["t4"].Text(), "ok") {
		t.Fatalf("t4 = %s", results["t4"].Text())
	}
}
