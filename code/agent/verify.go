package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	core "github.com/tsengmingchun/deepagents_golang/agent"
	"github.com/tsengmingchun/deepagents_golang/backend"
	"github.com/tsengmingchun/deepagents_golang/code/codetools"
	"github.com/tsengmingchun/deepagents_golang/llm"
	rt "github.com/tsengmingchun/deepagents_golang/runtime"
	"github.com/tsengmingchun/deepagents_golang/tool"
)

const (
	// VerifyGateName is the middleware name of the verification gate.
	VerifyGateName = "verify_gate"
	// verifyMarker starts every message the gate adds, so the gate can count
	// its own blocks from the transcript (and after a checkpoint restore).
	verifyMarker = "[verify]"
	// maxVerifyBlocks bounds how often one turn can be sent back to the model
	// for its checks; the diff review adds at most one more.
	maxVerifyBlocks = 2
	executeToolName = "execute"
)

var (
	mutatingTools = map[string]bool{"write_file": true, "edit_file": true, codetools.ApplyPatchName: true}

	// checkCommand matches shell commands that run tests or a build.
	checkCommand = regexp.MustCompile(`(?:^|[\s;&|(/])(?:go\s+(?:test|build|vet)|(?:npm|yarn|pnpm)\s+(?:run\s+)?test|npx\s+(?:mocha|jest|vitest|tsc)|mocha|jest|vitest|tsc|pytest|python3?\s+-m\s+(?:pytest|unittest)|make\s+(?:test|check)|cargo\s+(?:test|build)|tox|nox)\b`)
	// execExit is the status line the filesystem execute tool appends.
	execExit = regexp.MustCompile(`\[Command (?:succeeded|failed) with exit code (-?\d+)\]\s*$`)
	// noTestsRan means a runner exited cleanly without running anything
	// (for example a --grep that matched nothing).
	noTestsRan    = regexp.MustCompile(`(?m)(?:^\s*0 passing\b|no tests ran|collected 0 items|Tests:\s+0 total|No test files found)`)
	patchFileLine = regexp.MustCompile(`(?m)^\*\*\* (?:Add|Update|Delete) File: (.+)$`)
	// testFilter matches test-name filters: mocha --grep / -g / --fgrep, jest
	// and vitest -t / --testNamePattern, go test -run, pytest -k, and pytest
	// node IDs (file.py::test).
	testFilter = regexp.MustCompile(`(?:^|\s)(?:(?:--grep|-g|--fgrep|-t|--testNamePattern|--test-name-pattern|-run|-k)(?:=|\s+)(?:"[^"]*"|'[^']*'|\S+)|\S+\.py::\S+)`)

	refusedMark = regexp.MustCompile(`ECONNREFUSED|[Cc]onnection refused`)
	refusedPort = regexp.MustCompile(`(?:localhost|127\.0\.0\.1|0\.0\.0\.0|::1|\[::1\]):(\d{2,5})\b`)
)

// knownServices maps a port to the service usually behind it and a command
// that starts it inside a test container.
var knownServices = map[string][2]string{
	"6379":  {"Redis", "redis-server --daemonize yes --save '' --dir /tmp"},
	"27017": {"MongoDB", "mkdir -p /tmp/mongo && mongod --fork --dbpath /tmp/mongo --logpath /tmp/mongod.log"},
	"5432":  {"PostgreSQL", "service postgresql start (or pg_ctl -D <datadir> start)"},
	"3306":  {"MySQL", "service mysql start (or mysqld_safe &)"},
}

// VerifyGate keeps a headless run from finishing right after it changed files
// without a passing test or build. At a natural stop it looks at the current
// turn; if the last check after the last edit is missing or failed it adds a
// message and sends the loop back to the model, at most maxVerifyBlocks times.
// It also appends start-up hints to command output that shows a refused
// connection to a local service.
//
// With a Workspace it also compares a failing check against HEAD (see
// Baseline), letting the turn finish when every failure is pre-existing or
// flaky, and reviews the diff once before the turn finishes (see
// reviewDiff).
type VerifyGate struct {
	Workspace backend.SandboxBackend
	// RunTests reruns run_tests checks for the baseline.
	RunTests tool.Tool
	// NoBaseline, NoDiffReview and NoTestEdits turn those checks off.
	NoBaseline, NoDiffReview, NoTestEdits bool
	// OnBaseline, when set, receives every baseline verdict, including the
	// ones that let the turn finish without a message.
	OnBaseline func(verdict string, tests []string)
}

