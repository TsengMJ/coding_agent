package codetools

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func needCmd(t *testing.T, name string) {
	t.Helper()
	if _, err := exec.LookPath(name); err != nil {
		t.Skip(name + " not installed")
	}
}

// realDir resolves symlinks (macOS /var -> /private/var) so paths match the
// working directory the scripts report.
func realDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// cwdFixture reads testdata/name with {{CWD}} replaced by dir.
func cwdFixture(t *testing.T, name, dir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return strings.ReplaceAll(string(b), "{{CWD}}", dir)
}

// summarize runs a summary script on a fixture report the way reportRun does.
func summarize(t *testing.T, interpreter, flag, script, kind, report string) summary {
	t.Helper()
	dir := realDir(t)
	writeFile(t, dir, "report", cwdFixture(t, report, dir))
	writeFile(t, dir, "out", "runner console output\n")
	cmd := exec.Command(interpreter, flag, script, kind, "report", "out")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("%s summary: %v\n%s", kind, err, out)
	}
	s, code, err := parseSummary(string(out) + exitMarker + "1\n")
	if err != nil || code != 1 {
		t.Fatalf("parseSummary: %v (code %d)", err, code)
	}
	return s
}

func TestMochaSummary(t *testing.T) {
	needCmd(t, "node")
	s := summarize(t, "node", "-e", nodeSummaryScript, "mocha", "mocha_report.txt")
	rep := s.toReport(frameworkMocha, 1, nodeLocation)
	if rep.Status != "failed" || rep.Passed != 1 || rep.Failed != 1 || rep.Skipped != 1 || len(rep.Failures) != 1 {
		t.Fatalf("report = %+v", rep)
	}
	f := rep.Failures[0]
	if f.Package != "test/user.js" || f.Test != "User getUserData returns the default avatar" || f.File != "test/user.js" || f.Line != 42 || !strings.Contains(f.Message, "'/assets/a.png' !== '/assets/uploads/a.png'") {
		t.Errorf("failure = %+v", f)
	}
	if !strings.Contains(s.Output, "console output before the report") || !strings.Contains(s.Output, "runner console output") {
		t.Errorf("output = %q", s.Output)
	}
}

func TestJestSummary(t *testing.T) {
	needCmd(t, "node")
	s := summarize(t, "node", "-e", nodeSummaryScript, "jest", "jest_report.json")
	rep := s.toReport(frameworkJest, 1, nodeLocation)
	if rep.Status != "failed" || rep.Passed != 2 || rep.Failed != 2 || rep.Skipped != 1 || len(rep.Failures) != 2 {
		t.Fatalf("report = %+v", rep)
	}
	assertion, suite := rep.Failures[0], rep.Failures[1]
	if assertion.Test != "sum handles negatives" || assertion.File != "src/sum.test.ts" || assertion.Line != 9 || !strings.Contains(assertion.Message, "Expected: -1") || strings.Contains(assertion.Message, "\x1b") {
		t.Errorf("assertion failure = %+v", assertion)
	}
	if suite.Test != "" || suite.File != "src/broken.test.ts" || suite.Line != 1 || !strings.Contains(suite.Message, "Cannot find module './missing'") {
		t.Errorf("suite failure = %+v", suite)
	}
}

func TestPytestSummary(t *testing.T) {
	needCmd(t, "python3")
	s := summarize(t, "python3", "-c", pySummaryScript, "pytest", "pytest_report.xml")
	rep := s.toReport(frameworkPytest, 1, pyLocation)
	if rep.Status != "failed" || rep.Passed != 1 || rep.Failed != 2 || rep.Skipped != 1 || len(rep.Failures) != 2 {
		t.Fatalf("report = %+v", rep)
	}
	assertion, collection := rep.Failures[0], rep.Failures[1]
	if assertion.Package != "tests.test_api" || assertion.Test != "test_sum" || assertion.File != "tests/test_api.py" || assertion.Line != 8 || !strings.HasPrefix(assertion.Message, "assert 3 == 4") {
		t.Errorf("assertion failure = %+v", assertion)
	}
	if collection.File != "tests/test_broken.py" || collection.Line != 1 || !strings.Contains(collection.Message, "No module named 'missing'") {
		t.Errorf("collection failure = %+v", collection)
	}
}

