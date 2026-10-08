package agent

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	core "github.com/tsengmingchun/quill/agent"
	"github.com/tsengmingchun/quill/code/codetools"
	"github.com/tsengmingchun/quill/llm"
	rt "github.com/tsengmingchun/quill/runtime"
	"github.com/tsengmingchun/quill/tool"
)

func reportJSON(t *testing.T, rep codetools.TestReport) string {
	t.Helper()
	if rep.Failures == nil {
		rep.Failures = []codetools.TestFailure{}
	}
	if rep.BuildErrors == nil {
		rep.BuildErrors = []codetools.Diagnostic{}
	}
	raw, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func diagJSON(t *testing.T, diags ...codetools.Diagnostic) string {
	t.Helper()
	if diags == nil {
		diags = []codetools.Diagnostic{}
	}
	raw, err := json.Marshal(diags)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func runTestsCall() llm.ToolCall {
	return llm.ToolCall{ID: "c", Name: codetools.RunTestsName, Args: map[string]any{"packages": []any{"./internal/user"}}}
}

func shellCheck(cmd string) llm.ToolCall {
	return llm.ToolCall{ID: "c", Name: executeToolName, Args: map[string]any{"command": cmd}}
}

func TestClassifyFailure(t *testing.T) {
	assertion := codetools.TestReport{Status: "failed", Framework: "go", Passed: 3, Failed: 1, Failures: []codetools.TestFailure{
		{Package: "m/internal/user", Test: "TestGetDeletedUser", File: "service_test.go", Line: 42, Message: "expected 404, got 500"},
	}}
	panicked := codetools.TestReport{Status: "failed", Framework: "go", Failed: 1, Failures: []codetools.TestFailure{
		{Package: "m/internal/user", Test: "TestGetUser", Message: "panic: runtime error: invalid memory address or nil pointer dereference"},
	}}
	build := codetools.TestReport{Status: "failed", Framework: "go", BuildErrors: []codetools.Diagnostic{
		{File: "internal/user/service.go", Line: 40, Severity: "error", Source: "build", Message: "undefined: ErrUserNotFound"},
	}}
	syntax := codetools.TestReport{Status: "failed", Framework: "go", BuildErrors: []codetools.Diagnostic{
		{File: "internal/user/service.go", Line: 52, Severity: "error", Source: "build", Message: "syntax error: unexpected }, expected expression"},
	}}
	tests := []struct {
		name     string
		call     llm.ToolCall
		text     string
		category FailureCategory
		tests    []string
		env      string
	}{
		{"run_tests passed", runTestsCall(), reportJSON(t, codetools.TestReport{Status: "passed", Framework: "go", Passed: 4}), FailurePassed, nil, ""},
		{"run_tests assertion", runTestsCall(), reportJSON(t, assertion), FailureAssertion, []string{"TestGetDeletedUser"}, ""},
		{"run_tests panic", runTestsCall(), reportJSON(t, panicked), FailurePanic, []string{"TestGetUser"}, ""},
		{"run_tests compile", runTestsCall(), reportJSON(t, build), FailureCompile, nil, ""},
		{"run_tests syntax", runTestsCall(), reportJSON(t, syntax), FailureSyntax, nil, ""},
		{"run_tests no tests", runTestsCall(), reportJSON(t, codetools.TestReport{Status: "no_tests", Framework: "mocha"}), FailureNoTests, nil, ""},
		{"run_tests refused service", runTestsCall(), reportJSON(t, codetools.TestReport{Status: "error", Framework: "mocha", Output: "Error: connect ECONNREFUSED 127.0.0.1:6379"}), FailureEnvironment, nil, "service:6379"},
		{"run_tests missing package", runTestsCall(), reportJSON(t, codetools.TestReport{Status: "error", Framework: "mocha", Output: "Error: Cannot find module 'lodash'\nRequire stack:"}), FailureEnvironment, nil, "module_not_found:lodash"},
		{"run_tests missing python package", runTestsCall(), reportJSON(t, codetools.TestReport{Status: "error", Framework: "pytest", Output: "ModuleNotFoundError: No module named 'requests'"}), FailureEnvironment, nil, "module_not_found:requests"},
		{"run_tests wrong relative import is code", runTestsCall(), reportJSON(t, codetools.TestReport{Status: "error", Framework: "mocha", Output: "Error: Cannot find module './util'"}), FailureCompile, nil, ""},
		{"run_tests runner error", runTestsCall(), reportJSON(t, codetools.TestReport{Status: "error", Framework: "jest", Output: "the runner exited with code 1 although no test failed"}), FailureToolError, nil, ""},
		{"run_tests tool error", runTestsCall(), "Error: go test did not finish: signal: killed", FailureToolError, nil, ""},
		{"run_tests timeout", runTestsCall(), "Error: go test did not finish: Error: Command timed out after 2m0s (custom timeout).", FailureTimeout, nil, ""},
		{"run_tests with appended hint", runTestsCall(), reportJSON(t, assertion) + "\n\n[hint] Connection refused on port 6379 (Redis)", FailureAssertion, []string{"TestGetDeletedUser"}, ""},
		{"diagnostics clean", llm.ToolCall{Name: codetools.DiagnosticsName}, diagJSON(t), FailurePassed, nil, ""},
		{"diagnostics compile", llm.ToolCall{Name: codetools.DiagnosticsName}, diagJSON(t, codetools.Diagnostic{File: "a.go", Line: 3, Severity: "error", Source: "build", Message: "undefined: x"}), FailureCompile, nil, ""},
		{"diagnostics python syntax", llm.ToolCall{Name: codetools.DiagnosticsName}, diagJSON(t, codetools.Diagnostic{File: "a.py", Line: 3, Severity: "error", Source: "python", Message: "invalid syntax"}), FailureSyntax, nil, ""},
		{"diagnostics lint only", llm.ToolCall{Name: codetools.DiagnosticsName}, diagJSON(t, codetools.Diagnostic{File: "a.go", Line: 3, Severity: "warning", Source: "vet", Message: "printf: wrong verb"}), FailureLint, nil, ""},
		{"execute go test failure", shellCheck("go test ./..."), "--- FAIL: TestA (0.12s)\n    a_test.go:9: got 1\nFAIL\nFAIL\tm\t0.12s\n\n[Command failed with exit code 1]", FailureAssertion, []string{"TestA"}, ""},
		{"execute pytest failure", shellCheck("python -m pytest tests"), "FAILED tests/test_a.py::test_parse - assert 1 == 2\n1 failed, 3 passed\n\n[Command failed with exit code 1]", FailureAssertion, []string{"test_parse"}, ""},
		{"execute mocha failure", shellCheck("npx mocha test/user.js"), "  10 passing\n  1 failing\n\n  1) User\n       returns 404:\n     AssertionError: expected 500\n\n[Command failed with exit code 1]", FailureAssertion, []string{"User returns 404"}, ""},
		{"execute missing runner", shellCheck("npm test"), "sh: 1: jest: not found\n\n[Command failed with exit code 127]", FailureEnvironment, nil, "missing_tool:jest"},
		{"execute network", shellCheck("go test ./..."), "go: downloading x\ndial tcp: lookup proxy.golang.org: no such host\n\n[Command failed with exit code 1]", FailureEnvironment, nil, "network"},
		{"execute timeout", shellCheck("go test ./..."), "Error: Command timed out after 2m0s. For long-running commands, re-run using the timeout parameter.\n\n[Command failed with exit code 124]", FailureTimeout, nil, ""},
		{"execute build syntax", shellCheck("go build ./..."), "./a.go:3:1: syntax error: unexpected }\n\n[Command failed with exit code 1]", FailureSyntax, nil, ""},
		{"execute sandbox", shellCheck("go test ./..."), "open /root/x: operation not permitted\n\n[Command failed with exit code 1]\n[sandbox] This failure may come from the sandbox (mode workspace-write): ...", FailureSandbox, nil, ""},
		{"run_tests build blocked by the network", runTestsCall(), reportJSON(t, goBuildReport(proxyUnreachable)), FailureEnvironment, nil, "network"},
		{"run_tests missing go.sum entry", runTestsCall(), reportJSON(t, goBuildReport(missingGoSum)), FailureEnvironment, nil, "dependency:github.com/vulsio/goval-dictionary/log"},
		{"run_tests network and code errors", runTestsCall(), reportJSON(t, goBuildReport(proxyUnreachable, "undefined: ErrUserNotFound")), FailureCompile, nil, ""},
		{"run_tests own package not created yet", runTestsCall(), reportJSON(t, goBuildReport("no required module provides package go.flipt.io/flipt/internal/oci/ecr; to add it:\n\tgo get go.flipt.io/flipt/internal/oci/ecr")), FailureCompile, nil, ""},
		{"diagnostics blocked by the network", llm.ToolCall{Name: codetools.DiagnosticsName}, diagJSON(t, codetools.Diagnostic{File: "a.go", Line: 9, Severity: "error", Source: "build", Message: proxyUnreachable}), FailureEnvironment, nil, "network"},
		{"execute unreachable network", shellCheck("go test ./..."), "go: github.com/x/y@v1.0.0: Get \"https://proxy.golang.org/x\": dial tcp 142.250.0.1:443: connect: network is unreachable\n\n[Command failed with exit code 1]", FailureEnvironment, nil, "network"},
		{"execute passed", shellCheck("go test ./..."), "ok\tm\t0.1s\n\n[Command succeeded with exit code 0]", FailurePassed, nil, ""},
		{"execute no tests", shellCheck("npx mocha -g nothing"), "  0 passing (2ms)\n\n[Command succeeded with exit code 0]", FailureNoTests, nil, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sig, ok := ClassifyFailure(tc.call, tc.text)
			if !ok {
				t.Fatalf("not classified")
			}
			if sig.Category != tc.category {
				t.Fatalf("category = %q, want %q (sig %+v)", sig.Category, tc.category, sig)
			}
			if tc.tests != nil && !slices.Equal(sig.Tests, tc.tests) {
				t.Fatalf("tests = %q, want %q", sig.Tests, tc.tests)
			}
			if sig.Env != tc.env {
				t.Fatalf("env = %q, want %q", sig.Env, tc.env)
			}
		})
	}
}

// Messages from the RL8 traces: a module download in a container without
// network, and a module missing from go.sum.
const (
	proxyUnreachable = `github.com/aws/aws-sdk-go-v2/service/ecr@v1.53.0: Get "https://proxy.golang.org/github.com/aws/aws-sdk-go-v2/service/ecr/@v/v1.53.0.zip": dial tcp: lookup proxy.golang.org on 127.0.0.11:53: dial udp 127.0.0.11:53: connect: network is unreachable`
	missingGoSum     = "missing go.sum entry for module providing package github.com/vulsio/goval-dictionary/log (imported by github.com/future-architect/vuls/oval); to add:\n\tgo get github.com/future-architect/vuls/oval"
)

// goBuildReport is a go test report that failed to build with messages.
func goBuildReport(messages ...string) codetools.TestReport {
	rep := codetools.TestReport{Status: "failed", Framework: "go"}
	for i, m := range messages {
		rep.BuildErrors = append(rep.BuildErrors, codetools.Diagnostic{File: "internal/oci/file.go", Line: 10 + i, Severity: "error", Source: "build", Message: m})
	}
	return rep
}

func TestClassifyFailureKeepsOnlyCodeErrorsBesideEnvironmentOnes(t *testing.T) {
	s, _ := ClassifyFailure(runTestsCall(), reportJSON(t, goBuildReport(proxyUnreachable, "undefined: ErrUserNotFound")))
	if len(s.Details) != 1 || !strings.Contains(s.Details[0], "undefined: ErrUserNotFound") {
		t.Fatalf("details = %q", s.Details)
	}
}

func TestFailureAdviceDependsOnTheEnvironmentProblem(t *testing.T) {
	for _, tc := range []struct {
		env  string
		want []string
	}{
		{"network", []string{"network", "Editing the code will not fix", "unverified"}},
		{"dependency:github.com/vulsio/goval-dictionary/log", []string{"go mod tidy", "go.sum"}},
		{"service:6379", []string{"start the service"}},
		{"missing_tool:jest", []string{"install"}},
	} {
		advice := failureAdvice(Signature{Category: FailureEnvironment, Env: tc.env})
		for _, w := range tc.want {
			if !strings.Contains(advice, w) {
				t.Errorf("%s: advice lacks %q: %s", tc.env, w, advice)
			}
		}
	}
}

func TestClassifyFailureIgnoresNonChecks(t *testing.T) {
	for _, c := range []struct {
		call llm.ToolCall
		text string
	}{
		{shellCheck("npm install"), "added 3 packages\n\n[Command succeeded with exit code 0]"},
		{llm.ToolCall{Name: "read_file"}, "package a"},
		{runTestsCall(), `{"status":"unsupported","reason":"no go.mod"}`},
	} {
		if sig, ok := ClassifyFailure(c.call, c.text); ok {
			t.Errorf("%s classified as %+v", c.call.Name, sig)
		}
	}
}

func TestFailureKeyIgnoresVolatileOutput(t *testing.T) {
	a, _ := ClassifyFailure(shellCheck("go test ./..."), "--- FAIL: TestA (0.12s)\n    a_test.go:9: got 1 at 0xc000123\nFAIL\n\n[Command failed with exit code 1]")
	b, _ := ClassifyFailure(shellCheck("go test ./..."), "--- FAIL: TestA (3.40s)\n    a_test.go:9: got 1 at 0xc000999\nFAIL\n\n[Command failed with exit code 1]")
	if a.Key() != b.Key() {
		t.Fatalf("keys differ:\n%s\n%s", a.Key(), b.Key())
	}
}

func TestFailureKeyIsIndependentOfCommand(t *testing.T) {
	viaTool, _ := ClassifyFailure(runTestsCall(), reportJSON(t, codetools.TestReport{Status: "failed", Framework: "go", Failed: 1, Failures: []codetools.TestFailure{{Package: "m", Test: "TestA", Message: "got 1"}}}))
	viaShell, _ := ClassifyFailure(shellCheck("go test -run TestA ./..."), "--- FAIL: TestA (0.10s)\n    a_test.go:9: got 1\nFAIL\n\n[Command failed with exit code 1]")
	if !slices.Equal(viaTool.Tests, viaShell.Tests) {
		t.Fatalf("tests differ: %q vs %q", viaTool.Tests, viaShell.Tests)
	}
}

func sig(cat FailureCategory, tests ...string) Signature {
	s := Signature{Category: cat}
	for _, t := range tests {
		name, msg, _ := strings.Cut(t, "=")
		s.addTest(name, msg)
	}
	return s
}

func TestCompareFailures(t *testing.T) {
	tests := []struct {
		name          string
		before, after Signature
		kind          DeltaKind
		fixed, added  []string
	}{
		{"fixed", sig(FailureAssertion, "A=x"), sig(FailurePassed), DeltaFixed, []string{"A"}, nil},
		{"fewer failures", sig(FailureAssertion, "A=x", "B=y"), sig(FailureAssertion, "B=y"), DeltaProgressed, []string{"A"}, nil},
		{"more failures", sig(FailureAssertion, "A=x"), sig(FailureAssertion, "A=x", "B=y"), DeltaRegressed, nil, []string{"B"}},
		{"same failures", sig(FailureAssertion, "A=x"), sig(FailureAssertion, "A=x"), DeltaUnchanged, nil, nil},
		{"same test, new message", sig(FailureAssertion, "A=500"), sig(FailureAssertion, "A=404 wrong body"), DeltaChanged, nil, nil},
		{"some fixed, some new", sig(FailureAssertion, "A=x"), sig(FailureAssertion, "B=y"), DeltaChanged, []string{"A"}, []string{"B"}},
		{"build fixed, tests fail", Signature{Category: FailureCompile, Details: []string{"a.go:1: undefined: x"}}, sig(FailureAssertion, "A=x"), DeltaProgressed, nil, nil},
		{"tests fail, build broke", sig(FailureAssertion, "A=x"), Signature{Category: FailureSyntax, Details: []string{"a.go:1: syntax error"}}, DeltaRegressed, nil, nil},
		{"passing then failing", sig(FailurePassed), sig(FailureAssertion, "A=x"), DeltaRegressed, nil, []string{"A"}},
		{"environment is not a code signal", sig(FailureAssertion, "A=x"), Signature{Category: FailureEnvironment, Env: "service:6379"}, DeltaChanged, nil, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := CompareFailures(tc.before, tc.after)
			if d.Kind != tc.kind {
				t.Fatalf("kind = %q, want %q (%+v)", d.Kind, tc.kind, d)
			}
			if !slices.Equal(d.Fixed, tc.fixed) || !slices.Equal(d.New, tc.added) {
				t.Fatalf("fixed %q new %q, want %q %q", d.Fixed, d.New, tc.fixed, tc.added)
			}
		})
	}
}