func (VerifyGate) Name() string { return VerifyGateName }

// WrapToolCall adds a service hint to execute / run_tests results that show a
// refused connection.
func (VerifyGate) WrapToolCall(ctx context.Context, req *core.ToolCallRequest, next core.ToolHandler) (*tool.Result, error) {
	res, err := next(ctx, req)
	if err != nil || res == nil {
		return res, err
	}
	if name := req.ToolCall.Name; name != executeToolName && name != codetools.RunTestsName {
		return res, nil
	}
	var text strings.Builder
	for _, c := range res.Content {
		if c.Type == llm.BlockText {
			text.WriteString(c.Text)
		}
	}
	if hint := serviceHint(text.String()); hint != "" {
		res.Content = append(slices.Clone(res.Content), llm.TextBlock("\n\n"+hint))
	}
	return res, nil
}

// serviceHint explains a refused connection to a local port, or returns "".
func serviceHint(out string) string {
	if !refusedMark.MatchString(out) {
		return ""
	}
	var ports []string
	for _, m := range refusedPort.FindAllStringSubmatch(out, -1) {
		if !slices.Contains(ports, m[1]) {
			ports = append(ports, m[1])
		}
	}
	if len(ports) == 0 {
		return "[hint] A connection was refused: a service the code needs is probably not running. " +
			"Start it inside this environment and rerun; the Dockerfile, CI config (.github/workflows) or package.json scripts show how tests expect it to run."
	}
	var b strings.Builder
	for _, p := range ports {
		if s, ok := knownServices[p]; ok {
			fmt.Fprintf(&b, "[hint] Connection refused on port %s (%s): the service is not running. Start it, e.g. `%s`, then rerun.\n", p, s[0], s[1])
		} else {
			fmt.Fprintf(&b, "[hint] Connection refused on port %s: a service the code needs is not running. Start it, then rerun.\n", p)
		}
	}
	b.WriteString("[hint] The Dockerfile, CI config (.github/workflows) or package.json scripts show how tests expect services to run.")
	return b.String()
}

// Kinds of gate message, kept in their metadata under GateKindKey.
const (
	GateKindKey      = "verify_kind"
	gateKindCheck    = "check"
	gateKindBaseline = "baseline"
	gateKindDiff     = "diff"
)

// AfterAgent sends the loop back to the model when the turn edited files but
// has no passing check after the last edit, or once when the diff needs a
// review.
func (g VerifyGate) AfterAgent(ctx context.Context, state *rt.State, _ *core.Runtime) (*rt.Command, error) {
	t := inspectTurn(state.Messages)
	if t.lastEdit < 0 {
		return nil, nil
	}
	if !t.verified() {
		if t.blocks >= maxVerifyBlocks {
			return nil, nil
		}
		kind, msg := gateKindCheck, checkMessage(t)
		if g.Workspace != nil && !g.NoBaseline && t.last != nil && !t.last.passed && !t.kinds[gateKindBaseline] {
			b := Baseline{Workspace: g.Workspace, RunTests: g.RunTests}
			v := b.Compare(ctx, t.lastCall, t.last)
			if g.OnBaseline != nil {
				g.OnBaseline(v.kind.String(), v.tests)
			}
			switch v.kind {
			case verdictPreexisting, verdictFlaky:
				return nil, nil
			case verdictRegression:
				kind, msg = gateKindBaseline, baselineMessage(v, t.last)
			default:
				kind, msg = gateKindBaseline, msg+baselineMessage(v, t.last)
			}
		}
		return gateCommand(kind, msg), nil
	}
	if g.Workspace != nil && !g.NoDiffReview && !t.kinds[gateKindDiff] {
		if msg := reviewDiff(ctx, g.Workspace, t.prompt); msg != "" {
			return gateCommand(gateKindDiff, msg), nil
		}
	}
	return nil, nil
}

func gateCommand(kind, text string) *rt.Command {
	m := llm.NewUserMessage(text)
	m.Metadata = map[string]any{rt.SyntheticSourceKey: VerifyGateName, GateKindKey: kind}
	return &rt.Command{Update: &rt.Update{Messages: []llm.Message{m}}, Goto: core.GotoModel}
}

// checkResult is the outcome of one test or build run.
type checkResult struct {
	command  string
	passed   bool
	detail   string
	failures []string
	// filter is the test-name filter the run used ("" = whole files), and
	// files the test files it was limited to.
	filter string
	files  []string
	// failedTests names every failing test run_tests reported.
	failedTests []string
}

