package codetools

import (
	"context"
	"regexp"
	"strings"
	"time"

	"github.com/tsengmingchun/deepagents_golang/backend"
	"github.com/tsengmingchun/deepagents_golang/tool"
)

const nodeReportHint = " (are the dependencies installed? try npm ci or npm install, or run the tests with execute)"

// rawNoTests spots runners that found nothing to run in output run_tests
// cannot parse.
var rawNoTests = regexp.MustCompile(`(?i)\b0 passing\b|no tests (?:ran|found)|Tests:\s+0 total|No test files found|collected 0 items`)

func nodeRunTests(ctx context.Context, sb backend.SandboxBackend, t target, pattern string, timeout time.Duration) *tool.Result {
	p, files := t.p, t.files
	if !p.cmds["node"] || !p.cmds["npx"] {
		return jsonResult(Unsupported{Status: "unsupported", Reason: "node or npx is not installed", Hint: "install Node.js, or use execute to run the project's tests"})
	}
	args := quoteAll(files)
	fw := p.nodeFramework()
	var runner string
	switch fw {
	case frameworkMocha:
		if pattern != "" {
			args = "--grep " + shellQuote(pattern) + " " + args
		}
		runner = `npx --no-install mocha --reporter json --exit ` + args + ` > "$tmp.report" 2> "$tmp.out"`
	case frameworkJest:
		if pattern != "" {
			args = "-t " + shellQuote(pattern) + " " + args
		}
		runner = `npx --no-install jest --ci --json --outputFile="$tmp.report" ` + args + ` > "$tmp.out" 2>&1`
	case frameworkVitest:
		if pattern != "" {
			args = "-t " + shellQuote(pattern) + " " + args
		}
		runner = `npx --no-install vitest run --reporter=json --outputFile="$tmp.report" ` + args + ` > "$tmp.out" 2>&1`
	default:
		return npmTest(ctx, sb, p, files, pattern, timeout)
	}
	res := run(ctx, sb, reportRun(runner, nodeSummarize(fw)), timeout)
	s, code, err := parseSummary(res.Stdout)
	if err != nil {
		return errorResult("Error: %s: %v", fw, err)
	}
	if s.Error != "" {
		s.Error += nodeReportHint
	}
	rep := s.toReport(fw, code, nodeLocation)
	t.fixFailures(&rep)
	rep.Truncated = res.Truncated
	return jsonResult(rep)
}

// npmTest runs the test script when no known framework is found; only the
// exit code and raw output are reported.
func npmTest(ctx context.Context, sb backend.SandboxBackend, p project, files []string, pattern string, timeout time.Duration) *tool.Result {
	if !p.cmds["npm"] {
		return jsonResult(Unsupported{Status: "unsupported", Reason: "npm is not installed", Hint: "use execute to run the project's tests"})
	}
	if pattern != "" {
		return errorResult("Error: run is not supported for this project's test script (no mocha, jest or vitest found in package.json); use execute")
	}
	if _, ok := p.pkg.Scripts["test"]; !ok {
		return jsonResult(Unsupported{Status: "unsupported", Reason: "package.json has no test script and no mocha, jest or vitest dependency", Hint: "use execute to run the project's tests"})
	}
	cmd := "npm test --silent"
	if len(files) > 0 {
		cmd += " -- " + quoteAll(files)
	}
	res := run(ctx, sb, cmd+" 2>&1; echo "+exitMarker+"$?", timeout)
	outs, codes := splitExit(res.Stdout)
	if len(codes) == 0 {
		return errorResult("Error: npm test did not finish: %s", clipRaw(res.errText()))
	}
	rep := TestReport{Framework: frameworkNpm, Status: "passed", Failures: []TestFailure{}, BuildErrors: []Diagnostic{}, Output: clipRaw(outs[0]), Truncated: res.Truncated}
	switch {
	case codes[0] != 0:
		rep.Status = "failed"
	case rawNoTests.MatchString(outs[0]):
		rep.Status = "no_tests"
	}
	return jsonResult(rep)
}

