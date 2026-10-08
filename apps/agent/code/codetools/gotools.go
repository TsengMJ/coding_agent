package codetools

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/tsengmingchun/quill/apps/agent/backend"
	"github.com/tsengmingchun/quill/apps/agent/tool"
)

const (
	defaultGoTimeout = 300
	maxGoTimeout     = 1800
	maxRunPattern    = 500

	noGoMarker   = "__QUILL_NO_GO__"
	noModMarker  = "__QUILL_NO_GO_MODULE__"
	exitMarker   = "__QUILL_EXIT__"
	maxRawOutput = 4000
)

// goModuleCheck stops early with a marker when go or a module is missing.
const goModuleCheck = `command -v go >/dev/null 2>&1 || { echo ` + noGoMarker + `; exit 0; }; ` +
	`gomod=$(go env GOMOD 2>/dev/null); if [ -z "$gomod" ] || [ "$gomod" = /dev/null ]; then ` +
	`gowork=$(go env GOWORK 2>/dev/null); if [ -z "$gowork" ] || [ "$gowork" = off ]; then echo ` + noModMarker + `; exit 0; fi; fi; `

// goTestFilter runs `go test -json` into a temp file and prints only what the
// parser needs, so large suites fit the backend's output limit: result
// events without timestamps, build output, non-JSON lines, and the output of
// failed tests and failed packages.
const goTestFilter = `tmp=$(mktemp 2>/dev/null || echo /tmp/quill-gotest-$$); ` +
	`go test -json %s > "$tmp" 2>&1; code=$?; ` +
	`grep -E '"Action":"(pass|fail|skip|build-output|build-fail)"' "$tmp" | sed -E 's/"Time":"[^"]*",//; s/,"Elapsed":[-0-9.e+]+//'; ` +
	`{ grep '"Action":"fail"' "$tmp" | sed -n -E 's/.*("Package":"[^"]*","Test":"[^"]*").*/\1/p'; ` +
	`grep '"Action":"fail"' "$tmp" | grep -v '"Test":"' | sed -n -E 's/.*("Package":"[^"]*").*/\1,"Output"/p'; } | sort -u > "$tmp.keys"; ` +
	`if [ -s "$tmp.keys" ]; then grep '"Action":"output"' "$tmp" | grep -F -f "$tmp.keys" | ` +
	`grep -v -E '"Output":"(=== (RUN|PAUSE|CONT|NAME)|--- PASS)' | sed -E 's/"Time":"[^"]*",//'; fi; ` +
	`grep -v '^{' "$tmp"; ` +
	`rm -f "$tmp" "$tmp.keys"; echo ` + exitMarker + `$code`

const runTestsDescription = `Run the project's tests and get a structured summary instead of raw runner output:
{"status": "passed"|"failed"|"error"|"no_tests", "framework", "passed", "failed", "skipped", "failures": [{"package", "test", "file", "line", "message"}], "build_errors": [{"file", "line", "column", "message"}], "output"}

Supports Go (go test), JavaScript/TypeScript (mocha, jest or vitest from package.json; otherwise "npm test" with raw output) and Python (pytest; the standard unittest runner when pytest is not installed).
The language comes from the "language" argument, else from the extensions of "files", else from go.mod / pyproject.toml, setup.py, pytest.ini, setup.cfg, tox.ini / package.json at the project root.
When the root has no such marker, the project directory is found from the ancestors of "files", or from the only subdirectory (up to 3 levels deep) that has one; several candidates give "unsupported" with the list.
- dir: the project directory relative to the workspace root (e.g. "web" in a monorepo); "files" and the reported paths stay relative to the workspace root, Go "packages" are relative to dir.
- language: "go", "node" (JavaScript/TypeScript) or "python".
- files: Node/Python test files or directories relative to the project root (default: the whole suite).
- packages: Go package patterns (default ["./..."]).
- run: only run tests whose name matches this pattern (go test -run, mocha --grep, jest/vitest -t, pytest/unittest -k), to re-run the failing tests.
- "no_tests" means the runner found nothing to run: check the file list or the pattern.
- "error" means the runner itself failed (missing dependencies, a service that is not running, a bad config); read "output".
For other languages it returns status "unsupported" and you should use execute.`

