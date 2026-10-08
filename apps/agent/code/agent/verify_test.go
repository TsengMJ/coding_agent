package agent

import (
	"context"
	"strings"
	"testing"

	core "github.com/tsengmingchun/quill/apps/agent/agent"
	"github.com/tsengmingchun/quill/apps/agent/llm"
	rt "github.com/tsengmingchun/quill/apps/agent/runtime"
	"github.com/tsengmingchun/quill/apps/agent/tool"
)

// transcript builds messages from (call, result) pairs after a user prompt.
type step struct {
	name   string
	args   map[string]any
	result string
	failed bool
}

func transcript(steps ...step) []llm.Message {
	msgs := []llm.Message{llm.NewUserMessage("fix the bug")}
	for i, s := range steps {
		id := string(rune('a' + i))
		msgs = append(msgs, llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: id, Name: s.name, Args: s.args}}})
		m := llm.NewToolMessage(id, s.name, s.result)
		if s.failed {
			m.Status = llm.ToolStatusError
		}
		msgs = append(msgs, m)
	}
	return append(msgs, llm.NewAssistantMessage("done"))
}

var (
	editStep   = step{name: "edit_file", args: map[string]any{"file_path": "/src/a.js"}, result: "Successfully replaced 1 instance(s)"}
	passedExec = step{name: "execute", args: map[string]any{"command": "npx mocha test/user.js"}, result: "12 passing\n\n[Command succeeded with exit code 0]"}
	failedExec = step{name: "execute", args: map[string]any{"command": "npm test"}, result: "Error: connect ECONNREFUSED 127.0.0.1:6379\n\nExit code: 1\n[Command failed with exit code 1]"}
)

func TestVerifyGateAllowsTurnWithoutEdits(t *testing.T) {
	msgs := transcript(step{name: "read_file", args: map[string]any{"file_path": "/a"}, result: "x"})
	if msg, ok := verifyMessage(msgs); ok {
		t.Fatalf("blocked: %s", msg)
	}
}

func TestVerifyGateBlocksUnverifiedEdit(t *testing.T) {
	msg, ok := verifyMessage(transcript(editStep, step{name: "execute", args: map[string]any{"command": "npx eslint src"}, result: "\n[Command succeeded with exit code 0]"}))
	if !ok || !strings.HasPrefix(msg, verifyMarker) || !strings.Contains(msg, "/src/a.js") || !strings.Contains(msg, "have not run the tests") {
		t.Fatalf("ok=%v msg=%q", ok, msg)
	}
}

func TestVerifyGateAllowsPassingCheckAfterEdit(t *testing.T) {
	if msg, ok := verifyMessage(transcript(editStep, passedExec)); ok {
		t.Fatalf("blocked: %s", msg)
	}
}

func TestVerifyGateIgnoresCheckBeforeLastEdit(t *testing.T) {
	if _, ok := verifyMessage(transcript(passedExec, editStep)); !ok {
		t.Fatal("check before the edit counted as verification")
	}
}

func TestVerifyGateIgnoresFailedEdit(t *testing.T) {
	failedEdit := editStep
	failedEdit.result, failedEdit.failed = "Error: String not found in file", true
	if msg, ok := verifyMessage(transcript(failedEdit)); ok {
		t.Fatalf("blocked on a failed edit: %s", msg)
	}
}

func TestVerifyGateBlocksFailingRunTests(t *testing.T) {
	report := `{"status":"failed","passed":3,"failed":1,"skipped":0,"failures":[{"package":"test/user.js","test":"should remove uploaded picture","file":"test/user.js","line":1248,"message":"Expected actual to be strictly unequal to: false\nmore"}],"build_errors":[]}`
	msg, ok := verifyMessage(transcript(editStep, step{name: "run_tests", result: report}))
	if !ok || !strings.Contains(msg, "should remove uploaded picture (test/user.js:1248): Expected actual") || strings.Contains(msg, "more") {
		t.Fatalf("ok=%v msg=%q", ok, msg)
	}
}

func TestVerifyGateTreatsEmptyRunAsUnverified(t *testing.T) {
	empty := step{name: "execute", args: map[string]any{"command": "npx mocha -g nothing"}, result: "  0 passing (2ms)\n\n[Command succeeded with exit code 0]"}
	msg, ok := verifyMessage(transcript(editStep, empty))
	if !ok || !strings.Contains(msg, "ran no tests") {
		t.Fatalf("ok=%v msg=%q", ok, msg)
	}
}

