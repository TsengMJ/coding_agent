package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"

	core "github.com/tsengmingchun/quill/agent"
	"github.com/tsengmingchun/quill/code/codetools"
	"github.com/tsengmingchun/quill/llm"
	rt "github.com/tsengmingchun/quill/runtime"
	"github.com/tsengmingchun/quill/tool"
)

// repairMarker starts the line the FailureClassifier appends to a check result.
const repairMarker = "[repair]"

// FailureCategory says what kind of failure a check reported. Only syntax,
// compile, lint, assertion and panic mean the code ran (or was built) and
// was wrong; the others mean the check never exercised the code.
type FailureCategory string

const (
	FailurePassed      FailureCategory = "passed"
	FailureSyntax      FailureCategory = "syntax"
	FailureCompile     FailureCategory = "compile"
	FailureLint        FailureCategory = "lint"
	FailureAssertion   FailureCategory = "assertion"
	FailurePanic       FailureCategory = "panic"
	FailureFailed      FailureCategory = "failed"
	FailureNoTests     FailureCategory = "no_tests"
	FailureTimeout     FailureCategory = "timeout"
	FailureEnvironment FailureCategory = "environment"
	FailureToolError   FailureCategory = "tool_error"
	FailureSandbox     FailureCategory = "sandbox_blocked"
)

// rank orders the code categories by how far the code got: a build error
// is behind failing tests, which are behind passing ones. Categories that
// say nothing about the code have no rank.
func (c FailureCategory) rank() (int, bool) {
	switch c {
	case FailureSyntax:
		return 0, true
	case FailureCompile:
		return 1, true
	case FailureAssertion, FailurePanic, FailureFailed, FailureLint:
		return 2, true
	case FailurePassed:
		return 3, true
	}
	return 0, false
}

// Signature is a check result reduced to what identifies the failure across
// runs and commands: failing test names, normalized error lines, or the
// environment problem. Timings, addresses and the command itself are left
// out, so a test failing the same way under run_tests and `npx mocha` has
// the same signature.
type Signature struct {
	Check    string          `json:"check"`
	Category FailureCategory `json:"category"`
	Tests    []string        `json:"tests,omitempty"`
	// Details are "test: first message line" for failing tests, or
	// "file:line: message" for build and lint errors, normalized.
	Details []string `json:"details,omitempty"`
	Env     string   `json:"env,omitempty"`
	// output is the normalized output, used when nothing else identifies
	// the failure.
	output string
	// messages is set when the runner reported why each test failed;
	// console output parsed from execute only names the tests.
	messages bool
}

// Key identifies the failure independently of the command that showed it.
func (s Signature) Key() string {
	parts := []string{string(s.Category), s.Env}
	parts = append(parts, s.Details...)
	if len(s.Details) == 0 {
		parts = append(parts, s.output)
	}
	return strings.Join(parts, "\n")
}

var (
	syntaxMessage = regexp.MustCompile(`(?i)syntax ?error|invalid syntax|unexpected (?:token|eof|end of (?:input|file)|newline)|expected .+, found|IndentationError|unterminated`)
	panicMessage  = regexp.MustCompile(`(?m)^(?:panic: |Traceback \(most recent call last\)|Segmentation fault|fatal error: )`)
	timedOut      = regexp.MustCompile(`(?i)timed out after|deadline exceeded`)

	jsModuleMissing = regexp.MustCompile(`Cannot find module '([^']+)'|Cannot find package '([^']+)'`)
	pyModuleMissing = regexp.MustCompile(`No module named '([^']+)'`)
	toolMissing     = regexp.MustCompile(`(?m)^(?:sh|bash|/bin/sh)?:?\s*(?:\d+: )?([\w./-]+): (?:command )?not found`)
	// networkFailure includes Go's `Get "<url>": <reason>`, which a module
	// download reports for any transport failure (a missing module is
	// "reading <url>: 404" instead).
	networkFailure = regexp.MustCompile(`ENOTFOUND|EAI_AGAIN|getaddrinfo|Could not resolve host|Temporary failure in name resolution|dial tcp: lookup|no such host|network is unreachable|CONNECT tunnel failed|Tunnel connection failed|Get "https?://[^"]+": `)
	goSumMissing   = regexp.MustCompile(`missing go\.sum entry for module providing package (\S+)`)

	goTestFail     = regexp.MustCompile(`(?m)^\s*--- FAIL: (\S+)`)
	pytestFail     = regexp.MustCompile(`(?m)^FAILED \S+::(\S+)`)
	mochaFailStart = regexp.MustCompile(`^\s+\d+\) (.+)$`)
	errorLine      = regexp.MustCompile(`(?m)^(?:\./)?([\w./-]+\.(?:go|[cm]?[jt]sx?|py)):(\d+)(?::\d+)?: (.+)$`)
	tscLine        = regexp.MustCompile(`(?m)^([\w./-]+\.[cm]?tsx?)\((\d+),\d+\): error (.+)$`)
)