func TestSummaryReportError(t *testing.T) {
	needCmd(t, "node")
	dir := realDir(t)
	writeFile(t, dir, "out", "npm error could not determine executable to run\n")
	cmd := exec.Command("node", "-e", nodeSummaryScript, "mocha", "missing-report", "out")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	s, _, err := parseSummary(string(out) + exitMarker + "1\n")
	if err != nil {
		t.Fatal(err)
	}
	rep := s.toReport(frameworkMocha, 1, nodeLocation)
	if rep.Status != "error" || !strings.Contains(rep.Output, "report was not written") || !strings.Contains(rep.Output, "could not determine executable") {
		t.Errorf("report = %+v", rep)
	}
}

func TestESLintSummary(t *testing.T) {
	needCmd(t, "node")
	s := summarize(t, "node", "-e", nodeSummaryScript, "eslint", "eslint_report.json")
	want := []Diagnostic{
		{File: "src/a.js", Line: 1, Column: 7, Severity: "warning", Source: "eslint", Message: "'x' is assigned a value but never used. (no-unused-vars)"},
		{File: "src/a.js", Line: 4, Column: 12, Severity: "error", Source: "eslint", Message: "Parsing error: Unexpected token )"},
	}
	if s.Error != "" || !slices.Equal(s.Diagnostics, want) {
		t.Errorf("diagnostics = %+v, error %q", s.Diagnostics, s.Error)
	}
}