const diagnosticsDescription = `Check the project for compile, type and lint errors and get them as a JSON list:
[{"file", "line", "column", "severity": "error"|"warning"|"info", "source", "message"}]

An empty list means the checks pass. Use it after edits to catch errors before running tests. Errors come first.
- Go: go build and go vet (source "build"/"vet").
- JavaScript/TypeScript: tsc --noEmit on the whole project when tsconfig.json exists, and eslint when an ESLint config exists. A checker that cannot run (e.g. dependencies not installed) is reported as an "info" item. With neither, a syntax check of every .js/.cjs/.mjs file (source "node").
- Python: ruff check when installed (syntax errors and undefined names are errors, other rules warnings), otherwise a syntax check of every .py file.
The language and project directory are detected like run_tests.
- dir: the project directory relative to the workspace root (e.g. "web" in a monorepo).
- language: "go", "node" or "python".
- files: Node/Python files or directories to check (eslint, ruff and the syntax check; default the whole project).
- packages: Go package patterns (default ["./..."]).
- vet: also run "go vet" (default true).
For other languages it returns status "unsupported" and you should use execute.`

// Unsupported is returned when the project is not a Go module.
type Unsupported struct {
	Status string `json:"status"`
	Reason string `json:"reason"`
	Hint   string `json:"hint"`
}

// NewRunTests returns the run_tests tool.
func NewRunTests(sb backend.SandboxBackend) tool.Tool {
	return &tool.Func{
		ToolName:        RunTestsName,
		ToolDescription: runTestsDescription,
		ToolSchema: objectSchema(map[string]any{
			"language":        languageSchema,
			"dir":             dirSchema,
			"files":           map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": `Node/Python test files or directories, e.g. ["test/user.js"] or ["tests/test_api.py"].`},
			"packages":        packagesSchema,
			"run":             map[string]any{"type": "string", "description": "Only run tests whose name matches this pattern (go test -run, mocha --grep, jest/vitest -t, pytest/unittest -k)."},
			"timeout_seconds": timeoutSchema,
		}),
		Fn: func(ctx context.Context, _ *tool.Runtime, args map[string]any) (*tool.Result, error) {
			return runTests(ctx, sb, args), nil
		},
	}
}

// NewDiagnostics returns the diagnostics tool.
func NewDiagnostics(sb backend.SandboxBackend) tool.Tool {
	return &tool.Func{
		ToolName:        DiagnosticsName,
		ToolEffect:      tool.ReadOnly,
		ToolDescription: diagnosticsDescription,
		ToolSchema: objectSchema(map[string]any{
			"language":        languageSchema,
			"dir":             dirSchema,
			"files":           map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": `Node/Python files or directories to check, e.g. ["src"] or ["app/models.py"].`},
			"packages":        packagesSchema,
			"vet":             map[string]any{"type": "boolean", "default": true, "description": "Also run go vet."},
			"timeout_seconds": timeoutSchema,
		}),
		Fn: func(ctx context.Context, _ *tool.Runtime, args map[string]any) (*tool.Result, error) {
			return diagnostics(ctx, sb, args), nil
		},
	}
}

var (
	dirSchema      = map[string]any{"type": "string", "description": `Project directory relative to the workspace root, e.g. "web"; detected when omitted.`}
	languageSchema = map[string]any{"type": "string", "enum": []string{langGo, langNode, langPython}, "description": "Project language; detected from the project files when omitted."}
	packagesSchema = map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": `Go package patterns, e.g. ["./..."] or ["./internal/ws"].`}
	timeoutSchema  = map[string]any{"type": "integer", "minimum": 1, "maximum": maxGoTimeout, "description": fmt.Sprintf("Timeout in seconds (default %d).", defaultGoTimeout)}
	packagePattern = regexp.MustCompile(`^[A-Za-z0-9_.][A-Za-z0-9_./-]*$`)
	exitLine       = regexp.MustCompile(`(?m)^` + exitMarker + `(\d+)$`)
)