// ClassifyFailure reads a run_tests, diagnostics or test / build execute
// result as a Signature; ok is false for any other call or an unusable
// result (for example "unsupported").
func ClassifyFailure(call llm.ToolCall, text string) (Signature, bool) {
	sandboxed := strings.Contains(text, sandboxNote)
	text = stripNotes(text)
	switch call.Name {
	case codetools.RunTestsName:
		return classifyRunTests(text)
	case codetools.DiagnosticsName:
		return classifyDiagnostics(text)
	case executeToolName:
		return classifyExecute(call, text, sandboxed)
	}
	return Signature{}, false
}

func classifyRunTests(text string) (Signature, bool) {
	s := Signature{Check: codetools.RunTestsName}
	var rep codetools.TestReport
	if err := json.Unmarshal([]byte(text), &rep); err != nil {
		if !strings.HasPrefix(text, "Error") {
			return s, false
		}
		s.Category, s.Env = environmentOf(text, FailureToolError)
		s.output = normalizeOutput(text)
		return s, true
	}
	switch rep.Status {
	case "", "unsupported":
		return s, false
	case "passed":
		s.Category = FailurePassed
		return s, true
	case "no_tests":
		s.Category = FailureNoTests
		return s, true
	}
	if len(rep.BuildErrors) > 0 {
		classifyBuildErrors(&s, rep.BuildErrors)
		return s, true
	}
	if len(rep.Failures) > 0 {
		s.Category = FailureAssertion
		for _, f := range rep.Failures {
			name := f.Test
			if name == "" {
				name = f.Package
			}
			if panicMessage.MatchString(f.Message) {
				s.Category = FailurePanic
			}
			s.addTest(name, f.Message)
		}
		return s, true
	}
	s.Category, s.Env = environmentOf(rep.Output, FailureToolError)
	if s.Category == FailureToolError && jsModuleMissing.MatchString(rep.Output) {
		s.Category = FailureCompile
	}
	s.output = normalizeOutput(rep.Output)
	return s, true
}

func classifyDiagnostics(text string) (Signature, bool) {
	s := Signature{Check: codetools.DiagnosticsName}
	var diags []codetools.Diagnostic
	if err := json.Unmarshal([]byte(text), &diags); err != nil {
		if !strings.HasPrefix(text, "Error") {
			return s, false
		}
		s.Category, s.Env = environmentOf(text, FailureToolError)
		s.output = normalizeOutput(text)
		return s, true
	}
	var errs []codetools.Diagnostic
	lint := false
	for _, d := range diags {
		switch d.Severity {
		case "info":
		case "error":
			errs = append(errs, d)
		default:
			lint = true
			s.Details = append(s.Details, diagnosticDetail(d))
		}
	}
	switch {
	case len(errs) > 0:
		warnings := s.Details
		s.Details = nil
		classifyBuildErrors(&s, errs)
		if s.Category != FailureEnvironment {
			s.Details = append(s.Details, warnings...)
		}
	case lint:
		s.Category = FailureLint
	default:
		s.Category = FailurePassed
	}
	return s, true
}

// classifyBuildErrors sets the category and details of s from build
// errors: environment when every error is a problem outside the code
// (a download that could not reach the network, a missing go.sum entry),
// otherwise compile or syntax with only the errors in the code.
func classifyBuildErrors(s *Signature, errs []codetools.Diagnostic) {
	var code, env []codetools.Diagnostic
	for _, d := range errs {
		if e, ok := envOfMessage(d.Message); ok {
			if s.Env == "" {
				s.Env = e
			}
			env = append(env, d)
		} else {
			code = append(code, d)
		}
	}
	if len(code) == 0 {
		s.Category = FailureEnvironment
		for _, d := range env {
			s.Details = append(s.Details, diagnosticDetail(d))
		}
		return
	}
	s.Env = ""
	s.Category = FailureCompile
	for _, d := range code {
		if syntaxMessage.MatchString(d.Message) {
			s.Category = FailureSyntax
		}
		s.Details = append(s.Details, diagnosticDetail(d))
	}
}