// turnFacts is what the gate reads from the current turn.
type turnFacts struct {
	// prompt is the user message that started the turn.
	prompt string
	blocks int
	// kinds counts the gate's earlier messages in the turn by kind.
	kinds    map[string]bool
	lastEdit int
	files    []string
	// last is the last check after the last edit, run by lastCall.
	last     *checkResult
	lastCall llm.ToolCall
	hints    []string
}

// verified reports a passing whole-file check after the last edit.
func (t turnFacts) verified() bool {
	return t.last != nil && t.last.passed && t.last.filter == ""
}

// verifyMessage decides whether the current turn must continue and builds the
// message that tells the model why.
func verifyMessage(msgs []llm.Message) (string, bool) {
	t := inspectTurn(msgs)
	if t.blocks >= maxVerifyBlocks || t.lastEdit < 0 || t.verified() {
		return "", false
	}
	return checkMessage(t), true
}

func inspectTurn(msgs []llm.Message) turnFacts {
	start, blocks := turnStart(msgs)
	t := turnFacts{blocks: blocks, kinds: map[string]bool{}, lastEdit: -1}
	if start < len(msgs) && msgs[start].Role == llm.RoleUser {
		t.prompt = msgs[start].Text()
	}
	calls := map[string]llm.ToolCall{}
	for i := start; i < len(msgs); i++ {
		m := msgs[i]
		if m.Role == llm.RoleUser && i > start {
			if kind, _ := m.Metadata[GateKindKey].(string); kind != "" {
				t.kinds[kind] = true
				if kind == gateKindDiff {
					t.blocks--
				}
			}
		}
		for _, c := range m.ToolCalls {
			calls[c.ID] = c
		}
		if m.Role != llm.RoleTool {
			continue
		}
		call, ok := calls[m.ToolCallID]
		if !ok {
			continue
		}
		text := m.Text()
		if mutatingTools[call.Name] {
			if m.Status != llm.ToolStatusError && !strings.HasPrefix(text, "Error") {
				t.lastEdit, t.last = i, nil
				for _, f := range editedFiles(call) {
					if !slices.Contains(t.files, f) {
						t.files = append(t.files, f)
					}
				}
			}
			continue
		}
		if h := serviceHint(text); h != "" && !slices.Contains(t.hints, h) {
			t.hints = append(t.hints, h)
		}
		if t.lastEdit < 0 {
			continue
		}
		if r := classifyCheck(call, text); r != nil {
			t.last, t.lastCall = r, call
		}
	}
	return t
}

// checkMessage tells the model why the turn has no valid check yet.
func checkMessage(t turnFacts) string {
	last := t.last
	var b strings.Builder
	b.WriteString(verifyMarker + " ")
	switch {
	case last != nil && last.passed:
		fmt.Fprintf(&b, "The last passing check after your changes only ran tests matching `%s` (`%s`). "+
			"Other tests in the same files can break without showing up there. Run %s again without a filter "+
			"(run_tests with `files` and an empty `run`, or the same command without the filter) and fix what fails before finishing.",
			clipLine(last.filter, 200), clipLine(last.command, 200), testFileList(last.files))
	case last == nil:
		fmt.Fprintf(&b, "You changed %s but have not run the tests or a build since the last change. "+
			"Run the relevant tests (run_tests, or the project's own command through execute) and fix what fails before finishing.",
			fileList(t.files))
	default:
		fmt.Fprintf(&b, "The last check after your changes did not pass: `%s` %s.", clipLine(last.command, 200), last.detail)
		if len(last.failures) > 0 {
			b.WriteString("\nFailing:")
			for _, f := range last.failures {
				b.WriteString("\n- " + f)
			}
		}
		b.WriteString("\nFix the cause and rerun the check before finishing.")
	}
	b.WriteString("\nIf the tests cannot start because of the environment (a missing service, dependency or database), set the environment up instead of giving up.")
	for _, h := range t.hints {
		b.WriteString("\n" + h)
	}
	if t.blocks+1 == maxVerifyBlocks {
		b.WriteString("\nThis is the last reminder: if you still cannot get a passing check, say plainly in your final answer that the change is unverified or which checks still fail.")
	}
	return b.String()
}

// turnStart returns the index of the last user message that the gate did not
// write, and how many gate messages follow it.
func turnStart(msgs []llm.Message) (int, int) {
	blocks := 0
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role != llm.RoleUser {
			continue
		}
		if strings.HasPrefix(msgs[i].Text(), verifyMarker) {
			blocks++
			continue
		}
		return i, blocks
	}
	return 0, blocks
}