func TestVerifyGateRunTestsNoTests(t *testing.T) {
	noTests := step{name: "run_tests", result: `{"status":"no_tests","framework":"pytest","passed":0,"failed":0}`}
	if msg, ok := verifyMessage(transcript(editStep, noTests)); !ok || !strings.Contains(msg, "ran no tests") {
		t.Fatalf("ok=%v msg=%q", ok, msg)
	}
	npm := step{name: "run_tests", result: `{"status":"passed","framework":"npm","passed":0,"failed":0,"output":"ok"}`}
	if msg, ok := verifyMessage(transcript(editStep, npm)); ok {
		t.Fatalf("npm test fallback blocked: %s", msg)
	}
}

func TestVerifyGateBlocksFilteredRunTests(t *testing.T) {
	filtered := step{name: "run_tests",
		args:   map[string]any{"files": []any{"test/controllers.js"}, "run": "blocking access for unconfirmed emails"},
		result: `{"status":"passed","framework":"mocha","passed":2,"failed":0,"skipped":0,"failures":[],"build_errors":[]}`}
	msg, ok := verifyMessage(transcript(editStep, filtered))
	if !ok || !strings.Contains(msg, "blocking access for unconfirmed emails") || !strings.Contains(msg, "test/controllers.js") || !strings.Contains(msg, "without a filter") {
		t.Fatalf("ok=%v msg=%q", ok, msg)
	}
}

func TestVerifyGateBlocksFilteredExecute(t *testing.T) {
	for _, cmd := range []string{
		`npm test -- --grep "blocking access for unconfirmed emails"`,
		`npx mocha test/user.js -g 'export'`,
		`npx jest -t=renders src/a.test.js`,
		`go test ./pkg/... -count=1 -run TestParse`,
		`pytest tests/test_a.py -k parse`,
		`python -m pytest tests/test_a.py::test_parse`,
	} {
		filtered := step{name: "execute", args: map[string]any{"command": cmd}, result: "2 passing\n\n[Command succeeded with exit code 0]"}
		if msg, ok := verifyMessage(transcript(editStep, filtered)); !ok || !strings.Contains(msg, "without a filter") {
			t.Errorf("%s: ok=%v msg=%q", cmd, ok, msg)
		}
	}
}

func TestVerifyGateFilterDetectionIgnoresSimilarFlags(t *testing.T) {
	for _, cmd := range []string{"go test -timeout 60s ./...", "go test -tags integration ./...", "npx mocha test/user.js"} {
		run := step{name: "execute", args: map[string]any{"command": cmd}, result: "ok\n\n[Command succeeded with exit code 0]"}
		if msg, ok := verifyMessage(transcript(editStep, run)); ok {
			t.Errorf("%s blocked: %s", cmd, msg)
		}
	}
}

func TestVerifyGateAllowsFullRunAfterFiltered(t *testing.T) {
	filtered := step{name: "run_tests", args: map[string]any{"files": []any{"test/controllers.js"}, "run": "unconfirmed"},
		result: `{"status":"passed","framework":"mocha","passed":2,"failed":0}`}
	full := step{name: "run_tests", args: map[string]any{"files": []any{"test/controllers.js"}, "run": ""},
		result: `{"status":"passed","framework":"mocha","passed":180,"failed":0}`}
	if msg, ok := verifyMessage(transcript(editStep, filtered, full)); ok {
		t.Fatalf("blocked: %s", msg)
	}
}

func TestVerifyGateSuggestsStartingService(t *testing.T) {
	msg, ok := verifyMessage(transcript(editStep, failedExec))
	if !ok || !strings.Contains(msg, "exited with code 1") || !strings.Contains(msg, "redis-server --daemonize yes") {
		t.Fatalf("ok=%v msg=%q", ok, msg)
	}
}

func TestVerifyGateStopsAfterCap(t *testing.T) {
	msgs := transcript(editStep)
	first, ok := verifyMessage(msgs)
	if !ok || strings.Contains(first, "last reminder") {
		t.Fatalf("first block: ok=%v msg=%q", ok, first)
	}
	msgs = append(msgs, llm.NewUserMessage(first), llm.NewAssistantMessage("still done"))
	second, ok := verifyMessage(msgs)
	if !ok || !strings.Contains(second, "last reminder") {
		t.Fatalf("second block: ok=%v msg=%q", ok, second)
	}
	msgs = append(msgs, llm.NewUserMessage(second), llm.NewAssistantMessage("unverified"))
	if msg, ok := verifyMessage(msgs); ok {
		t.Fatalf("third block: %s", msg)
	}
}

func TestVerifyGateNamesTheEnvironmentProblem(t *testing.T) {
	msgs := transcript(editStep, step{name: "run_tests", result: reportJSON(t, goBuildReport(proxyUnreachable))})
	first, ok := verifyMessage(msgs)
	if !ok || !strings.Contains(first, "could not run because of the environment (network)") || !strings.Contains(first, "Editing the code will not fix") {
		t.Fatalf("ok=%v msg=%q", ok, first)
	}
	if strings.Contains(first, "Fix the cause") {
		t.Fatalf("asks to fix the code for an environment problem: %q", first)
	}
	msgs = append(msgs, llm.NewUserMessage(first), llm.NewAssistantMessage("done"))
	second, ok := verifyMessage(msgs)
	if !ok || !strings.Contains(second, "last reminder") {
		t.Fatalf("second block: ok=%v msg=%q", ok, second)
	}
	msgs = append(msgs, llm.NewUserMessage(second), llm.NewAssistantMessage("unverified"))
	if msg, ok := verifyMessage(msgs); ok {
		t.Fatalf("third block: %s", msg)
	}
}

