package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"path"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	core "github.com/tsengmingchun/quill/agent"
	"github.com/tsengmingchun/quill/backend"
	"github.com/tsengmingchun/quill/code/codetools"
	"github.com/tsengmingchun/quill/llm"
	rt "github.com/tsengmingchun/quill/runtime"
	"github.com/tsengmingchun/quill/tool"
)

// VerifyLevel is how far verification of the current changes has widened.
type VerifyLevel int

const (
	// LevelNone: no passing test since the last edit.
	LevelNone VerifyLevel = iota
	// LevelTargeted: some test passes, possibly filtered to a few cases.
	LevelTargeted
	// LevelPackage: the tests of the changed files pass.
	LevelPackage
	// LevelRelated: the tests of code depending on them pass too.
	LevelRelated
	// LevelBuildLint: the project also builds and lints cleanly.
	LevelBuildLint
)

func (l VerifyLevel) String() string {
	return [...]string{"none", "targeted", "package", "related", "build_lint"}[l]
}

const (
	gateKindWiden        = "widen"
	defaultPlanTimeout   = 30 * time.Second
	verifyLevelSequence  = "targeted → package → related → build_lint"
	reproMarker          = "[repro]"
	reproReminderMessage = reproMarker + " You edited code before any check in this turn failed. If this fixes a bug, reproduce it first: " +
		"run the failing test, or write a small test or script that fails, so you can tell whether the fix works. " +
		"If there is nothing to reproduce (a new feature or a refactor), record that with repair set_task kind=feature or kind=refactor."
)

var (
	// lintCommand matches shell commands that build, type-check or lint.
	lintCommand = regexp.MustCompile(`(?:^|[\s;&|(/])(?:go\s+(?:build|vet)|tsc|eslint|ruff|flake8|mypy|pyright)\b`)
	// testRunner matches shell commands that run tests.
	testRunner = regexp.MustCompile(`(?:^|[\s;&|(/])(?:go\s+test|(?:npm|yarn|pnpm)\s+(?:run\s+)?test|mocha|jest|vitest|pytest|python3?\s+-m\s+(?:pytest|unittest)|make\s+(?:test|check)|cargo\s+test|tox|nox)\b`)
)

// PlannedCheck is one check of a verification plan.
type PlannedCheck struct {
	Level VerifyLevel
	Tool  string
	Args  map[string]any
	Why   string
}

// describe renders the call to make, e.g. run_tests {"packages":["./b"]}.
func (c PlannedCheck) describe() string {
	args := maps.Clone(c.Args)
	delete(args, "language")
	raw, _ := json.Marshal(args)
	return c.Tool + " " + string(raw)
}

// VerificationPlan lists the checks for the current changes, cheapest
// first: the changed files' tests, the tests of their dependents, then
// build and lint.
type VerificationPlan struct {
	Checks []PlannedCheck
}

// Planner plans the verification of a change to files (paths relative to
// the workspace root).
type Planner interface {
	Plan(ctx context.Context, files []string) VerificationPlan
}

// VerificationPlanner finds the related tests with codetools.FindRelated
// and caches the plan per set of changed files.
type VerificationPlanner struct {
	Workspace backend.SandboxBackend
	Timeout   time.Duration

	mu    sync.Mutex
	cache map[string]VerificationPlan
}

// NewVerificationPlanner plans with the commands of sb.
func NewVerificationPlanner(sb backend.SandboxBackend) *VerificationPlanner {
	return &VerificationPlanner{Workspace: sb}
}

func (p *VerificationPlanner) Plan(ctx context.Context, files []string) VerificationPlan {
	files = codeFiles(files)
	if len(files) == 0 {
		return VerificationPlan{}
	}
	key := strings.Join(files, "\n")
	p.mu.Lock()
	plan, ok := p.cache[key]
	p.mu.Unlock()
	if ok {
		return plan
	}
	timeout := p.Timeout
	if timeout <= 0 {
		timeout = defaultPlanTimeout
	}
	if r, err := codetools.FindRelated(ctx, p.Workspace, files, timeout); err == nil {
		plan = planFromRelated(r)
	}
	p.mu.Lock()
	if p.cache == nil {
		p.cache = map[string]VerificationPlan{}
	}
	p.cache[key] = plan
	p.mu.Unlock()
	return plan
}