func classifyExecute(call llm.ToolCall, text string, sandboxed bool) (Signature, bool) {
	cmd, _ := call.Args["command"].(string)
	r := classifyCheck(call, text)
	if r == nil {
		return Signature{}, false
	}
	s := Signature{Check: normalizeOutput(cmd)}
	if r.passed {
		s.Category = FailurePassed
		return s, true
	}
	if r.detail == "ran no tests" {
		s.Category = FailureNoTests
		return s, true
	}
	if sandboxed {
		s.Category = FailureSandbox
		s.output = normalizeOutput(text)
		return s, true
	}
	if m := execExit.FindStringSubmatch(text); m != nil && m[1] == "124" || timedOut.MatchString(text) {
		s.Category = FailureTimeout
		return s, true
	}
	if cat, env := environmentOf(text, ""); cat != "" {
		s.Category, s.Env = cat, env
		return s, true
	}
	for _, name := range failingTestNames(text) {
		s.addTest(name, "")
	}
	if len(s.Tests) > 0 {
		s.Category = FailureAssertion
		if panicMessage.MatchString(text) {
			s.Category = FailurePanic
		}
		return s, true
	}
	if errs := errorLines(text); len(errs) > 0 {
		s.Category = FailureCompile
		for _, e := range errs {
			if syntaxMessage.MatchString(e) {
				s.Category = FailureSyntax
			}
			s.Details = append(s.Details, normalizeOutput(e))
		}
		return s, true
	}
	s.Category = FailureFailed
	s.output = normalizeOutput(text)
	return s, true
}

// environmentOf returns FailureEnvironment and what is missing when out
// shows a problem outside the code, otherwise fallback and "".
func environmentOf(out string, fallback FailureCategory) (FailureCategory, string) {
	if timedOut.MatchString(out) {
		return FailureTimeout, ""
	}
	if env, ok := envOfMessage(out); ok {
		return FailureEnvironment, env
	}
	return fallback, ""
}

// envOfMessage tells what is missing outside the code when msg shows it:
// "service:<port>", "module_not_found:<name>", "missing_tool:<name>",
// "dependency:<package>" or "network".
func envOfMessage(msg string) (string, bool) {
	if refusedMark.MatchString(msg) {
		if m := refusedPort.FindStringSubmatch(msg); m != nil {
			return "service:" + m[1], true
		}
		return "service", true
	}
	if m := jsModuleMissing.FindStringSubmatch(msg); m != nil {
		name := m[1] + m[2]
		if !strings.HasPrefix(name, ".") && !strings.HasPrefix(name, "/") {
			return "module_not_found:" + name, true
		}
	}
	if m := pyModuleMissing.FindStringSubmatch(msg); m != nil {
		return "module_not_found:" + m[1], true
	}
	if m := toolMissing.FindStringSubmatch(msg); m != nil {
		return "missing_tool:" + m[1], true
	}
	if networkFailure.MatchString(msg) {
		return "network", true
	}
	if m := goSumMissing.FindStringSubmatch(msg); m != nil {
		return "dependency:" + m[1], true
	}
	return "", false
}

func (s *Signature) addTest(name, message string) {
	if name == "" || slices.Contains(s.Tests, name) {
		return
	}
	s.Tests = append(s.Tests, name)
	first, _, _ := strings.Cut(strings.TrimSpace(message), "\n")
	s.Details = append(s.Details, normalizeOutput(name+": "+clipLine(first, 200)))
	s.messages = s.messages || first != ""
}

// sameFailure reports whether a and b fail the same way. Without messages
// on one side only the failing tests can be compared.
func sameFailure(a, b Signature) bool {
	if a.Key() == b.Key() {
		return true
	}
	if a.Category != b.Category || len(a.Tests) == 0 || a.messages && b.messages {
		return false
	}
	return len(missing(a.Tests, b.Tests)) == 0 && len(missing(b.Tests, a.Tests)) == 0
}

func diagnosticDetail(d codetools.Diagnostic) string {
	return normalizeOutput(fmt.Sprintf("%s: %s", d.File, clipLine(d.Message, 200)))
}

// failingTestNames reads failing test names from go test, pytest and mocha
// console output.
func failingTestNames(text string) []string {
	var names []string
	add := func(n string) {
		if n = strings.TrimSpace(n); n != "" && !slices.Contains(names, n) {
			names = append(names, n)
		}
	}
	for _, m := range goTestFail.FindAllStringSubmatch(text, -1) {
		add(m[1])
	}
	for _, m := range pytestFail.FindAllStringSubmatch(text, -1) {
		add(m[1])
	}
	// mocha prints "  1) Suite\n       test name:" with the title split
	// over indented lines that end at the one with a trailing colon.
	lines := strings.Split(text, "\n")
	for i := 0; i < len(lines); i++ {
		m := mochaFailStart.FindStringSubmatch(lines[i])
		if m == nil {
			continue
		}
		parts := []string{strings.TrimSpace(m[1])}
		for j := i; !strings.HasSuffix(parts[len(parts)-1], ":") && j+1 < len(lines) && j < i+5; j++ {
			next := strings.TrimSpace(lines[j+1])
			if next == "" {
				break
			}
			parts = append(parts, next)
			i = j + 1
		}
		add(strings.TrimSuffix(strings.Join(parts, " "), ":"))
	}
	return names
}

