package codetools

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

var (
	//go:embed scripts/summary.js
	nodeSummaryScript string
	//go:embed scripts/summary.py
	pySummaryScript string
	//go:embed scripts/pycompile.py
	pyCompileScript string
	//go:embed scripts/nodecheck.js
	nodeCheckScript string
)

// summary is what scripts/summary.{js,py} print: test counts and failures,
// or diagnostics, from a runner's report file.
type summary struct {
	Passed      int              `json:"passed"`
	Failed      int              `json:"failed"`
	Skipped     int              `json:"skipped"`
	Cwd         string           `json:"cwd"`
	Failures    []summaryFailure `json:"failures"`
	Diagnostics []Diagnostic     `json:"diagnostics"`
	Error       string           `json:"error"`
	Output      string           `json:"output"`
}

type summaryFailure struct {
	Package string `json:"package"`
	File    string `json:"file"`
	Test    string `json:"test"`
	Message string `json:"message"`
	Stack   string `json:"stack"`
}

// reportRun runs a test or lint command with its report in "$tmp.report"
// and its console output in "$tmp.out", then prints the summary and the
// runner's exit code. runner must reference those two files.
func reportRun(runner, summarize string) string {
	return `tmp=$(mktemp 2>/dev/null || echo /tmp/dcode-report-$$); ` +
		runner + `; code=$?; ` + summarize + `; rm -f "$tmp" "$tmp.report" "$tmp.out"; echo ` + exitMarker + `$code`
}

func nodeSummarize(kind string) string {
	return "node -e " + shellQuote(nodeSummaryScript) + " " + kind + ` "$tmp.report" "$tmp.out"`
}

func pySummarize(python, kind string) string {
	return python + " -c " + shellQuote(pySummaryScript) + " " + kind + ` "$tmp.report" "$tmp.out"`
}

// parseSummary reads the summary line and exit code from reportRun output.
func parseSummary(stdout string) (summary, int, error) {
	outs, codes := splitExit(stdout)
	if len(codes) == 0 {
		return summary{}, 0, fmt.Errorf("the command did not finish: %s", clipRaw(stdout))
	}
	var s summary
	lines := strings.Split(strings.TrimSpace(outs[0]), "\n")
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &s); err != nil {
		return summary{}, 0, fmt.Errorf("could not read the report summary: %s", clipRaw(outs[0]))
	}
	return s, codes[0], nil
}

// toReport turns a summary into a TestReport. locate finds file:line in a
// failure's stack; a runner that fails without reporting failures is an
// "error", and one that ran nothing is "no_tests".
func (s summary) toReport(framework string, exitCode int, locate func(stack, cwd, testFile string) (string, int)) TestReport {
	rep := TestReport{Framework: framework, Passed: s.Passed, Failed: s.Failed, Skipped: s.Skipped, Failures: []TestFailure{}, BuildErrors: []Diagnostic{}}
	for _, f := range s.Failures {
		file, line := locate(f.Stack, s.Cwd, f.File)
		if file == "" {
			file = f.File
		}
		pkg := f.Package
		if pkg == "" {
			pkg = f.File
		}
		rep.Failures = append(rep.Failures, TestFailure{Package: pkg, Test: f.Test, File: file, Line: line, Message: clipMessage(strings.Split(strings.TrimSpace(f.Message), "\n"))})
	}
	switch {
	case s.Error != "":
		rep.Status = "error"
		rep.Output = clipRaw(s.Error + "\n" + s.Output)
	case s.Failed > 0:
		rep.Status = "failed"
		if len(rep.Failures) == 0 {
			rep.Output = clipRaw(s.Output)
		}
	case exitCode != 0 && s.Passed+s.Skipped > 0:
		rep.Status = "error"
		rep.Output = clipRaw(fmt.Sprintf("the runner exited with code %d although no test failed:\n%s", exitCode, s.Output))
	case s.Passed == 0:
		rep.Status = "no_tests"
		rep.Output = clipRaw(s.Output)
	default:
		rep.Status = "passed"
	}
	return rep
}

var (
	// jsFrame matches "at fn (/abs/file.js:12:5)" and "at file:///abs/file.ts:3:1".
	jsFrame = regexp.MustCompile(`(?:file://)?((?:/|\.{0,2}/|[A-Za-z]:\\)?[^\s()'"]+\.(?:[cm]?[jt]sx?|vue|svelte)):(\d+):\d+`)
	// pyLoc matches pytest's "path/test_x.py:12: AssertionError" lines.
	pyLoc = regexp.MustCompile(`(?m)^(\S+\.py):(\d+): `)
	// pyFrame matches traceback frames: File "path/x.py", line 12
	pyFrame = regexp.MustCompile(`File "([^"]+\.py)", line (\d+)`)
)

// nodeLocation picks the stack frame in the test file, else the first frame
// in the project.
func nodeLocation(stack, cwd, testFile string) (string, int) {
	var first string
	var firstLine int
	for _, m := range jsFrame.FindAllStringSubmatch(stack, -1) {
		file := relTo(cwd, m[1])
		if strings.Contains(file, "node_modules/") || strings.HasPrefix(file, "node:") || strings.HasPrefix(file, "/") {
			continue
		}
		if file == testFile {
			return file, atoi(m[2])
		}
		if first == "" {
			first, firstLine = file, atoi(m[2])
		}
	}
	return first, firstLine
}

// pyLocation picks the last project frame: pytest prints the failing line of
// the test last.
func pyLocation(stack, cwd, _ string) (string, int) {
	matches := pyLoc.FindAllStringSubmatch(stack, -1)
	if len(matches) == 0 {
		matches = pyFrame.FindAllStringSubmatch(stack, -1)
	}
	for i := len(matches) - 1; i >= 0; i-- {
		file := relTo(cwd, matches[i][1])
		if strings.Contains(file, "site-packages/") || strings.HasPrefix(file, "/") {
			continue
		}
		return file, atoi(matches[i][2])
	}
	return "", 0
}

func relTo(cwd, file string) string {
	if cwd != "" {
		file = strings.TrimPrefix(file, strings.TrimSuffix(cwd, "/")+"/")
	}
	return strings.TrimPrefix(file, "./")
}
