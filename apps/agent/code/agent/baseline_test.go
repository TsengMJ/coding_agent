package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	core "github.com/tsengmingchun/quill/apps/agent/agent"
	"github.com/tsengmingchun/quill/apps/agent/llm"
	rt "github.com/tsengmingchun/quill/apps/agent/runtime"
)

const mathGo = "package m\n\nfunc Add(a, b int) int { return a + b }\n\nfunc Sub(a, b int) int { return a - b }\n"

// goRepo makes a committed Go module whose tests pass, plus extra files.
func goRepo(t *testing.T, extra map[string]string) string {
	t.Helper()
	for _, bin := range []string{"git", "go"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skip(bin + " not installed")
		}
	}
	dir := t.TempDir()
	files := map[string]string{
		"go.mod":       "module m\n\ngo 1.21\n",
		"m.go":         mathGo,
		"m_test.go":    "package m\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) { if Add(1, 2) != 3 { t.Fatal(\"add\") } }\n\nfunc TestSub(t *testing.T) { if Sub(3, 1) != 2 { t.Fatal(\"sub\") } }\n",
		".gitignore":   "",
		"README.md":    "m\n",
		"testdata/x.t": "x\n",
	}
	for k, v := range extra {
		files[k] = v
	}
	for name, content := range files {
		writeFile(t, filepath.Join(dir, name), content)
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "-A"}, {"-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "-m", "init"}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	return dir
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, _ := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	return strings.TrimSpace(string(out))
}

func gateFor(dir string) VerifyGate {
	return newVerifyGate(NewBackend(dir), nil)
}

var goTestFailed = step{name: "execute", args: map[string]any{"command": "go test ./..."},
	result: "--- FAIL: TestSub\nFAIL\n\n[Command failed with exit code 1]"}

