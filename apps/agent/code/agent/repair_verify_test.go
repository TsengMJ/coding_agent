package agent

import (
	"context"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	core "github.com/tsengmingchun/quill/apps/agent/agent"
	"github.com/tsengmingchun/quill/apps/agent/backend"
	"github.com/tsengmingchun/quill/apps/agent/code/codetools"
	"github.com/tsengmingchun/quill/apps/agent/llm"
	rt "github.com/tsengmingchun/quill/apps/agent/runtime"
	"github.com/tsengmingchun/quill/apps/agent/tool"
)

// goPlan is the plan for a change to svc/a/a.go: its package, the package
// importing it, then build and lint.
var goPlan = planFromRelated(codetools.Related{Language: "go", Dir: "svc", Packages: []string{"./a"}, Dependents: []string{"./b"}})

func TestPlanFromRelatedOrdersLevels(t *testing.T) {
	var levels []VerifyLevel
	for _, c := range goPlan.Checks {
		levels = append(levels, c.Level)
	}
	if !slices.Equal(levels, []VerifyLevel{LevelPackage, LevelRelated, LevelBuildLint}) {
		t.Fatalf("levels = %v", levels)
	}
	pkg := goPlan.Checks[0]
	if pkg.Tool != codetools.RunTestsName || pkg.Args["dir"] != "svc" || !slices.Equal(stringArgs(pkg.Args["packages"]), []string{"./a"}) {
		t.Fatalf("package check = %+v", pkg)
	}
	if lint := goPlan.Checks[2]; lint.Tool != codetools.DiagnosticsName || !slices.Equal(stringArgs(lint.Args["packages"]), []string{"./..."}) {
		t.Fatalf("lint check = %+v", lint)
	}
	node := planFromRelated(codetools.Related{Language: "node", Tests: []string{"test/user.test.js"}, Importers: []string{"test/api.spec.js"}})
	if len(node.Checks) != 3 || !slices.Equal(stringArgs(node.Checks[1].Args["files"]), []string{"test/api.spec.js"}) {
		t.Fatalf("node plan = %+v", node.Checks)
	}
	if bare := planFromRelated(codetools.Related{Language: "python"}); len(bare.Checks) != 1 || bare.Checks[0].Level != LevelBuildLint {
		t.Fatalf("plan without tests = %+v", bare.Checks)
	}
}

func testsRun(args map[string]any, result string) step {
	return step{name: codetools.RunTestsName, args: args, result: result}
}