// goTestArgs renders the validated `go test` arguments.
func goTestArgs(args map[string]any) (string, error) {
	pkgs, err := goPackages(args)
	if err != nil {
		return "", err
	}
	runPat, err := runPattern(args)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	if runPat != "" {
		b.WriteString("-run " + shellQuote(runPat) + " ")
	}
	b.WriteString(strings.Join(pkgs, " "))
	return b.String(), nil
}

func runPattern(args map[string]any) (string, error) {
	pat, err := stringArg(args, "run")
	if err != nil {
		return "", err
	}
	if len(pat) > maxRunPattern || strings.ContainsAny(pat, "\x00\n") {
		return "", fmt.Errorf("run must be a single-line pattern of at most %d characters", maxRunPattern)
	}
	return pat, nil
}

// goPackages validates package patterns; they must stay inside the project
// and cannot be flags.
func goPackages(args map[string]any) ([]string, error) {
	pkgs, err := stringsArg(args, "packages")
	if err != nil {
		return nil, err
	}
	if len(pkgs) == 0 {
		return []string{"./..."}, nil
	}
	for _, p := range pkgs {
		if !packagePattern.MatchString(p) {
			return nil, fmt.Errorf("invalid package pattern %q: use letters, digits, '_', '.', '/', '-' (e.g. ./... or ./internal/ws)", p)
		}
		for _, seg := range strings.Split(p, "/") {
			if seg == ".." {
				return nil, fmt.Errorf("package pattern %q must not leave the project root", p)
			}
		}
	}
	return pkgs, nil
}

func goTimeout(args map[string]any) (time.Duration, error) {
	secs, err := intArg(args, "timeout_seconds", defaultGoTimeout)
	if err != nil {
		return 0, err
	}
	if secs < 1 || secs > maxGoTimeout {
		return 0, fmt.Errorf("timeout_seconds must be between 1 and %d", maxGoTimeout)
	}
	return time.Duration(secs) * time.Second, nil
}

// unsupported maps the module check markers to a result, or nil.
func unsupported(stdout string) *tool.Result {
	switch strings.TrimSpace(stdout) {
	case noGoMarker:
		return jsonResult(Unsupported{Status: "unsupported", Reason: "the go command is not installed", Hint: "use execute to run the project's own test or build commands"})
	case noModMarker:
		return jsonResult(Unsupported{Status: "unsupported", Reason: "not a Go module, Node package or Python project (no go.mod, go.work, package.json, pyproject.toml, setup.py, setup.cfg, pytest.ini or tox.ini)", Hint: "pass language if the project files live in a subdirectory, or use execute to run the project's own test or build commands"})
	}
	return nil
}

// splitExit removes exit markers and returns the output before each one
// with its exit code.
func splitExit(stdout string) ([]string, []int) {
	var outs []string
	var codes []int
	rest := stdout
	for {
		loc := exitLine.FindStringSubmatchIndex(rest)
		if loc == nil {
			break
		}
		outs = append(outs, rest[:loc[0]])
		code, _ := strconv.Atoi(rest[loc[2]:loc[3]])
		codes = append(codes, code)
		rest = rest[loc[1]:]
	}
	return outs, codes
}

func runTests(ctx context.Context, sb backend.SandboxBackend, args map[string]any) *tool.Result {
	t, res := resolveTarget(ctx, sb, args)
	if res != nil {
		return res
	}
	sb = t.sandbox(sb)
	if t.lang == langNode || t.lang == langPython {
		pattern, err := runPattern(args)
		if err != nil {
			return errorResult("Error: %v", err)
		}
		timeout, err := goTimeout(args)
		if err != nil {
			return errorResult("Error: %v", err)
		}
		if t.lang == langNode {
			return nodeRunTests(ctx, sb, t, pattern, timeout)
		}
		return pyRunTests(ctx, sb, t, pattern, timeout)
	}
	return goRunTests(ctx, sb, t, args)
}