func TestRuffSummary(t *testing.T) {
	needCmd(t, "python3")
	s := summarize(t, "python3", "-c", pySummaryScript, "ruff", "ruff_report.json")
	if s.Error != "" || len(s.Diagnostics) != 3 {
		t.Fatalf("diagnostics = %+v, error %q", s.Diagnostics, s.Error)
	}
	got := []string{}
	for _, d := range s.Diagnostics {
		got = append(got, d.File+":"+d.Severity)
	}
	if want := []string{"app/models.py:warning", "app/views.py:error", "app/bad.py:error"}; !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestParseTsc(t *testing.T) {
	out := "src/a.ts(3,7): error TS2322: Type 'string' is not assignable to type 'number'.\n" +
		"src/b.ts(10,1): error TS2345: Argument of type 'X' is not assignable.\n  Property 'id' is missing.\n"
	got := parseTsc(out)
	if len(got) != 2 || got[0] != (Diagnostic{File: "src/a.ts", Line: 3, Column: 7, Severity: "error", Source: "tsc", Message: "Type 'string' is not assignable to type 'number'. (TS2322)"}) ||
		!strings.HasSuffix(got[1].Message, "(TS2345)\nProperty 'id' is missing.") {
		t.Errorf("diagnostics = %+v", got)
	}
}

func TestDiagnosticsPythonSyntaxCheck(t *testing.T) {
	needCmd(t, "python3")
	if _, err := exec.LookPath("ruff"); err == nil {
		t.Skip("ruff installed; the syntax check fallback is not used")
	}
	dir := t.TempDir()
	writeFile(t, dir, "pyproject.toml", "[project]\nname = 'x'\n")
	writeFile(t, dir, "app/ok.py", "x = 1\n")
	writeFile(t, dir, "app/bad.py", "def f(:\n    pass\n")
	writeFile(t, dir, ".venv/lib/broken.py", "def (\n")
	diags := decode[[]Diagnostic](t, invoke(t, NewDiagnostics(newShell(t, dir)), nil))
	if len(diags) != 1 || diags[0].File != "app/bad.py" || diags[0].Line != 1 || !strings.HasPrefix(diags[0].Message, "SyntaxError") {
		t.Fatalf("diagnostics = %+v", diags)
	}
	if _, err := os.Stat(filepath.Join(dir, "app/__pycache__")); err == nil {
		t.Error("the syntax check wrote __pycache__")
	}
}

func TestDiagnosticsNodeSyntaxCheck(t *testing.T) {
	needCmd(t, "npx")
	dir := t.TempDir()
	writeFile(t, dir, "package.json", `{"name":"x"}`)
	writeFile(t, dir, "src/ok.js", "#!/usr/bin/env node\nconst x = require('x');\nmodule.exports = x;\n")
	writeFile(t, dir, "src/esm.mjs", "import fs from 'fs';\nexport default fs;\n")
	writeFile(t, dir, "src/bad.js", "function f() {\n  return 1;\n}\n}\n")
	writeFile(t, dir, "src/bad.mjs", "export const = 1;\n")
	writeFile(t, dir, "src/types.ts", "export const n: number = 1;\n")
	writeFile(t, dir, "node_modules/dep/broken.js", "function (\n")
	diags := decode[[]Diagnostic](t, invoke(t, NewDiagnostics(newShell(t, dir)), nil))
	if len(diags) != 3 {
		t.Fatalf("diagnostics = %+v", diags)
	}
	bad, mjs, info := diags[0], diags[1], diags[2]
	if bad.File != "src/bad.js" || bad.Line != 4 || bad.Source != "node" || !strings.HasPrefix(bad.Message, "SyntaxError") {
		t.Errorf("bad.js = %+v", bad)
	}
	if mjs.File != "src/bad.mjs" || mjs.Line != 1 || !strings.HasPrefix(mjs.Message, "SyntaxError") {
		t.Errorf("bad.mjs = %+v", mjs)
	}
	if info.Severity != "info" || !strings.Contains(info.Message, "1 TypeScript files were not type-checked") {
		t.Errorf("info = %+v", info)
	}
}

func TestDiagnosticsNodeSyntaxCheckFilesAndModuleType(t *testing.T) {
	needCmd(t, "npx")
	dir := t.TempDir()
	writeFile(t, dir, "package.json", `{"name":"x","type":"module"}`)
	writeFile(t, dir, "a.js", "import fs from 'fs';\nexport default fs;\n")
	writeFile(t, dir, "b.js", "export default {;\n")
	ok := decode[[]Diagnostic](t, invoke(t, NewDiagnostics(newShell(t, dir)), map[string]any{"files": []any{"a.js"}}))
	if len(ok) != 0 {
		t.Fatalf("a.js diagnostics = %+v", ok)
	}
	bad := decode[[]Diagnostic](t, invoke(t, NewDiagnostics(newShell(t, dir)), nil))
	if len(bad) != 1 || bad[0].File != "b.js" || bad[0].Line != 1 {
		t.Fatalf("diagnostics = %+v", bad)
	}
}

func TestDiagnosticsResultOrdersErrorsFirst(t *testing.T) {
	res := diagnosticsResult(target{}, []Diagnostic{{File: "a", Severity: "warning"}, {File: "b", Severity: "info"}, {File: "c", Severity: "error"}}, false)
	got := decode[[]Diagnostic](t, res)
	if got[0].File != "c" || got[1].File != "a" || got[2].File != "b" {
		t.Errorf("order = %+v", got)
	}
}

func TestToReportStatus(t *testing.T) {
	cases := []struct {
		name string
		s    summary
		code int
		want string
	}{
		{"passed", summary{Passed: 3}, 0, "passed"},
		{"nothing ran", summary{}, 5, "no_tests"},
		{"exit code without failures", summary{Passed: 3}, 1, "error"},
		{"failed", summary{Passed: 1, Failed: 1}, 1, "failed"},
	}
	for _, c := range cases {
		if got := c.s.toReport(frameworkPytest, c.code, pyLocation).Status; got != c.want {
			t.Errorf("%s: status %q, want %q", c.name, got, c.want)
		}
	}
}

func TestNodeLocationSkipsDependencies(t *testing.T) {
	stack := "    at assert (/p/node_modules/chai/index.js:3:1)\n    at helper (/p/src/lib/util.ts:7:2)\n    at Context.<anonymous> (file:///p/test/a.test.ts:12:5)"
	if file, line := nodeLocation(stack, "/p", "test/a.test.ts"); file != "test/a.test.ts" || line != 12 {
		t.Errorf("test file frame: %s:%d", file, line)
	}
	if file, line := nodeLocation(stack, "/p", "test/other.js"); file != "src/lib/util.ts" || line != 7 {
		t.Errorf("first project frame: %s:%d", file, line)
	}
}

// A fake mocha in node_modules/.bin checks the command wiring without
// installing packages.
func TestRunTestsMochaEndToEnd(t *testing.T) {
	needCmd(t, "npx")
	dir := realDir(t)
	writeFile(t, dir, "package.json", `{"scripts":{"test":"mocha"},"devDependencies":{"mocha":"10"}}`)
	writeFile(t, dir, "report.json", cwdFixture(t, "mocha_report.txt", dir))
	writeFile(t, dir, "node_modules/.bin/mocha", "#!/bin/sh\necho \"$@\" > args.txt\ncat report.json\nexit 1\n")
	if err := os.Chmod(filepath.Join(dir, "node_modules/.bin/mocha"), 0o755); err != nil {
		t.Fatal(err)
	}
	rep := decode[TestReport](t, invoke(t, NewRunTests(newShell(t, dir)), map[string]any{"files": []any{"test/user.js"}, "run": "avatar"}))
	if rep.Framework != frameworkMocha || rep.Status != "failed" || len(rep.Failures) != 1 || rep.Failures[0].Line != 42 {
		t.Fatalf("report = %+v", rep)
	}
	args, _ := os.ReadFile(filepath.Join(dir, "args.txt"))
	if got := strings.TrimSpace(string(args)); got != "--reporter json --exit --grep avatar test/user.js" {
		t.Errorf("mocha args = %q", got)
	}
}

func TestRunTestsNpmFallback(t *testing.T) {
	needCmd(t, "npm")
	dir := t.TempDir()
	writeFile(t, dir, "package.json", `{"scripts":{"test":"node t.js"}}`)
	writeFile(t, dir, "t.js", "console.log('all good'); process.exit(process.argv.length > 2 ? 1 : 0)\n")
	rep := decode[TestReport](t, invoke(t, NewRunTests(newShell(t, dir)), nil))
	if rep.Framework != frameworkNpm || rep.Status != "passed" || !strings.Contains(rep.Output, "all good") {
		t.Fatalf("report = %+v", rep)
	}
	rep = decode[TestReport](t, invoke(t, NewRunTests(newShell(t, dir)), map[string]any{"files": []any{"t.js"}}))
	if rep.Status != "failed" {
		t.Errorf("with files: %+v", rep)
	}
}

// A fake pytest module on the working directory's import path checks the
// command wiring without installing pytest.
func TestRunTestsPytestEndToEnd(t *testing.T) {
	needCmd(t, "python3")
	dir := realDir(t)
	writeFile(t, dir, "pyproject.toml", "[project]\nname = 'x'\n")
	writeFile(t, dir, "report.xml", cwdFixture(t, "pytest_report.xml", dir))
	writeFile(t, dir, "pytest/__init__.py", "")
	writeFile(t, dir, "pytest/__main__.py", `import shutil, sys
args = sys.argv[1:]
shutil.copy("report.xml", next(a.split("=", 1)[1] for a in args if a.startswith("--junitxml=")))
open("args.txt", "w").write(" ".join(a for a in args if not a.startswith("--junitxml=")))
sys.exit(1)
`)
	rep := decode[TestReport](t, invoke(t, NewRunTests(newShell(t, dir)), map[string]any{"run": "sum", "files": []any{"tests/test_api.py"}}))
	if rep.Framework != frameworkPytest || rep.Status != "failed" || rep.Failed != 2 || rep.Failures[0].Line != 8 {
		t.Fatalf("report = %+v", rep)
	}
	args, _ := os.ReadFile(filepath.Join(dir, "args.txt"))
	if got := string(args); got != "-q -p no:cacheprovider -k sum tests/test_api.py" {
		t.Errorf("pytest args = %q", got)
	}
}

// unittestProject has a passing, a failing, a skipped and an erroring test,
// plus a module that fails to import.
func unittestProject(t *testing.T) string {
	t.Helper()
	dir := realDir(t)
	writeFile(t, dir, "setup.py", "from setuptools import setup\nsetup(name='x')\n")
	writeFile(t, dir, "calc.py", "def add(a, b):\n    return a - b\n")
	writeFile(t, dir, "tests/__init__.py", "")
	writeFile(t, dir, "tests/test_calc.py", `import unittest
from calc import add


class CalcTest(unittest.TestCase):
    def test_ok(self):
        self.assertEqual(add(1, 1), 0)

    def test_sum(self):
        self.assertEqual(add(1, 2), 3)

    @unittest.skip("later")
    def test_skip(self):
        pass

    def test_boom(self):
        raise RuntimeError("boom")
`)
	writeFile(t, dir, "tests/test_broken.py", "import missing\n")
	return dir
}

func TestRunTestsUnittestFallback(t *testing.T) {
	needCmd(t, "python3")
	if exec.Command("python3", "-c", "import pytest").Run() == nil {
		t.Skip("pytest installed; the unittest fallback is not used")
	}
	dir := unittestProject(t)
	rep := decode[TestReport](t, invoke(t, NewRunTests(newShell(t, dir)), nil))
	if rep.Framework != frameworkUnittest || rep.Status != "failed" || rep.Passed != 1 || rep.Failed != 3 || rep.Skipped != 1 {
		t.Fatalf("report = %+v", rep)
	}
	byTest := map[string]TestFailure{}
	for _, f := range rep.Failures {
		byTest[f.Test] = f
	}
	sum := byTest["tests.test_calc.CalcTest.test_sum"]
	if sum.File != "tests/test_calc.py" || sum.Line != 10 || !strings.Contains(sum.Message, "AssertionError: -1 != 3") {
		t.Errorf("test_sum = %+v", sum)
	}
	if boom := byTest["tests.test_calc.CalcTest.test_boom"]; boom.Line != 17 || boom.Message != "RuntimeError: boom" {
		t.Errorf("test_boom = %+v", boom)
	}
	if !strings.Contains(fmt.Sprint(rep.Failures), "No module named 'missing'") {
		t.Errorf("import error missing: %+v", rep.Failures)
	}

	one := decode[TestReport](t, invoke(t, NewRunTests(newShell(t, dir)), map[string]any{"files": []any{"tests/test_calc.py"}, "run": "test_ok"}))
	if one.Status != "passed" || one.Passed != 1 || one.Failed != 0 {
		t.Errorf("filtered report = %+v", one)
	}
}

func TestParseUnittest(t *testing.T) {
	out := `test_a (m.T) ... ok
test_b (m.T) ... FAIL
test_c (m.T) ... expected failure

======================================================================
FAIL: test_b (m.T)
----------------------------------------------------------------------
Traceback (most recent call last):
  File "/w/m.py", line 7, in test_b
    self.fail("nope")
AssertionError: nope

----------------------------------------------------------------------
Ran 3 tests in 0.001s

FAILED (failures=1, expected failures=1)
`
	s := parseUnittest(out, "/w")
	if s.Passed != 2 || s.Failed != 1 || len(s.Failures) != 1 {
		t.Fatalf("summary = %+v", s)
	}
	rep := s.toReport(frameworkUnittest, 1, pyLocation)
	if f := rep.Failures[0]; f.Test != "m.T.test_b" || f.File != "m.py" || f.Line != 7 || f.Message != "AssertionError: nope" {
		t.Errorf("failure = %+v", f)
	}
	empty := parseUnittest("\n----------------------------------------------------------------------\nRan 0 tests in 0.000s\n\nNO TESTS RAN\n", "/w")
	if got := empty.toReport(frameworkUnittest, 5, pyLocation).Status; got != "no_tests" {
		t.Errorf("Ran 0 tests: status %q", got)
	}
	if got := parseUnittest("usage: python -m unittest ...", "/w").toReport(frameworkUnittest, 2, pyLocation).Status; got != "error" {
		t.Errorf("no result line: status %q", got)
	}
}
