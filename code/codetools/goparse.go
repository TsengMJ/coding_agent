package codetools

import (
	"encoding/json"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Diagnostic is one compiler or vet finding.
type Diagnostic struct {
	File     string `json:"file"`
	Line     int    `json:"line"`
	Column   int    `json:"column,omitempty"`
	Severity string `json:"severity"`
	Source   string `json:"source"`
	Message  string `json:"message"`
}

// TestReport is the run_tests result.
type TestReport struct {
	// Status is "passed", "failed", "error", or "no_tests" when the runner
	// found nothing to run.
	Status      string        `json:"status"`
	Framework   string        `json:"framework,omitempty"`
	Passed      int           `json:"passed"`
	Failed      int           `json:"failed"`
	Skipped     int           `json:"skipped"`
	Failures    []TestFailure `json:"failures"`
	BuildErrors []Diagnostic  `json:"build_errors"`
	// Output holds unparsed output when the command itself failed.
	Output    string `json:"output,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
}

// TestFailure is one failed test, or a package that failed outside a test.
type TestFailure struct {
	Package string `json:"package"`
	Test    string `json:"test,omitempty"`
	File    string `json:"file,omitempty"`
	Line    int    `json:"line,omitempty"`
	Message string `json:"message"`
}

const (
	maxFailures        = 50
	maxDiagnostics     = 100
	maxFailureMsgLines = 20
	maxFailureMsgBytes = 2000
)

var (
	// diagLine matches "path/file.go:12:3: message", optionally after "vet: ".
	diagLine = regexp.MustCompile(`^(?:vet: )?(?:\./)?([^\s:][^:]*\.go):(\d+)(?::(\d+))?: (.+)$`)
	// testLoc matches the "file_test.go:182: message" prefix t.Error writes.
	testLoc = regexp.MustCompile(`^\s*([\w.\-/]+\.go):(\d+): ?(.*)$`)
	// goLoc finds the first file:line in panic stacks and other output.
	goLoc = regexp.MustCompile(`([\w.\-/]+\.go):(\d+)`)
)

// parseDiagnostics reads compiler / vet output. Indented lines continue the
// previous message.
func parseDiagnostics(out, source string) []Diagnostic {
	var diags []Diagnostic
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if m := diagLine.FindStringSubmatch(line); m != nil {
			n, _ := strconv.Atoi(m[2])
			col, _ := strconv.Atoi(m[3])
			diags = append(diags, Diagnostic{File: m[1], Line: n, Column: col, Severity: "error", Source: source, Message: m[4]})
			continue
		}
		if len(diags) > 0 && (strings.HasPrefix(line, "\t") || strings.HasPrefix(line, "    ")) {
			d := &diags[len(diags)-1]
			d.Message += "\n" + strings.TrimSpace(line)
		}
	}
	return diags
}

// dedupeDiagnostics drops repeats (vet re-reports compile errors) and caps
// the list; it reports whether anything was dropped by the cap.
func dedupeDiagnostics(diags []Diagnostic) ([]Diagnostic, bool) {
	type key struct {
		file      string
		line, col int
		msg       string
	}
	seen := map[key]bool{}
	out := make([]Diagnostic, 0, len(diags))
	for _, d := range diags {
		k := key{d.File, d.Line, d.Column, d.Message}
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, d)
	}
	if len(out) > maxDiagnostics {
		return out[:maxDiagnostics], true
	}
	return out, false
}

// testEvent is one `go test -json` (test2json) event.
type testEvent struct {
	Action      string
	Package     string
	Test        string
	Output      string
	ImportPath  string
	FailedBuild string
}

type testKey struct{ pkg, test string }

// parseTestJSON summarizes `go test -json` output. Counts cover leaf tests
// only, so a failing subtest is not counted again for its parent. Lines that
// are not JSON (build errors from older Go versions) are read as
// diagnostics.
func parseTestJSON(out string) TestReport {
	rep := TestReport{Failures: []TestFailure{}, BuildErrors: []Diagnostic{}}
	results := map[testKey]string{}
	var order []testKey
	output := map[testKey][]string{}
	buildOut := map[string][]string{}
	var plain []string
	failedPkgs := map[string]string{}
	var pkgOrder []string
	for _, line := range strings.Split(out, "\n") {
		var ev testEvent
		if !strings.HasPrefix(line, "{") || json.Unmarshal([]byte(line), &ev) != nil {
			plain = append(plain, line)
			continue
		}
		k := testKey{ev.Package, ev.Test}
		switch ev.Action {
		case "build-output":
			buildOut[ev.ImportPath] = append(buildOut[ev.ImportPath], strings.TrimRight(ev.Output, "\n"))
		case "output":
			output[k] = append(output[k], strings.TrimRight(ev.Output, "\n"))
		case "pass", "fail", "skip":
			if ev.Test == "" {
				if ev.Action == "fail" {
					failedPkgs[ev.Package] = ev.FailedBuild
					pkgOrder = append(pkgOrder, ev.Package)
				}
				continue
			}
			if _, ok := results[k]; !ok {
				order = append(order, k)
			}
			results[k] = ev.Action
		}
	}
	parents := map[testKey]bool{}
	for _, k := range order {
		if i := strings.LastIndex(k.test, "/"); i > 0 {
			parents[testKey{k.pkg, k.test[:i]}] = true
		}
	}
	failedInPkg := map[string]bool{}
	for _, k := range order {
		res := results[k]
		leaf := !parents[k]
		switch {
		case res == "pass" && leaf:
			rep.Passed++
		case res == "skip" && leaf:
			rep.Skipped++
		case res == "fail":
			// A parent counts only when it failed without a failing subtest.
			if !leaf && childFailed(k, order, results) {
				continue
			}
			rep.Failed++
			failedInPkg[k.pkg] = true
			if len(rep.Failures) < maxFailures {
				rep.Failures = append(rep.Failures, testFailure(k, output[k]))
			} else {
				rep.Truncated = true
			}
		}
	}
	var diags []Diagnostic
	for _, lines := range buildOut {
		diags = append(diags, parseDiagnostics(strings.Join(lines, "\n"), "build")...)
	}
	diags = append(diags, parseDiagnostics(strings.Join(plain, "\n"), "build")...)
	slices.SortStableFunc(diags, func(a, b Diagnostic) int {
		if c := strings.Compare(a.File, b.File); c != 0 {
			return c
		}
		if a.Line != b.Line {
			return a.Line - b.Line
		}
		return strings.Compare(a.Message, b.Message)
	})
	var capped bool
	rep.BuildErrors, capped = dedupeDiagnostics(diags)
	rep.Truncated = rep.Truncated || capped
	for _, pkg := range pkgOrder {
		if failedPkgs[pkg] != "" || failedInPkg[pkg] {
			continue
		}
		// The package failed outside any test: panic in init, TestMain,
		// timeout or a crash.
		msg := failureMessage(output[testKey{pkg, ""}])
		f := TestFailure{Package: pkg, Message: msg}
		f.File, f.Line = userLocation(msg)
		rep.Failures = append(rep.Failures, f)
		rep.Failed++
	}
	switch {
	case rep.Failed > 0 || len(rep.BuildErrors) > 0 || hasBuildFailure(failedPkgs):
		rep.Status = "failed"
	default:
		rep.Status = "passed"
	}
	return rep
}

func hasBuildFailure(failed map[string]string) bool {
	for _, fb := range failed {
		if fb != "" {
			return true
		}
	}
	return false
}

func childFailed(parent testKey, order []testKey, results map[testKey]string) bool {
	prefix := parent.test + "/"
	for _, k := range order {
		if k.pkg == parent.pkg && strings.HasPrefix(k.test, prefix) && results[k] == "fail" {
			return true
		}
	}
	return false
}

// testFailure extracts the location and message t.Error / t.Fatal wrote.
func testFailure(k testKey, lines []string) TestFailure {
	f := TestFailure{Package: k.pkg, Test: k.test}
	var msg []string
	for i, l := range lines {
		if m := testLoc.FindStringSubmatch(l); m != nil && f.File == "" && !strings.HasPrefix(strings.TrimSpace(l), "---") {
			f.File, f.Line = m[1], atoi(m[2])
			msg = append(msg, m[3])
			indent := len(l) - len(strings.TrimLeft(l, " \t"))
			for _, next := range lines[i+1:] {
				if len(next)-len(strings.TrimLeft(next, " \t")) <= indent || strings.HasPrefix(strings.TrimSpace(next), "---") {
					break
				}
				msg = append(msg, strings.TrimSpace(next))
			}
			break
		}
	}
	if len(msg) > 0 {
		f.Message = clipMessage(msg)
		return f
	}
	f.Message = failureMessage(lines)
	f.File, f.Line = userLocation(strings.Join(lines, "\n"))
	return f
}

// userLocation picks the most useful file:line from panic stacks and other
// output: a test file first, then any file outside the Go runtime, the
// testing package and the module cache.
func userLocation(text string) (string, int) {
	matches := goLoc.FindAllStringSubmatch(text, -1)
	for _, m := range matches {
		if strings.HasSuffix(m[1], "_test.go") {
			return m[1], atoi(m[2])
		}
	}
	for _, m := range matches {
		if !strings.Contains(m[1], "/src/runtime/") && !strings.Contains(m[1], "/src/testing/") && !strings.Contains(m[1], "/pkg/mod/") {
			return m[1], atoi(m[2])
		}
	}
	return "", 0
}

// failureMessage keeps the informative lines of a test or package output.
func failureMessage(lines []string) string {
	var keep []string
	for _, l := range lines {
		t := strings.TrimSpace(l)
		if t == "" || t == "FAIL" || strings.HasPrefix(t, "=== ") || strings.HasPrefix(t, "--- FAIL") ||
			strings.HasPrefix(t, "FAIL\t") || strings.HasPrefix(t, "ok  \t") {
			continue
		}
		keep = append(keep, strings.TrimRight(l, " "))
	}
	if len(keep) == 0 {
		return "failed without output"
	}
	return clipMessage(keep)
}

func clipMessage(lines []string) string {
	if len(lines) > maxFailureMsgLines {
		lines = append(lines[:maxFailureMsgLines:maxFailureMsgLines], "...")
	}
	s := strings.Join(lines, "\n")
	if len(s) > maxFailureMsgBytes {
		s = truncateBytes(s, maxFailureMsgBytes) + "..."
	}
	return s
}

func truncateBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && (s[n]&0xC0) == 0x80 {
		n--
	}
	return s[:n]
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}