func goRunTests(ctx context.Context, sb backend.SandboxBackend, t target, args map[string]any) *tool.Result {
	testArgs, err := goTestArgs(args)
	if err != nil {
		return errorResult("Error: %v", err)
	}
	timeout, err := goTimeout(args)
	if err != nil {
		return errorResult("Error: %v", err)
	}
	res := run(ctx, sb, goModuleCheck+fmt.Sprintf(goTestFilter, testArgs), timeout)
	if r := unsupported(res.Stdout); r != nil {
		return r
	}
	outs, codes := splitExit(res.Stdout)
	if len(codes) == 0 {
		return errorResult("Error: go test did not finish: %s", clipRaw(res.errText()))
	}
	rep := parseTestJSON(outs[0])
	rep.Framework = frameworkGo
	for i := range rep.BuildErrors {
		rep.BuildErrors[i].File = t.rootPath(rep.BuildErrors[i].File)
	}
	rep.Truncated = rep.Truncated || res.Truncated
	if codes[0] != 0 && rep.Status == "passed" {
		rep.Status = "failed"
		if rep.Passed == 0 {
			rep.Status = "error"
		}
		rep.Output = clipRaw(nonJSON(outs[0]))
	}
	return jsonResult(rep)
}

func diagnostics(ctx context.Context, sb backend.SandboxBackend, args map[string]any) *tool.Result {
	t, res := resolveTarget(ctx, sb, args)
	if res != nil {
		return res
	}
	sb = t.sandbox(sb)
	if t.lang == langNode || t.lang == langPython {
		timeout, err := goTimeout(args)
		if err != nil {
			return errorResult("Error: %v", err)
		}
		if t.lang == langNode {
			return nodeDiagnostics(ctx, sb, t, timeout)
		}
		return pyDiagnostics(ctx, sb, t, timeout)
	}
	return goDiagnostics(ctx, sb, t, args)
}

func goDiagnostics(ctx context.Context, sb backend.SandboxBackend, t target, args map[string]any) *tool.Result {
	pkgs, err := goPackages(args)
	if err != nil {
		return errorResult("Error: %v", err)
	}
	vet, err := boolArg(args, "vet", true)
	if err != nil {
		return errorResult("Error: %v", err)
	}
	timeout, err := goTimeout(args)
	if err != nil {
		return errorResult("Error: %v", err)
	}
	list := strings.Join(pkgs, " ")
	cmd := goModuleCheck + "go build -o /dev/null " + list + " 2>&1; echo " + exitMarker + "$?"
	if vet {
		cmd += "; go vet " + list + " 2>&1; echo " + exitMarker + "$?"
	}
	res := run(ctx, sb, cmd, timeout)
	if r := unsupported(res.Stdout); r != nil {
		return r
	}
	outs, codes := splitExit(res.Stdout)
	if len(codes) == 0 {
		return errorResult("Error: go build did not finish: %s", clipRaw(res.errText()))
	}
	sources := []string{"build", "vet"}
	var diags []Diagnostic
	for i, out := range outs {
		found := parseDiagnostics(out, sources[min(i, 1)])
		if codes[i] != 0 && len(found) == 0 {
			return errorResult("Error: go %s failed without file diagnostics:\n%s", sources[min(i, 1)], clipRaw(out))
		}
		diags = append(diags, found...)
	}
	return diagnosticsResult(t, diags, res.Truncated)
}

// diagnosticsResult makes paths project-relative, lists errors before
// warnings, drops repeats and caps the list.
func diagnosticsResult(t target, diags []Diagnostic, truncated bool) *tool.Result {
	for i := range diags {
		diags[i].File = t.rootPath(diags[i].File)
	}
	rank := map[string]int{"error": 0, "warning": 1}
	slices.SortStableFunc(diags, func(a, b Diagnostic) int {
		ra, ok := rank[a.Severity]
		if !ok {
			ra = 2
		}
		rb, ok := rank[b.Severity]
		if !ok {
			rb = 2
		}
		return ra - rb
	})
	diags, capped := dedupeDiagnostics(diags)
	if capped || truncated {
		diags = append(diags, Diagnostic{Severity: "info", Source: "quill", Message: "more diagnostics were omitted; narrow packages or files to see them"})
	}
	return jsonResult(diags)
}

func nonJSON(out string) string {
	var keep []string
	for _, l := range strings.Split(out, "\n") {
		if l != "" && !strings.HasPrefix(l, "{") {
			keep = append(keep, l)
		}
	}
	return strings.Join(keep, "\n")
}

func clipRaw(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxRawOutput {
		return s
	}
	return "..." + s[len(s)-maxRawOutput:]
}