func TestClassifyCheckIgnoresAppendedNotes(t *testing.T) {
	text := reportJSON(t, codetools.TestReport{Status: "passed", Framework: "go", Passed: 2}) + "\n\n[repair] {\"category\":\"passed\"}\n\n[progress] note"
	r := classifyCheck(runTestsCall(), text)
	if r == nil || !r.passed {
		t.Fatalf("classifyCheck = %+v", r)
	}
	r = classifyCheck(shellCheck("go test ./..."), "ok\n\n[Command succeeded with exit code 0]\n\n[hint] Connection refused on port 6379")
	if r == nil || !r.passed {
		t.Fatalf("execute classifyCheck = %+v", r)
	}
}

// repairBlock extracts the JSON after the [repair] marker.
func repairBlock(t *testing.T, text string) map[string]any {
	t.Helper()
	_, rest, ok := strings.Cut(text, repairMarker+" ")
	if !ok {
		t.Fatalf("no %s block in %q", repairMarker, text)
	}
	rest, _, _ = strings.Cut(rest, "\n")
	var out map[string]any
	if err := json.Unmarshal([]byte(rest), &out); err != nil {
		t.Fatalf("block %q: %v", rest, err)
	}
	return out
}

func classify(t *testing.T, history []llm.Message, call llm.ToolCall, text string) string {
	t.Helper()
	res, err := FailureClassifier{}.WrapToolCall(context.Background(), &core.ToolCallRequest{ToolCall: call, State: &rt.State{Messages: history}},
		func(context.Context, *core.ToolCallRequest) (*tool.Result, error) {
			return &tool.Result{Content: []llm.ContentBlock{llm.TextBlock(text)}}, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	return resultText(res)
}

func TestFailureClassifierAnnotatesFailures(t *testing.T) {
	text := classify(t, nil, shellCheck("npm test"), "sh: 1: jest: not found\n\n[Command failed with exit code 127]")
	block := repairBlock(t, text)
	if block["category"] != "environment" || block["env"] != "missing_tool:jest" || !strings.Contains(block["advice"].(string), "not the application code") {
		t.Fatalf("block = %v", block)
	}
}

func TestFailureClassifierReportsDeltaAfterEdit(t *testing.T) {
	first := step{name: "execute", args: map[string]any{"command": "go test ./..."}, result: "--- FAIL: TestA (0.1s)\n    a_test.go:9: got 500\n--- FAIL: TestB (0.1s)\n    b_test.go:3: boom\nFAIL\n\n[Command failed with exit code 1]"}
	history := transcript(first, step{name: "edit_file", args: map[string]any{"file_path": "/a.go"}, result: "Successfully replaced 1 instance(s)"})
	text := classify(t, history, shellCheck("go test ./..."), "--- FAIL: TestB (0.1s)\n    b_test.go:3: boom\nFAIL\n\n[Command failed with exit code 1]")
	block := repairBlock(t, text)
	delta, _ := block["delta"].(map[string]any)
	if delta["kind"] != "progressed" {
		t.Fatalf("delta = %v", block)
	}
}

func TestFailureClassifierComparesAcrossCommands(t *testing.T) {
	first := step{name: codetools.RunTestsName, args: map[string]any{"files": []any{"test/user.js"}}, result: reportJSON(t, codetools.TestReport{Status: "failed", Framework: "mocha", Failed: 1, Failures: []codetools.TestFailure{{File: "test/user.js", Test: "User returns 404", Message: "AssertionError: expected 500"}}})}
	history := transcript(first, step{name: "edit_file", args: map[string]any{"file_path": "/src/user.js"}, result: "Successfully replaced 1 instance(s)"})
	text := classify(t, history, shellCheck("npx mocha test/user.js --grep 404"), "  0 passing\n  1 failing\n\n  1) User\n       returns 404:\n     AssertionError: expected 500\n\n[Command failed with exit code 1]")
	delta, _ := repairBlock(t, text)["delta"].(map[string]any)
	if delta["kind"] != "unchanged" {
		t.Fatalf("delta = %v", delta)
	}
}

func TestFailureClassifierSkipsPassingRerunWithoutEdit(t *testing.T) {
	text := classify(t, nil, shellCheck("go test ./..."), "ok\tm\t0.1s\n\n[Command succeeded with exit code 0]")
	if strings.Contains(text, repairMarker) {
		t.Fatalf("passing check annotated: %q", text)
	}
}

func TestAgentAnnotatesCheckResults(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module m\n\ngo 1.21\n")
	writeFile(t, filepath.Join(dir, "a.go"), "package m\n\nfunc Two() int { return 3 }\n")
	writeFile(t, filepath.Join(dir, "a_test.go"), "package m\n\nimport \"testing\"\n\nfunc TestTwo(t *testing.T) {\n\tif Two() != 2 {\n\t\tt.Fatal(\"want 2\")\n\t}\n}\n")
	call := func(id, name string, args map[string]any) llm.Message {
		return llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: id, Name: name, Args: args}}}
	}
	test := map[string]any{"command": "go test ./..."}
	model := llm.NewFakeModel(
		call("t1", "execute", test),
		call("r1", "read_file", map[string]any{"file_path": "/a.go"}),
		call("e1", "edit_file", map[string]any{"file_path": "/a.go", "old_string": "return 3", "new_string": "return 2"}),
		call("t2", "execute", test),
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
	results := map[string]string{}
	for _, m := range st.Messages {
		if m.Role == llm.RoleTool {
			results[m.ToolCallID] = m.Text()
		}
	}
	if b := repairBlock(t, results["t1"]); b["category"] != "assertion" {
		t.Fatalf("first run block = %v\n%s", b, results["t1"])
	}
	if d, _ := repairBlock(t, results["t2"])["delta"].(map[string]any); d["kind"] != "fixed" {
		t.Fatalf("second run = %s", results["t2"])
	}
}

func TestFailureClassifierReportsFixed(t *testing.T) {
	first := step{name: "execute", args: map[string]any{"command": "go test ./..."}, result: "--- FAIL: TestA (0.1s)\nFAIL\n\n[Command failed with exit code 1]"}
	history := transcript(first, step{name: "edit_file", args: map[string]any{"file_path": "/a.go"}, result: "Successfully replaced 1 instance(s)"})
	text := classify(t, history, shellCheck("go test ./..."), "ok\tm\t0.1s\n\n[Command succeeded with exit code 0]")
	block := repairBlock(t, text)
	if delta, _ := block["delta"].(map[string]any); delta["kind"] != "fixed" {
		t.Fatalf("block = %v", block)
	}
}