func TestVerificationReached(t *testing.T) {
	pass := passReport(t)
	fail := failReport(t)
	pkgA := map[string]any{"dir": "svc", "packages": []any{"./a"}}
	pkgB := map[string]any{"dir": "svc", "packages": []any{"./b"}}
	edit := step{name: "edit_file", args: map[string]any{"file_path": "/svc/a/a.go"}, result: "Successfully replaced 1 instance(s)"}
	diag := step{name: codetools.DiagnosticsName, args: map[string]any{"dir": "svc"}, result: "[]"}
	for _, tc := range []struct {
		name  string
		steps []step
		want  VerifyLevel
	}{
		{"no check", []step{edit}, LevelNone},
		{"failing check", []step{edit, testsRun(pkgA, fail)}, LevelNone},
		{"filtered run", []step{edit, testsRun(map[string]any{"dir": "svc", "packages": []any{"./a"}, "run": "TestA"}, pass)}, LevelTargeted},
		{"package", []step{edit, testsRun(pkgA, pass)}, LevelPackage},
		{"related", []step{edit, testsRun(pkgA, pass), testsRun(pkgB, pass)}, LevelRelated},
		{"whole module", []step{edit, testsRun(map[string]any{"dir": "svc", "packages": []any{"./..."}}, pass)}, LevelRelated},
		{"shell go test ./...", []step{edit, {name: "execute", args: map[string]any{"command": "cd svc && go test ./..."}, result: "ok\n\n[Command succeeded with exit code 0]"}}, LevelRelated},
		{"build and lint", []step{edit, testsRun(map[string]any{"dir": "svc"}, pass), diag}, LevelBuildLint},
		{"checks before the last edit do not count", []step{testsRun(pkgA, pass), testsRun(pkgB, pass), edit}, LevelNone},
		{"other dir", []step{edit, testsRun(map[string]any{"packages": []any{"./a"}}, pass)}, LevelTargeted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := verificationReached(transcript(tc.steps...), goPlan); got != tc.want {
				t.Fatalf("reached = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestVerificationReachedForNodeFiles(t *testing.T) {
	plan := planFromRelated(codetools.Related{Language: "node", Tests: []string{"test/user.test.js"}, Importers: []string{"test/api.spec.js"}})
	edit := step{name: "edit_file", args: map[string]any{"file_path": "/src/user.js"}, result: "Successfully replaced 1 instance(s)"}
	pass := passReport(t)
	one := transcript(edit, testsRun(map[string]any{"files": []any{"test/user.test.js"}}, pass))
	if got := verificationReached(one, plan); got != LevelPackage {
		t.Fatalf("one file = %v", got)
	}
	dir := transcript(edit, testsRun(map[string]any{"files": []any{"test"}}, pass))
	if got := verificationReached(dir, plan); got != LevelRelated {
		t.Fatalf("test dir = %v", got)
	}
	npm := transcript(edit, step{name: "execute", args: map[string]any{"command": "npm test"}, result: "5 passing\n\n[Command succeeded with exit code 0]"})
	if got := verificationReached(npm, plan); got != LevelRelated {
		t.Fatalf("npm test = %v", got)
	}
}

// fixedPlanner returns the same plan for any files and records the calls.
type fixedPlanner struct {
	plan  VerificationPlan
	files *[]string
}

func (p fixedPlanner) Plan(_ context.Context, files []string) VerificationPlan {
	if p.files != nil {
		*p.files = files
	}
	return p.plan
}

func TestVerifyGateAsksToWidenVerificationOnce(t *testing.T) {
	var asked []string
	g := VerifyGate{Planner: fixedPlanner{plan: goPlan, files: &asked}}
	edit := step{name: "edit_file", args: map[string]any{"file_path": "/svc/a/a.go"}, result: "Successfully replaced 1 instance(s)"}
	msgs := transcript(edit, testsRun(map[string]any{"dir": "svc", "packages": []any{"./a"}}, passReport(t)))
	cmd, err := g.AfterAgent(context.Background(), &rt.State{Messages: msgs}, nil)
	if err != nil || cmd == nil {
		t.Fatalf("cmd = %v, err = %v", cmd, err)
	}
	if !slices.Equal(asked, []string{"svc/a/a.go"}) {
		t.Fatalf("planned for %v", asked)
	}
	m := cmd.Update.Messages[0]
	text := m.Text()
	if m.Metadata[GateKindKey] != gateKindWiden || !strings.HasPrefix(text, verifyMarker) ||
		!strings.Contains(text, "reached: package") || !strings.Contains(text, `"./b"`) || !strings.Contains(text, "diagnostics") {
		t.Fatalf("message = %v\n%s", m.Metadata, text)
	}
	again, _ := g.AfterAgent(context.Background(), &rt.State{Messages: append(msgs, m, llm.NewAssistantMessage("done"))}, nil)
	if again != nil {
		t.Fatalf("second widen request: %s", again.Update.Messages[0].Text())
	}
}

func TestVerifyGateLetsRelatedVerificationFinish(t *testing.T) {
	g := VerifyGate{Planner: fixedPlanner{plan: goPlan}}
	edit := step{name: "edit_file", args: map[string]any{"file_path": "/svc/a/a.go"}, result: "Successfully replaced 1 instance(s)"}
	msgs := transcript(edit, testsRun(map[string]any{"dir": "svc", "packages": []any{"./..."}}, passReport(t)))
	if cmd, _ := g.AfterAgent(context.Background(), &rt.State{Messages: msgs}, nil); cmd != nil {
		t.Fatalf("gate blocked: %s", cmd.Update.Messages[0].Text())
	}
}

func TestWidenRequestDoesNotUseACheckReminder(t *testing.T) {
	edit := step{name: "edit_file", args: map[string]any{"file_path": "/svc/a/a.go"}, result: "Successfully replaced 1 instance(s)"}
	msgs := transcript(edit)
	widen := llm.NewUserMessage(verifyMarker + " widen")
	widen.Metadata = map[string]any{rt.SyntheticSourceKey: VerifyGateName, GateKindKey: gateKindWiden}
	msgs = append(msgs, widen)
	if got := inspectTurn(msgs).blocks; got != 0 {
		t.Fatalf("blocks = %d", got)
	}
}

func TestRepairStateShowsTheVerificationPlan(t *testing.T) {
	msgs := transcript(
		step{name: "execute", args: goTest, result: goFailure("TestA")},
		step{name: "edit_file", args: map[string]any{"file_path": "/svc/a/a.go"}, result: "Successfully replaced 1 instance(s)"},
		testsRun(map[string]any{"dir": "svc", "packages": []any{"./a"}}, passReport(t)),
	)
	var asked []string
	tr := RepairTracker{Planner: fixedPlanner{plan: goPlan, files: &asked}}
	sys := llm.NewSystemMessage("sys")
	var seen []llm.Message
	_, err := tr.WrapModelCall(context.Background(), &core.ModelRequest{SystemMessage: &sys, Messages: msgs, State: &rt.State{Messages: msgs}},
		func(_ context.Context, req *core.ModelRequest) (*core.ModelResponse, error) {
			seen = req.Messages
			return &core.ModelResponse{}, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	tail := seen[len(seen)-1].Text()
	for _, want := range []string{"verification:", "reached: package", "level: related", `"./b"`, "phase: verified", "next: \"Widen verification: run_tests"} {
		if !strings.Contains(tail, want) {
			t.Fatalf("missing %q in:\n%s", want, tail)
		}
	}
	if !slices.Equal(asked, []string{"svc/a/a.go"}) {
		t.Fatalf("planned for %v", asked)
	}
}

func TestVerificationPlannerCachesPerChangeSet(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not installed")
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/m\n\ngo 1.21\n")
	writeFile(t, filepath.Join(dir, "a/a.go"), "package a\n\nfunc A() int { return 1 }\n")
	writeFile(t, filepath.Join(dir, "b/b.go"), "package b\n\nimport \"example.com/m/a\"\n\nfunc B() int { return a.A() }\n")
	shell := &countingShell{SandboxBackend: NewBackend(dir).(backend.SandboxBackend)}
	p := NewVerificationPlanner(shell)
	plan := p.Plan(context.Background(), []string{"a/a.go"})
	if len(plan.Checks) != 3 || !slices.Equal(stringArgs(plan.Checks[1].Args["packages"]), []string{"./b"}) {
		t.Fatalf("plan = %+v", plan.Checks)
	}
	n := shell.n
	p.Plan(context.Background(), []string{"a/a.go"})
	if shell.n != n {
		t.Fatalf("the second plan ran %d more commands", shell.n-n)
	}
}

type countingShell struct {
	backend.SandboxBackend
	n int
}

func (s *countingShell) Execute(ctx context.Context, command string, timeout *time.Duration) backend.ExecuteResponse {
	s.n++
	return s.SandboxBackend.Execute(ctx, command, timeout)
}

func reproNote(t *testing.T, history []llm.Message, call llm.ToolCall, result string, rec *RepairRecord) string {
	t.Helper()
	st := &rt.State{Messages: history, Values: map[string]any{}}
	if rec != nil {
		st.Values[RepairStateKey] = *rec
	}
	res, err := ReproReminder{}.WrapToolCall(context.Background(), &core.ToolCallRequest{ToolCall: call, State: st},
		func(context.Context, *core.ToolCallRequest) (*tool.Result, error) {
			return &tool.Result{Content: []llm.ContentBlock{llm.TextBlock(result)}}, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	return resultText(res)
}

func TestReproReminder(t *testing.T) {
	editA := llm.ToolCall{ID: "e9", Name: "edit_file", Args: map[string]any{"file_path": "/a.go"}}
	ok := "Successfully replaced 1 instance(s)"
	earlierEdit := step{name: "edit_file", args: map[string]any{"file_path": "/b.go"}, result: ok}
	failing := step{name: "execute", args: goTest, result: goFailure("TestA")}
	envFailure := step{name: "execute", args: goTest, result: "dial tcp 127.0.0.1:6379: connect: connection refused\n\n[Command failed with exit code 1]"}
	for _, tc := range []struct {
		name    string
		history []llm.Message
		call    llm.ToolCall
		result  string
		rec     *RepairRecord
		want    bool
	}{
		{"first edit without a failing check", transcript(), editA, ok, nil, true},
		{"after a failing check", transcript(failing), editA, ok, nil, false},
		{"after an environment failure", transcript(envFailure), editA, ok, nil, true},
		{"second edit", transcript(earlierEdit), editA, ok, nil, false},
		{"failed edit", transcript(), editA, "Error: old_string not found", nil, false},
		{"not code", transcript(), llm.ToolCall{ID: "e9", Name: "write_file", Args: map[string]any{"file_path": "/README.md"}}, "Updated file /README.md", nil, false},
		{"declared feature", transcript(), editA, ok, &RepairRecord{Task: &TaskSpec{Expected: []string{"x"}, Kind: "feature"}}, false},
		{"declared bug", transcript(), editA, ok, &RepairRecord{Task: &TaskSpec{Expected: []string{"x"}, Kind: "bug"}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			text := reproNote(t, tc.history, tc.call, tc.result, tc.rec)
			if got := strings.Contains(text, reproMarker); got != tc.want {
				t.Fatalf("reminded = %v, want %v:\n%s", got, tc.want, text)
			}
			if tc.want && stripNotes(text) != tc.result {
				t.Fatalf("stripNotes left %q", stripNotes(text))
			}
		})
	}
}

func TestRepairToolAcceptsTaskKind(t *testing.T) {
	res, err := runRepairTool(context.Background(), &tool.Runtime{ToolCallID: "c1"}, map[string]any{"op": "set_task", "expected": []any{"x"}, "kind": "refactor"})
	if err != nil {
		t.Fatal(err)
	}
	rec := res.Command.Update.Values[RepairStateKey].(RepairRecord)
	if rec.Task.Kind != "refactor" {
		t.Fatalf("task = %+v", rec.Task)
	}
	if _, err := runRepairTool(context.Background(), &tool.Runtime{}, map[string]any{"op": "set_task", "expected": []any{"x"}, "kind": "chore"}); err == nil {
		t.Fatal("want an error for an unknown kind")
	}
}

func TestAgentWidensVerificationToDependents(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not installed")
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/m\n\ngo 1.21\n")
	writeFile(t, filepath.Join(dir, "a/a.go"), "package a\n\nfunc Two() int { return 3 }\n")
	writeFile(t, filepath.Join(dir, "a/a_test.go"), "package a\n\nimport \"testing\"\n\nfunc TestTwo(t *testing.T) {\n\tif Two() != 2 {\n\t\tt.Fatal(\"want 2\")\n\t}\n}\n")
	writeFile(t, filepath.Join(dir, "b/b.go"), "package b\n\nimport \"example.com/m/a\"\n\nfunc Four() int { return a.Two() * 2 }\n")
	writeFile(t, filepath.Join(dir, "b/b_test.go"), "package b\n\nimport \"testing\"\n\nfunc TestFour(t *testing.T) {\n\tif Four() != 4 {\n\t\tt.Fatal(\"want 4\")\n\t}\n}\n")
	call := func(id, name string, args map[string]any) llm.Message {
		return llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: id, Name: name, Args: args}}}
	}
	model := llm.NewFakeModel(
		call("r1", "read_file", map[string]any{"file_path": "/a/a.go"}),
		call("e1", "edit_file", edit("a/a.go", "return 3", "return 2")),
		call("t1", codetools.RunTestsName, map[string]any{"packages": []any{"./a"}}),
		llm.NewAssistantMessage("Fixed."),
		call("t2", codetools.RunTestsName, map[string]any{"packages": []any{"./b"}}),
		llm.NewAssistantMessage("Fixed, and the dependent package passes."),
	)
	a, err := CreateCLIAgent(context.Background(), Options{Model: model, Cwd: dir, Backend: NewBackend(dir), AutoApprove: true, NoWebTools: true,
		Disable: []string{FeatureBaseline, FeatureDiffReview, FeatureTestEdits}})
	if err != nil {
		t.Fatal(err)
	}
	st, err := a.Invoke(context.Background(), core.Input{Messages: []llm.Message{llm.NewUserMessage("make Two return 2")}}, rt.RunConfig{})
	if err != nil {
		t.Fatal(err)
	}
	var widen []string
	var editResult string
	for _, m := range st.Messages {
		if m.Metadata[GateKindKey] == gateKindWiden {
			widen = append(widen, m.Text())
		}
		if m.Role == llm.RoleTool && m.ToolCallID == "e1" {
			editResult = m.Text()
		}
	}
	if len(widen) != 1 || !strings.Contains(widen[0], `run_tests {"packages":["./b"]}`) {
		t.Fatalf("widen messages = %q", widen)
	}
	if !strings.Contains(editResult, reproMarker) {
		t.Fatalf("edit result = %q", editResult)
	}
	if last := st.Messages[len(st.Messages)-1]; last.Text() != "Fixed, and the dependent package passes." {
		t.Fatalf("last message = %q", last.Text())
	}
}

func passReport(t *testing.T) string {
	return reportJSON(t, codetools.TestReport{Status: "passed", Framework: "go", Passed: 3})
}

func failReport(t *testing.T) string {
	return reportJSON(t, codetools.TestReport{Status: "failed", Framework: "go", Passed: 2, Failed: 1, Failures: []codetools.TestFailure{{Package: "m", Test: "TestA", Message: "want 2"}}})
}