func errorLines(text string) []string {
	var out []string
	for _, m := range errorLine.FindAllStringSubmatch(text, 10) {
		out = append(out, fmt.Sprintf("%s: %s", m[1], m[3]))
	}
	for _, m := range tscLine.FindAllStringSubmatch(text, 10) {
		out = append(out, fmt.Sprintf("%s: %s", m[1], m[3]))
	}
	return out
}

// sandboxNote starts the explanation sandboxedShell appends to an execute
// result that looks blocked by the sandbox.
const sandboxNote = "\n[sandbox] "

// noteMarkers start annotations that middleware appends to tool results.
var noteMarkers = []string{"\n\n" + repairMarker + " ", "\n\n" + progressMarker + " ", "\n\n" + microMarker + " ", "\n\n" + reproMarker + " ", "\n\n" + stuckMarker + " ", "\n\n" + checkpointMarker + " ", "\n\n[hint] ", sandboxNote}

// stripNotes removes the annotations middleware appended to a tool result,
// leaving what the tool itself returned.
func stripNotes(text string) string {
	for _, m := range noteMarkers {
		if i := strings.Index(text, m); i >= 0 {
			text = text[:i]
		}
	}
	return text
}

// DeltaKind says how a check changed since the last comparable one.
type DeltaKind string

const (
	// DeltaFixed: it passes now.
	DeltaFixed DeltaKind = "fixed"
	// DeltaProgressed: fewer failures, or the code got further (it builds now).
	DeltaProgressed DeltaKind = "progressed"
	// DeltaUnchanged: the same failures, failing the same way.
	DeltaUnchanged DeltaKind = "unchanged"
	// DeltaChanged: different failures, the same tests failing differently,
	// or a result that says nothing about the code.
	DeltaChanged DeltaKind = "changed"
	// DeltaRegressed: more failures, or the code got less far.
	DeltaRegressed DeltaKind = "regressed"
)

// Delta compares two signatures of the same check.
type Delta struct {
	Kind  DeltaKind `json:"kind"`
	Fixed []string  `json:"fixed,omitempty"`
	New   []string  `json:"new,omitempty"`
}

// CompareFailures tells whether after is progress over before.
func CompareFailures(before, after Signature) Delta {
	rb, okb := before.Category.rank()
	ra, oka := after.Category.rank()
	var d Delta
	// Test lists compare only between results that ran the tests.
	ranTests := func(s Signature) bool { return len(s.Tests) > 0 || s.Category == FailurePassed }
	if ranTests(before) && ranTests(after) {
		d.Fixed, d.New = missing(before.Tests, after.Tests), missing(after.Tests, before.Tests)
	}
	switch {
	case !okb || !oka:
		d.Kind = DeltaChanged
	case after.Category == FailurePassed && before.Category != FailurePassed:
		d.Kind = DeltaFixed
	case ra > rb:
		d.Kind = DeltaProgressed
	case ra < rb:
		d.Kind = DeltaRegressed
	case sameFailure(before, after):
		d.Kind = DeltaUnchanged
	case len(d.Fixed) > 0 && len(d.New) == 0:
		d.Kind = DeltaProgressed
	case len(d.New) > 0 && len(d.Fixed) == 0:
		d.Kind = DeltaRegressed
	default:
		d.Kind = DeltaChanged
	}
	return d
}

// missing lists the items of a not in b.
func missing(a, b []string) []string {
	var out []string
	for _, x := range a {
		if !slices.Contains(b, x) {
			out = append(out, x)
		}
	}
	return out
}

// FailureClassifier appends a [repair] line to test, build and lint
// results: what kind of failure it is (so an environment problem is not
// mistaken for a bug in the code), and after an edit how the result
// compares with the previous run of the same tests.
type FailureClassifier struct{}

func (FailureClassifier) Name() string { return "FailureClassifierMiddleware" }