func runGate(t *testing.T, g VerifyGate, msgs []llm.Message) *rt.Command {
	t.Helper()
	cmd, err := g.AfterAgent(context.Background(), &rt.State{Messages: msgs}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return cmd
}

func TestBaselineProvesRegressionAndRestoresWorkingTree(t *testing.T) {
	dir := goRepo(t, nil)
	broken := strings.Replace(mathGo, "a - b", "a + b", 1)
	writeFile(t, filepath.Join(dir, "m.go"), broken)
	writeFile(t, filepath.Join(dir, "notes.txt"), "untracked work\n")

	cmd := runGate(t, gateFor(dir), transcript(editStep, goTestFailed))
	if cmd == nil {
		t.Fatal("a regression must block")
	}
	msg := cmd.Update.Messages[0]
	if !strings.Contains(msg.Text(), "passes on the unchanged HEAD") || msg.Metadata[GateKindKey] != gateKindBaseline {
		t.Fatalf("message = %q (%v)", msg.Text(), msg.Metadata)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "m.go")); string(got) != broken {
		t.Fatalf("changes were not restored: %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "notes.txt")); string(got) != "untracked work\n" {
		t.Fatalf("untracked file was not restored: %q", got)
	}
	if list := gitOut(t, dir, "stash", "list"); list != "" {
		t.Fatalf("stash left behind: %s", list)
	}
	if ref := gitOut(t, dir, "rev-parse", "-q", "--verify", baselineRef); ref != "" {
		t.Fatalf("backup ref left behind: %s", ref)
	}
}

func TestBaselineSecondFailureDoesNotRerun(t *testing.T) {
	dir := goRepo(t, nil)
	writeFile(t, filepath.Join(dir, "m.go"), strings.Replace(mathGo, "a - b", "a + b", 1))
	msgs := transcript(editStep, goTestFailed)
	cmd := runGate(t, gateFor(dir), msgs)
	msgs = append(msgs, cmd.Update.Messages...)
	msgs = append(msgs, transcript(editStep, goTestFailed)[1:]...)
	second := runGate(t, gateFor(dir), msgs)
	if second == nil || second.Update.Messages[0].Metadata[GateKindKey] != gateKindCheck {
		t.Fatalf("second block should be a plain check message: %+v", second)
	}
}

func TestBaselineNotesFailureThatAlsoHappensOnHead(t *testing.T) {
	dir := goRepo(t, map[string]string{"old_test.go": "package m\n\nimport \"testing\"\n\nfunc TestOld(t *testing.T) { t.Fatal(\"broken before\") }\n"})
	writeFile(t, filepath.Join(dir, "new.go"), "package m\n\nfunc Mul(a, b int) int { return a * b }\n")

	cmd := runGate(t, gateFor(dir), transcript(editStep, goTestFailed))
	if cmd == nil || !strings.Contains(cmd.Update.Messages[0].Text(), "also fails on the unchanged HEAD") {
		t.Fatalf("command = %+v", cmd)
	}
	if _, err := os.Stat(filepath.Join(dir, "new.go")); err != nil {
		t.Fatal("new file lost")
	}
}

func TestBaselineLetsPreexistingRunTestsFailuresFinish(t *testing.T) {
	dir := goRepo(t, map[string]string{"old_test.go": "package m\n\nimport \"testing\"\n\nfunc TestOld(t *testing.T) { t.Fatal(\"broken before\") }\n"})
	writeFile(t, filepath.Join(dir, "new.go"), "package m\n\nfunc Mul(a, b int) int { return a * b }\n")
	g := gateFor(dir)
	args := map[string]any{"language": "go", "packages": []any{"./..."}}
	res, err := g.RunTests.Invoke(context.Background(), nil, args)
	if err != nil {
		t.Fatal(err)
	}
	run := step{name: "run_tests", args: args, result: resultText(res)}
	if !strings.Contains(run.result, "TestOld") {
		t.Fatalf("run_tests did not report the failure: %s", run.result)
	}
	var got []string
	g.OnBaseline = func(v string, tests []string) { got = append(got, v+":"+strings.Join(tests, ",")) }
	if cmd := runGate(t, g, transcript(editStep, run)); cmd != nil {
		t.Fatalf("pre-existing failures should not block: %s", cmd.Update.Messages[0].Text())
	}
	if len(got) != 1 || !strings.HasPrefix(got[0], "preexisting:") || !strings.Contains(got[0], "TestOld") {
		t.Fatalf("reported verdicts = %v", got)
	}
}

func TestBaselineTreatsPassingRerunAsFlaky(t *testing.T) {
	dir := goRepo(t, nil)
	writeFile(t, filepath.Join(dir, "new.go"), "package m\n\nfunc Mul(a, b int) int { return a * b }\n")
	g := gateFor(dir)
	var got []string
	g.OnBaseline = func(v string, _ []string) { got = append(got, v) }
	if cmd := runGate(t, g, transcript(editStep, goTestFailed)); cmd != nil {
		t.Fatalf("a failure that passes on HEAD and on rerun is flaky: %s", cmd.Update.Messages[0].Text())
	}
	if strings.Join(got, ",") != "flaky" {
		t.Fatalf("reported verdicts = %v", got)
	}
}

func TestBaselineReportsRegression(t *testing.T) {
	dir := goRepo(t, nil)
	writeFile(t, filepath.Join(dir, "m.go"), strings.Replace(mathGo, "a - b", "a + b", 1))
	g := gateFor(dir)
	var got []string
	g.OnBaseline = func(v string, _ []string) { got = append(got, v) }
	if cmd := runGate(t, g, transcript(editStep, goTestFailed)); cmd == nil {
		t.Fatal("a regression must block")
	}
	if strings.Join(got, ",") != "regression" {
		t.Fatalf("reported verdicts = %v", got)
	}
}

func TestBaselineCanBeDisabled(t *testing.T) {
	dir := goRepo(t, nil)
	writeFile(t, filepath.Join(dir, "new.go"), "package m\n\nfunc Mul(a, b int) int { return a * b }\n")
	g := newVerifyGate(NewBackend(dir), []string{FeatureBaseline})
	cmd := runGate(t, g, transcript(editStep, goTestFailed))
	if cmd == nil || cmd.Update.Messages[0].Metadata[GateKindKey] != gateKindCheck {
		t.Fatalf("disabled baseline should leave the plain check block: %+v", cmd)
	}
}

// runTestsStep runs run_tests on the repo now and records it as a step.
func runTestsStep(t *testing.T, g VerifyGate) step {
	t.Helper()
	args := map[string]any{"language": "go", "packages": []any{"./..."}}
	res, err := g.RunTests.Invoke(context.Background(), nil, args)
	if err != nil {
		t.Fatal(err)
	}
	return step{name: "run_tests", args: args, result: resultText(res)}
}

const subTestExpects4 = "package m\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) { if Add(1, 2) != 3 { t.Fatal(\"add\") } }\n\nfunc TestSub(t *testing.T) { if Sub(3, 1) != 4 { t.Fatal(\"sub\") } }\n"

func TestTestEditsCatchesTestRewrittenToMatchBrokenCode(t *testing.T) {
	dir := goRepo(t, nil)
	broken := strings.Replace(mathGo, "a - b", "a + b", 1)
	writeFile(t, filepath.Join(dir, "m.go"), broken)
	writeFile(t, filepath.Join(dir, "m_test.go"), subTestExpects4)
	g := gateFor(dir)
	run := runTestsStep(t, g)
	if !strings.Contains(run.result, `"passed"`) {
		t.Fatalf("edited tests should pass: %s", run.result)
	}

	cmd := runGate(t, g, transcript(editStep, run))
	if cmd == nil || cmd.Update.Messages[0].Metadata[GateKindKey] != gateKindTestEdit {
		t.Fatalf("command = %+v", cmd)
	}
	if text := cmd.Update.Messages[0].Text(); !strings.Contains(text, "TestSub") || !strings.Contains(text, "`m_test.go`") {
		t.Fatalf("message = %s", text)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "m.go")); string(got) != broken {
		t.Fatalf("source changes lost: %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "m_test.go")); string(got) != subTestExpects4 {
		t.Fatalf("test changes not restored: %q", got)
	}
	if list := gitOut(t, dir, "stash", "list"); list != "" {
		t.Fatalf("stash left behind: %s", list)
	}
	if ref := gitOut(t, dir, "rev-parse", "-q", "--verify", testEditsRef); ref != "" {
		t.Fatalf("backup ref left behind: %s", ref)
	}
}

func TestTestEditsIgnoresNewTestFiles(t *testing.T) {
	dir := goRepo(t, nil)
	writeFile(t, filepath.Join(dir, "m.go"), mathGo+"\nfunc Mul(a, b int) int { return a * b }\n")
	writeFile(t, filepath.Join(dir, "mul_test.go"), "package m\n\nimport \"testing\"\n\nfunc TestMul(t *testing.T) { if Mul(2, 3) != 6 { t.Fatal(\"mul\") } }\n")
	g := gateFor(dir)
	if cmd := runGate(t, g, transcript(editStep, runTestsStep(t, g))); cmd != nil {
		t.Fatalf("new tests need no review: %s", cmd.Update.Messages[0].Text())
	}
}

func TestTestEditsPassesWhenOriginalTestsStillPass(t *testing.T) {
	dir := goRepo(t, nil)
	writeFile(t, filepath.Join(dir, "m_test.go"), strings.Replace(subTestExpects4, "!= 4", "!= 2", 1)+"\nfunc TestMore(t *testing.T) {}\n")
	g := gateFor(dir)
	cmd := runGate(t, g, transcript(editStep, runTestsStep(t, g)))
	if cmd == nil || cmd.Update.Messages[0].Metadata[GateKindKey] != gateKindDiff {
		t.Fatalf("only the diff review should mention the changed test: %+v", cmd)
	}
}

func TestTestEditsCanBeDisabled(t *testing.T) {
	dir := goRepo(t, nil)
	writeFile(t, filepath.Join(dir, "m.go"), strings.Replace(mathGo, "a - b", "a + b", 1))
	writeFile(t, filepath.Join(dir, "m_test.go"), subTestExpects4)
	g := newVerifyGate(NewBackend(dir), []string{FeatureTestEdits})
	cmd := runGate(t, g, transcript(editStep, runTestsStep(t, g)))
	if cmd == nil || cmd.Update.Messages[0].Metadata[GateKindKey] != gateKindDiff {
		t.Fatalf("disabled check should leave the diff review: %+v", cmd)
	}
}

func TestOriginalTestArgs(t *testing.T) {
	last := llm.ToolCall{Name: "run_tests", Args: map[string]any{"language": "node", "dir": "web", "timeout_seconds": 600, "run": "x"}}
	got := originalTestArgs([]string{"web/test/a.js", "pkg/a_test.go", "pkg/b_test.go"}, last)
	if len(got) != 2 {
		t.Fatalf("args = %v", got)
	}
	if p := got[0]["packages"].([]any); len(p) != 1 || p[0] != "./pkg" {
		t.Fatalf("packages = %v", got[0])
	}
	if f := got[1]["files"].([]any); len(f) != 1 || f[0] != "test/a.js" || got[1]["dir"] != "web" || got[1]["run"] != nil {
		t.Fatalf("files call = %v", got[1])
	}
}

func TestDiffReviewFlagsByproductsTestsAndRequirements(t *testing.T) {
	dir := goRepo(t, nil)
	writeFile(t, filepath.Join(dir, "dump.rdb"), "REDIS")
	writeFile(t, filepath.Join(dir, "m_test.go"), "package m\n")
	writeFile(t, filepath.Join(dir, "m.go"), mathGo+"\n// changed\n")
	msgs := transcript(editStep, step{name: "execute", args: map[string]any{"command": "go test ./..."}, result: "ok\n\n[Command succeeded with exit code 0]"})
	msgs[0] = llm.NewUserMessage("Fix it.\n\n## Requirements\n- Sub must subtract.\n  It returns a - b.\n\n- Add stays.\n\n## New Interfaces\nNone")

	cmd := runGate(t, gateFor(dir), msgs)
	if cmd == nil {
		t.Fatal("diff review should block once")
	}
	text := cmd.Update.Messages[0].Text()
	for _, want := range []string{"`dump.rdb`", "existing tests: `m_test.go`", "1. Sub must subtract. It returns a - b.", "2. Add stays."} {
		if !strings.Contains(text, want) {
			t.Errorf("review missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "New Interfaces") || strings.Contains(text, "m.go`") {
		t.Errorf("review has unrelated content:\n%s", text)
	}
	msgs = append(msgs, cmd.Update.Messages[0], llm.NewAssistantMessage("all in order"))
	if again := runGate(t, gateFor(dir), msgs); again != nil {
		t.Fatalf("diff review must run once per turn: %s", again.Update.Messages[0].Text())
	}
}

func TestDiffReviewSkipsCleanTreeWithoutRequirements(t *testing.T) {
	dir := goRepo(t, nil)
	writeFile(t, filepath.Join(dir, "m.go"), mathGo+"\n// changed\n")
	msgs := transcript(editStep, step{name: "execute", args: map[string]any{"command": "go test ./..."}, result: "ok\n\n[Command succeeded with exit code 0]"})
	if cmd := runGate(t, gateFor(dir), msgs); cmd != nil {
		t.Fatalf("nothing to review: %s", cmd.Update.Messages[0].Text())
	}
}

func TestClassifyStatus(t *testing.T) {
	out := " M src/a.js\n?? dump.rdb\n M test/user.js\nA  test/new.js\n?? src/__pycache__/x.cpython-311.pyc\n M pkg/a_test.go\n?? test/fixtures/new.json\n D tests/old_test.py\n"
	byproducts, tests := classifyStatus(out)
	if strings.Join(byproducts, ",") != "dump.rdb,src/__pycache__/x.cpython-311.pyc" {
		t.Fatalf("byproducts = %v", byproducts)
	}
	if strings.Join(tests, ",") != "test/user.js,pkg/a_test.go,tests/old_test.py" {
		t.Fatalf("tests = %v", tests)
	}
}

// TestHeadlessAgentCannotDismissRegression replays the bd80 failure: the
// agent breaks a test, calls the failure unrelated and tries to finish.
func TestHeadlessAgentCannotDismissRegression(t *testing.T) {
	dir := goRepo(t, nil)
	call := func(id, name string, args map[string]any) llm.Message {
		return llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: id, Name: name, Args: args}}}
	}
	model := llm.NewFakeModel(
		call("r", "read_file", map[string]any{"file_path": "/m.go"}),
		call("e", "edit_file", map[string]any{"file_path": "/m.go", "old_string": "a - b", "new_string": "a + b"}),
		call("t1", "execute", map[string]any{"command": "go test ./..."}),
		llm.NewAssistantMessage("TestSub fails, but that failure is unrelated to my change. Done."),
		call("f", "edit_file", map[string]any{"file_path": "/m.go", "old_string": "Sub(a, b int) int { return a + b }", "new_string": "Sub(a, b int) int { return a - b }"}),
		call("t2", "execute", map[string]any{"command": "go test ./..."}),
		llm.NewAssistantMessage("Fixed the regression; tests pass."),
	)
	a, err := CreateCLIAgent(context.Background(), Options{Model: model, Cwd: dir, Backend: NewBackend(dir), AutoApprove: true, NoWebTools: true})
	if err != nil {
		t.Fatal(err)
	}
	st, err := a.Invoke(context.Background(), core.Input{Messages: []llm.Message{llm.NewUserMessage("rename nothing")}}, rt.RunConfig{})
	if err != nil {
		reqs := model.Requests()
		for _, m := range reqs[len(reqs)-1].Messages[1:] {
			t.Logf("%s: %s %v", m.Role, clipLine(m.Text(), 300), m.ToolCalls)
		}
		t.Fatal(err)
	}
	var sawBaseline bool
	for _, m := range st.Messages {
		sawBaseline = sawBaseline || strings.Contains(m.Text(), "passes on the unchanged HEAD")
	}
	if !sawBaseline {
		t.Fatal("the gate did not show the HEAD comparison")
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "m.go")); string(got) != mathGo {
		t.Fatalf("m.go = %q", got)
	}
}