// codeFiles keeps the source files among paths, relative and sorted.
func codeFiles(paths []string) []string {
	var out []string
	for _, p := range paths {
		p = strings.TrimPrefix(path.Clean("/"+p), "/")
		if checkedExts[path.Ext(p)] && !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	slices.Sort(out)
	return out
}

func planFromRelated(r codetools.Related) VerificationPlan {
	if r.Language == "" {
		return VerificationPlan{}
	}
	base := map[string]any{"language": r.Language}
	if r.Dir != "" {
		base["dir"] = r.Dir
	}
	with := func(key string, values []string) map[string]any {
		args := maps.Clone(base)
		if values != nil {
			args[key] = toAny(values)
		}
		return args
	}
	var plan VerificationPlan
	add := func(level VerifyLevel, tool string, args map[string]any, why string) {
		plan.Checks = append(plan.Checks, PlannedCheck{Level: level, Tool: tool, Args: args, Why: why})
	}
	if r.Language == "go" {
		if len(r.Packages) > 0 {
			add(LevelPackage, codetools.RunTestsName, with("packages", r.Packages), "packages of the changed files")
		}
		if len(r.Dependents) > 0 {
			add(LevelRelated, codetools.RunTestsName, with("packages", r.Dependents), "packages that import the changed packages")
		}
		add(LevelBuildLint, codetools.DiagnosticsName, with("packages", []string{"./..."}), "build and vet the module")
		return plan
	}
	if len(r.Tests) > 0 {
		add(LevelPackage, codetools.RunTestsName, with("files", r.Tests), "tests of the changed files")
	}
	if len(r.Importers) > 0 {
		add(LevelRelated, codetools.RunTestsName, with("files", r.Importers), "other tests that import the changed modules")
	}
	add(LevelBuildLint, codetools.DiagnosticsName, maps.Clone(base), "type-check and lint the project")
	return plan
}

// verificationReached is the level the checks since the last edit of the
// current turn reach for plan.
func verificationReached(msgs []llm.Message, plan VerificationPlan) VerifyLevel {
	reached, _ := verificationProgress(msgs, plan)
	return reached
}

// verificationProgress returns the level reached and the planned checks no
// passing check covers yet, in plan order.
func verificationProgress(msgs []llm.Message, plan VerificationPlan) (VerifyLevel, []PlannedCheck) {
	start := 0
	for i := len(msgs) - 1; i >= 0; i-- {
		if rt.IsUserTurn(msgs[i]) {
			start = i
			break
		}
	}
	calls := map[string]llm.ToolCall{}
	lastEdit := -1
	var passing []llm.ToolCall
	targeted := false
	for i := start; i < len(msgs); i++ {
		m := msgs[i]
		for _, c := range m.ToolCalls {
			calls[c.ID] = c
		}
		c, ok := calls[m.ToolCallID]
		if m.Role != llm.RoleTool || !ok {
			continue
		}
		text := m.Text()
		failed := m.Status == llm.ToolStatusError || strings.HasPrefix(text, "Error")
		if mutatingTools[c.Name] {
			if !failed {
				lastEdit, passing, targeted = i, nil, false
			}
			continue
		}
		if lastEdit < 0 {
			continue
		}
		if c.Name == codetools.DiagnosticsName {
			if s, ok := ClassifyFailure(c, text); ok && s.Category == FailurePassed {
				passing = append(passing, c)
			}
			continue
		}
		if c.Name == executeToolName && lintCommand.MatchString(stringArg(c, "command")) && !testRunner.MatchString(stringArg(c, "command")) {
			if r := classifyCheck(c, text); r != nil && r.passed {
				passing = append(passing, c)
			}
			continue
		}
		if r := classifyCheck(c, text); r != nil && r.passed {
			targeted = true
			if r.filter == "" {
				passing = append(passing, c)
			}
		}
	}
	var pending []PlannedCheck
	for _, pc := range plan.Checks {
		if !slices.ContainsFunc(passing, func(c llm.ToolCall) bool { return covers(c, pc) }) {
			pending = append(pending, pc)
		}
	}
	if !targeted {
		return LevelNone, pending
	}
	reached := LevelTargeted
	for _, level := range []VerifyLevel{LevelPackage, LevelRelated, LevelBuildLint} {
		if slices.ContainsFunc(pending, func(pc PlannedCheck) bool { return pc.Level <= level }) {
			break
		}
		reached = level
	}
	return reached, pending
}

func stringArg(c llm.ToolCall, key string) string {
	s, _ := c.Args[key].(string)
	return s
}

// covers reports whether a passing call ran at least what pc asks for.
func covers(c llm.ToolCall, pc PlannedCheck) bool {
	d, _ := pc.Args["dir"].(string)
	wantDir := cleanDir(d)
	switch pc.Tool {
	case codetools.RunTestsName:
		switch c.Name {
		case codetools.RunTestsName:
			return cleanDir(stringArg(c, "dir")) == wantDir &&
				coversPaths(stringArgs(c.Args["packages"]), stringArgs(c.Args["files"]), pc)
		case executeToolName:
			dir, args := shellTestArgs(stringArg(c, "command"))
			if dir != wantDir {
				return false
			}
			if _, isGo := pc.Args["packages"]; isGo {
				return coversPaths(args, nil, pc)
			}
			return coversPaths(nil, args, pc)
		}
	case codetools.DiagnosticsName:
		switch c.Name {
		case codetools.DiagnosticsName:
			pkgs := stringArgs(c.Args["packages"])
			return cleanDir(stringArg(c, "dir")) == wantDir && len(stringArgs(c.Args["files"])) == 0 &&
				(len(pkgs) == 0 || slices.Contains(pkgs, "./..."))
		case executeToolName:
			cmd := stringArg(c, "command")
			dir, _ := shellTestArgs(cmd)
			return dir == wantDir && lintCommand.MatchString(cmd)
		}
	}
	return false
}

func cleanDir(d string) string {
	d = strings.Trim(path.Clean("/"+d), "/")
	if d == "." {
		return ""
	}
	return d
}

// shellTestArgs reads "cd dir && <runner> args..." into the directory and
// the path-like arguments.
func shellTestArgs(cmd string) (string, []string) {
	fields := strings.Fields(cmd)
	dir := ""
	if len(fields) >= 3 && fields[0] == "cd" && (fields[2] == "&&" || fields[2] == ";") {
		dir, fields = cleanDir(fields[1]), fields[3:]
	}
	var paths []string
	for _, f := range fields {
		if strings.HasPrefix(f, "-") {
			continue
		}
		if f == "." || strings.HasPrefix(f, "./") || strings.Contains(f, "/") || testFileExt.MatchString(f) {
			paths = append(paths, f)
		}
	}
	return dir, paths
}

var testFileExt = regexp.MustCompile(`\.(?:[cm]?[jt]sx?|py)$`)

// coversPaths reports whether running packages / files (none = the whole
// project) includes every package or file pc lists.
func coversPaths(pkgs, files []string, pc PlannedCheck) bool {
	if len(pkgs) == 0 && len(files) == 0 {
		return true
	}
	for _, want := range stringArgs(pc.Args["packages"]) {
		if !slices.ContainsFunc(pkgs, func(have string) bool { return goPackageCovers(have, want) }) {
			return false
		}
	}
	for _, want := range stringArgs(pc.Args["files"]) {
		want = path.Clean(want)
		if !slices.ContainsFunc(files, func(have string) bool {
			have = path.Clean(have)
			return have == "." || have == want || strings.HasPrefix(want, have+"/")
		}) {
			return false
		}
	}
	return true
}

func goPackageCovers(have, want string) bool {
	norm := func(p string) string {
		p = strings.TrimPrefix(path.Clean(p), "./")
		if p == "" {
			return "."
		}
		return p
	}
	if strings.HasSuffix(have, "/...") || have == "..." {
		root := norm(strings.TrimSuffix(strings.TrimSuffix(have, "..."), "/"))
		w := norm(want)
		return root == "." || w == root || strings.HasPrefix(w, root+"/")
	}
	return norm(have) == norm(want)
}

// widenMessage asks to run the planned checks that are still missing when
// the changes are verified below LevelRelated, or returns "".
func widenMessage(ctx context.Context, p Planner, msgs []llm.Message, files []string) string {
	plan := p.Plan(ctx, codeFiles(files))
	reached, pending := verificationProgress(msgs, plan)
	if reached >= LevelRelated || !slices.ContainsFunc(pending, func(pc PlannedCheck) bool { return pc.Level <= LevelRelated }) {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s Your checks pass, but verification only reached: %s (%s). Widen it before finishing:", verifyMarker, reached, verifyLevelSequence)
	for i, pc := range pending {
		label := "then"
		if i == 0 {
			label = "next"
		}
		fmt.Fprintf(&b, "\n%s: %s (%s)", label, pc.describe(), pc.Why)
	}
	b.WriteString("\nFix what fails. If one of these cannot run here, say so in your final answer.")
	return b.String()
}

// renderVerification is the verification part of <repair-state>.
func renderVerification(b *strings.Builder, plan VerificationPlan, reached VerifyLevel, pending []PlannedCheck) {
	if len(plan.Checks) == 0 {
		return
	}
	b.WriteString("verification:\n")
	fmt.Fprintf(b, "  reached: %s\n", reached)
	b.WriteString("  plan:\n")
	for _, pc := range plan.Checks {
		args := maps.Clone(pc.Args)
		delete(args, "language")
		raw, _ := json.Marshal(args)
		why, _ := json.Marshal(pc.Why)
		done := !slices.ContainsFunc(pending, func(p PlannedCheck) bool {
			return p.Level == pc.Level && p.Tool == pc.Tool && p.describe() == pc.describe()
		})
		fmt.Fprintf(b, "    - {level: %s, tool: %s, args: %s, why: %s, done: %t}\n", pc.Level, pc.Tool, raw, why, done)
	}
}

// ReproReminder notes once per turn, on the first successful edit of a
// code file, that nothing has reproduced the problem yet: no check in the
// turn failed in the code (environment failures do not count) and the
// task was not recorded as a feature or a refactor.
type ReproReminder struct{}

func (ReproReminder) Name() string { return "ReproReminderMiddleware" }

func (ReproReminder) WrapToolCall(ctx context.Context, req *core.ToolCallRequest, next core.ToolHandler) (*tool.Result, error) {
	res, err := next(ctx, req)
	if err != nil || res == nil || res.IsError || !mutatingTools[req.ToolCall.Name] || strings.HasPrefix(resultText(res), "Error") {
		return res, err
	}
	if len(codeFiles(editedFiles(req.ToolCall))) == 0 || req.State == nil {
		return res, nil
	}
	if rec, err := parseRepairRecord(req.State.Values[RepairStateKey]); err == nil && rec.Task != nil && rec.Task.Kind != "" && rec.Task.Kind != "bug" {
		return res, nil
	}
	if reproducedOrEdited(req.State.Messages) {
		return res, nil
	}
	out := *res
	out.Content = append(slices.Clone(res.Content), llm.TextBlock("\n\n"+reproReminderMessage))
	return &out, nil
}

// reproducedOrEdited reports whether the current turn already has a
// successful edit or a check that failed in the code.
func reproducedOrEdited(msgs []llm.Message) bool {
	start := 0
	for i := len(msgs) - 1; i >= 0; i-- {
		if rt.IsUserTurn(msgs[i]) {
			start = i
			break
		}
	}
	calls := map[string]llm.ToolCall{}
	for _, m := range msgs[start:] {
		for _, c := range m.ToolCalls {
			calls[c.ID] = c
		}
		c, ok := calls[m.ToolCallID]
		if m.Role != llm.RoleTool || !ok {
			continue
		}
		text := m.Text()
		if mutatingTools[c.Name] {
			if m.Status != llm.ToolStatusError && !strings.HasPrefix(text, "Error") {
				return true
			}
			continue
		}
		if s, ok := ClassifyFailure(c, text); ok && s.Category != FailurePassed {
			if _, inCode := s.Category.rank(); inCode {
				return true
			}
		}
	}
	return false
}