func (FailureClassifier) WrapToolCall(ctx context.Context, req *core.ToolCallRequest, next core.ToolHandler) (*tool.Result, error) {
	res, err := next(ctx, req)
	if err != nil || res == nil {
		return res, err
	}
	var history []llm.Message
	if req.State != nil {
		history = req.State.Messages
	}
	if line := repairLine(history, req.ToolCall, resultText(res)); line != "" {
		res.Content = append(slices.Clone(res.Content), llm.TextBlock("\n\n"+line))
	}
	return res, nil
}

// repairNote is the JSON after the [repair] marker.
type repairNote struct {
	Category FailureCategory `json:"category"`
	Check    string          `json:"check,omitempty"`
	Tests    []string        `json:"tests,omitempty"`
	Env      string          `json:"env,omitempty"`
	Delta    *Delta          `json:"delta,omitempty"`
	Advice   string          `json:"advice,omitempty"`
}

// repairLine builds the [repair] line for a result, or "" when there is
// nothing to say: not a check, or a pass that is not a fix.
func repairLine(history []llm.Message, call llm.ToolCall, text string) string {
	s, ok := ClassifyFailure(call, text)
	if !ok {
		return ""
	}
	note := repairNote{Category: s.Category, Check: s.Check, Env: s.Env, Advice: failureAdvice(s)}
	note.Tests = s.Tests
	if len(note.Tests) > 10 {
		note.Tests = append(slices.Clone(note.Tests[:10]), fmt.Sprintf("... and %d more", len(s.Tests)-10))
	}
	if prev, ok := previousSignature(history, call, s); ok {
		d := CompareFailures(prev, s)
		note.Delta = &d
	}
	if s.Category == FailurePassed && (note.Delta == nil || note.Delta.Kind != DeltaFixed) {
		return ""
	}
	raw, err := json.Marshal(note)
	if err != nil {
		return ""
	}
	return repairMarker + " " + string(raw)
}

// previousSignature finds the check to compare s with: the last earlier
// check of the same call, or else the last one that failed in any of the
// same tests. It only counts when files were edited after that check, so a
// plain rerun has no delta.
func previousSignature(history []llm.Message, call llm.ToolCall, s Signature) (Signature, bool) {
	key := callKey(call)
	calls := map[string]llm.ToolCall{}
	var prev Signature
	found, editedSince := false, false
	for _, m := range history {
		if rt.IsUserTurn(m) {
			found, editedSince = false, false
		}
		for _, c := range m.ToolCalls {
			calls[c.ID] = c
		}
		if m.Role != llm.RoleTool {
			continue
		}
		c, ok := calls[m.ToolCallID]
		if !ok {
			continue
		}
		if mutatingTools[c.Name] && m.Status != llm.ToolStatusError && !strings.HasPrefix(m.Text(), "Error") {
			editedSince = true
			continue
		}
		p, ok := ClassifyFailure(c, m.Text())
		if !ok {
			continue
		}
		if callKey(c) == key || len(missing(s.Tests, p.Tests)) < len(s.Tests) || len(missing(p.Tests, s.Tests)) < len(p.Tests) {
			prev, found, editedSince = p, true, false
		}
	}
	return prev, found && editedSince
}

// failureAdvice tells the model what the category means for its next step.
func failureAdvice(s Signature) string {
	switch s.Category {
	case FailureEnvironment:
		return environmentAdvice(s.Env)
	case FailureToolError:
		return "The check itself failed to run: fix the command, its arguments or the test setup, not the application code."
	case FailureTimeout:
		return "The check timed out: narrow it (fewer tests or a name filter) or raise the timeout; suspect a hang in the code only if a narrow run also times out."
	case FailureSandbox:
		return "The sandbox blocked the check; see the [sandbox] note before changing code."
	case FailureNoTests:
		return "No tests ran: fix the filter or the path, then rerun."
	case FailureSyntax, FailureCompile:
		return "The code does not build, so no test ran: fix these errors first."
	}
	return ""
}

// environmentAdvice tells the model what it can do about an environment
// problem: what is fixable from here first, reporting it last.
func environmentAdvice(env string) string {
	kind, _, _ := strings.Cut(env, ":")
	switch kind {
	case "network":
		return "The code was not exercised: the check needed the network and could not reach it. Editing the code will not fix this. " +
			"Restore access if you can (commands may run in a sandbox whose network setting blocks it; ask the user), or run checks that need no download. " +
			"If neither is possible, say in your final answer which changes are unverified."
	case "dependency":
		return "The code was not exercised (" + env + "): the module setup lacks a dependency. Run `go mod tidy` (or `go get` the module) and rerun; " +
			"that changes go.mod / go.sum, not the application code."
	}
	return "The code was not exercised (" + env + "): fix the environment (start the service, install the dependency or tool) and rerun. Change the environment, not the application code."
}