func TestVerifyGateAsksToFixACodeFailure(t *testing.T) {
	msg, ok := verifyMessage(transcript(editStep, step{name: "run_tests", result: reportJSON(t, goBuildReport("undefined: ErrUserNotFound"))}))
	if !ok || !strings.Contains(msg, "did not pass") || !strings.Contains(msg, "Fix the cause") {
		t.Fatalf("ok=%v msg=%q", ok, msg)
	}
}

func TestVerifyGateNewUserTurnResetsCap(t *testing.T) {
	msgs := transcript(editStep)
	for range maxVerifyBlocks {
		msgs = append(msgs, llm.NewUserMessage(verifyMarker+" x"), llm.NewAssistantMessage("done"))
	}
	msgs = append(msgs, transcript(editStep)...)
	if _, ok := verifyMessage(msgs); !ok {
		t.Fatal("a new user turn inherited the previous turn's blocks")
	}
}

func TestServiceHint(t *testing.T) {
	cases := map[string]string{
		"connect ECONNREFUSED 127.0.0.1:27017":                        "MongoDB",
		"Error 111 connecting to localhost:6379. Connection refused.": "Redis",
		"connect ECONNREFUSED 127.0.0.1:9200":                         "port 9200: a service",
		"ECONNREFUSED":                                                "probably not running",
	}
	for out, want := range cases {
		if got := serviceHint(out); !strings.Contains(got, want) {
			t.Errorf("%q: hint %q, want %q", out, got, want)
		}
	}
	if got := serviceHint("all good on 127.0.0.1:6379"); got != "" {
		t.Errorf("hint without refusal: %q", got)
	}
}

func TestVerifyGateWrapToolCallAddsHint(t *testing.T) {
	next := func(context.Context, *core.ToolCallRequest) (*tool.Result, error) {
		return &tool.Result{Content: []llm.ContentBlock{llm.TextBlock("connect ECONNREFUSED 127.0.0.1:6379")}}, nil
	}
	for name, want := range map[string]bool{"execute": true, "read_file": false} {
		res, err := VerifyGate{}.WrapToolCall(context.Background(), &core.ToolCallRequest{ToolCall: llm.ToolCall{Name: name}}, next)
		if err != nil {
			t.Fatal(err)
		}
		if got := len(res.Content) == 2 && strings.Contains(res.Content[1].Text, "redis-server"); got != want {
			t.Errorf("%s: hint added = %v, want %v", name, got, want)
		}
	}
}

// The gate sends the loop back to the model, which then runs the tests.
func TestVerifyGateKeepsAgentRunning(t *testing.T) {
	fake := func(name, out string) tool.Tool {
		return &tool.Func{ToolName: name, ToolSchema: tool.Schema{"type": "object"},
			Fn: func(context.Context, *tool.Runtime, map[string]any) (*tool.Result, error) {
				return &tool.Result{Content: []llm.ContentBlock{llm.TextBlock(out)}}, nil
			}}
	}
	call := func(id, name string, args map[string]any) llm.Message {
		return llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: id, Name: name, Args: args}}}
	}
	model := llm.NewFakeModel(
		call("1", "edit_file", map[string]any{"file_path": "/a.go"}),
		llm.NewAssistantMessage("done"),
		call("2", "execute", map[string]any{"command": "go test ./..."}),
		llm.NewAssistantMessage("done, tests pass"),
	)
	a, err := core.CreateAgent(core.Config{
		Model: model,
		Tools: []tool.Tool{
			fake("edit_file", "Successfully replaced 1 instance(s)"),
			fake("execute", "ok\n\n[Command succeeded with exit code 0]"),
		},
		Middleware: []core.Middleware{VerifyGate{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	state, err := a.Invoke(context.Background(), core.Input{Messages: []llm.Message{llm.NewUserMessage("fix")}}, rt.RunConfig{})
	if err != nil {
		t.Fatal(err)
	}
	var gate int
	for _, m := range state.Messages {
		if m.Role == llm.RoleUser && strings.HasPrefix(m.Text(), verifyMarker) {
			gate++
		}
	}
	if last := state.Messages[len(state.Messages)-1].Text(); gate != 1 || last != "done, tests pass" {
		t.Fatalf("gate messages = %d, last = %q", gate, last)
	}
}
