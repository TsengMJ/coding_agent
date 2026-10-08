package codetools

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/tsengmingchun/quill/apps/agent/backend"
	"github.com/tsengmingchun/quill/apps/agent/tool"
)

const pyReportHint = " (is pytest installed? try pip install pytest, or run the tests with execute)"

// pyDiagnostics runs ruff when installed (syntax errors and undefined names
// are errors, other rules warnings); otherwise it syntax-checks the files.
func pyDiagnostics(ctx context.Context, sb backend.SandboxBackend, t target, timeout time.Duration) *tool.Result {
	p, files := t.p, t.files
	py := p.python()
	if py == "" {
		return jsonResult(Unsupported{Status: "unsupported", Reason: "python3 is not installed", Hint: "use execute to run the project's lint commands"})
	}
	targets := quoteAll(files)
	var cmd string
	if p.cmds["ruff"] {
		if targets == "" {
			targets = "."
		}
		cmd = reportRun(`ruff check --output-format json --no-cache --exit-zero `+targets+` > "$tmp.report" 2> "$tmp.out"`, pySummarize(py, "ruff"))
	} else {
		cmd = py + " -c " + shellQuote(pyCompileScript) + " " + targets + "; echo " + exitMarker + "$?"
	}
	res := run(ctx, sb, cmd, timeout)
	s, _, err := parseSummary(res.Stdout)
	if err != nil {
		return errorResult("Error: python diagnostics: %v", err)
	}
	if s.Error != "" {
		return errorResult("Error: ruff did not run: %s\n%s", s.Error, clipLines(s.Output, 20))
	}
	return diagnosticsResult(t, s.Diagnostics, res.Truncated)
}

func pyRunTests(ctx context.Context, sb backend.SandboxBackend, t target, pattern string, timeout time.Duration) *tool.Result {
	p, files := t.p, t.files
	py := p.python()
	if py == "" {
		return jsonResult(Unsupported{Status: "unsupported", Reason: "python3 is not installed", Hint: "use execute to run the project's tests"})
	}
	if !hasPytest(ctx, sb, py) {
		return unittestRun(ctx, sb, t, py, pattern, timeout)
	}
	args := quoteAll(files)
	if pattern != "" {
		args = "-k " + shellQuote(pattern) + " " + args
	}
	runner := `PYTHONDONTWRITEBYTECODE=1 ` + py + ` -m pytest -q -p no:cacheprovider --junitxml="$tmp.report" ` + args + ` > "$tmp.out" 2>&1`
	res := run(ctx, sb, reportRun(runner, pySummarize(py, frameworkPytest)), timeout)
	s, code, err := parseSummary(res.Stdout)
	if err != nil {
		return errorResult("Error: pytest: %v", err)
	}
	if s.Error != "" {
		s.Error += pyReportHint
	}
	rep := s.toReport(frameworkPytest, code, pyLocation)
	t.fixFailures(&rep)
	rep.Truncated = res.Truncated
	return jsonResult(rep)
}

func hasPytest(ctx context.Context, sb backend.SandboxBackend, py string) bool {
	_, codes := splitExit(run(ctx, sb, py+" -c 'import pytest' >/dev/null 2>&1; echo "+exitMarker+"$?", probeTimeout).Stdout)
	return len(codes) > 0 && codes[0] == 0
}