// classifyCheck reads a tool result as a test / build run, or returns nil when
// the call was not one (or gave no usable signal).
func classifyCheck(call llm.ToolCall, text string) *checkResult {
	switch call.Name {
	case codetools.RunTestsName:
		var rep codetools.TestReport
		if err := json.Unmarshal([]byte(text), &rep); err != nil || rep.Status == "" || rep.Status == "unsupported" {
			return nil
		}
		r := &checkResult{command: "run_tests", passed: rep.Status == "passed", files: stringArgs(call.Args["files"])}
		if run, _ := call.Args["run"].(string); strings.TrimSpace(run) != "" {
			r.filter, r.command = run, fmt.Sprintf("run_tests run=%q", run)
		}
		// The npm fallback reports no counts, only the exit code.
		if rep.Status == "no_tests" || rep.Status == "passed" && rep.Passed == 0 && rep.Framework != "npm" {
			r.passed, r.detail = false, "ran no tests"
			return r
		}
		r.detail = fmt.Sprintf("reported %s (%d passed, %d failed)", rep.Status, rep.Passed, rep.Failed)
		for _, f := range rep.Failures {
			if name := testName(f); name != "" && !slices.Contains(r.failedTests, name) {
				r.failedTests = append(r.failedTests, name)
			}
		}
		for i, f := range rep.Failures {
			if i == 5 {
				r.failures = append(r.failures, fmt.Sprintf("... and %d more", len(rep.Failures)-5))
				break
			}
			r.failures = append(r.failures, failureLine(f))
		}
		for i, d := range rep.BuildErrors {
			if i == 3 {
				break
			}
			r.failures = append(r.failures, fmt.Sprintf("%s:%d: %s", d.File, d.Line, clipLine(d.Message, 200)))
		}
		return r
	case executeToolName:
		cmd, _ := call.Args["command"].(string)
		if !checkCommand.MatchString(cmd) {
			return nil
		}
		m := execExit.FindStringSubmatch(text)
		if m == nil {
			return nil
		}
		code, _ := strconv.Atoi(m[1])
		r := &checkResult{command: cmd, passed: code == 0, detail: fmt.Sprintf("exited with code %d", code),
			filter: strings.TrimSpace(testFilter.FindString(cmd))}
		if r.passed && noTestsRan.MatchString(text) {
			r.passed, r.detail = false, "ran no tests"
		}
		return r
	}
	return nil
}

// testName identifies a failing test across runs.
func testName(f codetools.TestFailure) string {
	switch {
	case f.Test != "" && f.File != "":
		return f.File + " | " + f.Test
	case f.Test != "":
		return f.Test
	}
	return f.Package
}

func failureLine(f codetools.TestFailure) string {
	name := f.Test
	if name == "" {
		name = f.Package
	}
	loc := ""
	if f.File != "" {
		loc = fmt.Sprintf(" (%s:%d)", f.File, f.Line)
	}
	msg := strings.TrimSpace(f.Message)
	if i := strings.IndexByte(msg, '\n'); i >= 0 {
		msg = msg[:i]
	}
	return clipLine(name+loc+": "+msg, 300)
}

// editedFiles lists the paths a successful mutating call touched.
func editedFiles(call llm.ToolCall) []string {
	if call.Name == codetools.ApplyPatchName {
		patch, _ := call.Args["patch"].(string)
		var out []string
		for _, m := range patchFileLine.FindAllStringSubmatch(patch, -1) {
			out = append(out, strings.TrimSpace(m[1]))
		}
		return out
	}
	if p, _ := call.Args["file_path"].(string); p != "" {
		return []string{p}
	}
	return nil
}

func fileList(files []string) string {
	switch {
	case len(files) == 0:
		return "files"
	case len(files) <= 5:
		return "`" + strings.Join(files, "`, `") + "`"
	default:
		return fmt.Sprintf("`%s` and %d more files", strings.Join(files[:5], "`, `"), len(files)-5)
	}
}

func testFileList(files []string) string {
	if len(files) == 0 {
		return "the same tests"
	}
	return fileList(files)
}

// stringArgs reads a tool argument holding a list of strings.
func stringArgs(v any) []string {
	var out []string
	switch xs := v.(type) {
	case []string:
		out = append(out, xs...)
	case []any:
		for _, x := range xs {
			if s, ok := x.(string); ok && s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}

func clipLine(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