// tscLine matches `tsc --pretty false` output: "src/a.ts(3,7): error TS2322: message".
var tscLine = regexp.MustCompile(`^(.+?)\((\d+),(\d+)\): (error|warning) (TS\d+): (.*)$`)

// nodeDiagnostics runs tsc on the whole project when tsconfig.json exists
// and eslint on files (default ".") when an ESLint config exists. A checker
// that cannot run becomes an "info" diagnostic so the other still reports.
// Without either it falls back to a syntax check of the JavaScript files.
func nodeDiagnostics(ctx context.Context, sb backend.SandboxBackend, t target, timeout time.Duration) *tool.Result {
	p, files := t.p, t.files
	if !p.cmds["node"] || !p.cmds["npx"] {
		return jsonResult(Unsupported{Status: "unsupported", Reason: "node or npx is not installed", Hint: "use execute to run the project's lint or build commands"})
	}
	tsc, eslint := p.files["tsconfig.json"], p.hasESLintConfig()
	if !tsc && !eslint {
		return nodeSyntaxCheck(ctx, sb, t, timeout)
	}
	var diags []Diagnostic
	truncated := false
	if tsc {
		res := run(ctx, sb, "npx --no-install tsc --noEmit --pretty false 2>&1; echo "+exitMarker+"$?", timeout)
		outs, codes := splitExit(res.Stdout)
		if len(codes) == 0 {
			return errorResult("Error: tsc did not finish: %s", clipRaw(res.errText()))
		}
		found := parseTsc(outs[0])
		if codes[0] != 0 && len(found) == 0 {
			found = append(found, Diagnostic{Severity: "info", Source: "tsc", Message: "tsc failed without file diagnostics" + nodeReportHint + ":\n" + clipLines(outs[0], 20)})
		}
		diags = append(diags, found...)
		truncated = truncated || res.Truncated
	}
	if eslint {
		targets := "."
		if len(files) > 0 {
			targets = quoteAll(files)
		}
		res := run(ctx, sb, reportRun(`npx --no-install eslint -f json `+targets+` > "$tmp.report" 2> "$tmp.out"`, nodeSummarize("eslint")), timeout)
		s, _, err := parseSummary(res.Stdout)
		if err != nil {
			return errorResult("Error: eslint: %v", err)
		}
		if s.Error != "" {
			diags = append(diags, Diagnostic{Severity: "info", Source: "eslint", Message: "eslint did not run: " + s.Error + nodeReportHint + ":\n" + clipLines(s.Output, 20)})
		}
		diags = append(diags, s.Diagnostics...)
		truncated = truncated || res.Truncated
	}
	return diagnosticsResult(t, diags, truncated)
}

func parseTsc(out string) []Diagnostic {
	var diags []Diagnostic
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if m := tscLine.FindStringSubmatch(line); m != nil {
			diags = append(diags, Diagnostic{File: m[1], Line: atoi(m[2]), Column: atoi(m[3]), Severity: m[4], Source: "tsc", Message: m[6] + " (" + m[5] + ")"})
		} else if len(diags) > 0 && strings.HasPrefix(line, "  ") {
			diags[len(diags)-1].Message += "\n" + strings.TrimSpace(line)
		}
	}
	return diags
}

// clipLines keeps the last n lines of s.
func clipLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return clipRaw(strings.Join(lines, "\n"))
}

func quoteAll(items []string) string {
	q := make([]string, len(items))
	for i, s := range items {
		q[i] = shellQuote(s)
	}
	return strings.Join(q, " ")
}

// nodeSyntaxCheck syntax-checks the JavaScript files (default the whole
// project) for projects with no tsconfig.json or ESLint config.
func nodeSyntaxCheck(ctx context.Context, sb backend.SandboxBackend, t target, timeout time.Duration) *tool.Result {
	res := run(ctx, sb, "node -e "+shellQuote(nodeCheckScript)+" "+quoteAll(t.files)+"; echo "+exitMarker+"$?", timeout)
	s, _, err := parseSummary(res.Stdout)
	if err != nil {
		return errorResult("Error: node syntax check: %v", err)
	}
	return diagnosticsResult(t, s.Diagnostics, res.Truncated)
}