// unittestRun runs the standard library unittest runner for projects
// without pytest: discovery by default, else the given .py files or a single
// directory.
func unittestRun(ctx context.Context, sb backend.SandboxBackend, t target, py, pattern string, timeout time.Duration) *tool.Result {
	args, err := unittestArgs(t.files)
	if err != nil {
		return errorResult("Error: %v", err)
	}
	args += " -v"
	if pattern != "" {
		args += " -k " + shellQuote(pattern)
	}
	cmd := `pwd; PYTHONDONTWRITEBYTECODE=1 ` + py + ` -m unittest ` + args + ` 2>&1; echo ` + exitMarker + `$?`
	res := run(ctx, sb, cmd, timeout)
	outs, codes := splitExit(res.Stdout)
	if len(codes) == 0 {
		return errorResult("Error: unittest did not finish: %s", clipRaw(res.errText()))
	}
	cwd, out, _ := strings.Cut(outs[0], "\n")
	rep := parseUnittest(out, strings.TrimSpace(cwd)).toReport(frameworkUnittest, codes[0], pyLocation)
	t.fixFailures(&rep)
	rep.Truncated = res.Truncated
	return jsonResult(rep)
}

func unittestArgs(files []string) (string, error) {
	if len(files) == 0 {
		return "discover", nil
	}
	if len(files) == 1 && !strings.HasSuffix(files[0], ".py") {
		return "discover -s " + shellQuote(files[0]) + " -t .", nil
	}
	for _, f := range files {
		if !strings.HasSuffix(f, ".py") {
			return "", fmt.Errorf("without pytest, files must be .py test files or a single directory, got %q", f)
		}
	}
	return quoteAll(files), nil
}

var (
	unittestRan     = regexp.MustCompile(`(?m)^Ran (\d+) tests? in `)
	unittestCounts  = regexp.MustCompile(`[(,] ?(failures|errors|skipped)=(\d+)`)
	unittestHeader  = regexp.MustCompile(`^(?:FAIL|ERROR): (\S+) \(([^)]+)\)`)
	unittestDivider = strings.Repeat("=", 70)
	unittestRule    = strings.Repeat("-", 70)
)

// parseUnittest reads "python -m unittest -v" output: the "Ran N tests"
// line, the FAILED (failures=a, errors=b, skipped=c) or OK (skipped=c) line,
// and a FAIL:/ERROR: block with a traceback per failed test.
func parseUnittest(out, cwd string) summary {
	s := summary{Cwd: cwd, Output: out}
	ran := unittestRan.FindStringSubmatchIndex(out)
	if ran == nil {
		s.Error = "unittest did not report a result"
		return s
	}
	total := atoi(out[ran[2]:ran[3]])
	result := out[ran[1]:]
	if i := strings.Index(result, "\n\n"); i >= 0 {
		result = result[i:]
	}
	if line, _, _ := strings.Cut(strings.TrimSpace(result), "\n"); strings.HasPrefix(line, "FAILED") || strings.HasPrefix(line, "OK") {
		for _, m := range unittestCounts.FindAllStringSubmatch(line, -1) {
			if m[1] == "skipped" {
				s.Skipped = atoi(m[2])
			} else {
				s.Failed += atoi(m[2])
			}
		}
	}
	s.Passed = max(total-s.Failed-s.Skipped, 0)
	for _, block := range strings.Split(out[:ran[0]], unittestDivider+"\n")[1:] {
		head, body, _ := strings.Cut(block, "\n"+unittestRule+"\n")
		m := unittestHeader.FindStringSubmatch(head)
		if m == nil || len(s.Failures) >= maxFailures {
			continue
		}
		id := m[2]
		if !strings.HasSuffix(id, "."+m[1]) {
			id += "." + m[1]
		}
		pkg := id
		if parts := strings.Split(id, "."); len(parts) > 2 {
			pkg = strings.Join(parts[:len(parts)-2], ".")
		}
		body = strings.TrimSuffix(strings.TrimRight(body, "\n"), unittestRule)
		s.Failures = append(s.Failures, summaryFailure{Package: pkg, Test: id, Message: tracebackMessage(body), Stack: body})
	}
	return s
}

// tracebackMessage keeps the exception lines after the last indented frame.
func tracebackMessage(tb string) string {
	lines := strings.Split(strings.TrimSpace(tb), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.HasPrefix(lines[i], " ") {
			return strings.Join(lines[i+1:], "\n")
		}
	}
	return strings.Join(lines, "\n")
}
