package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/tsengmingchun/quill/apps/agent"
	core "github.com/tsengmingchun/quill/apps/agent/agent"
	"github.com/tsengmingchun/quill/apps/agent/backend"
	"github.com/tsengmingchun/quill/apps/agent/code/codetools"
	"github.com/tsengmingchun/quill/apps/agent/llm"
	rt "github.com/tsengmingchun/quill/apps/agent/runtime"
	"github.com/tsengmingchun/quill/apps/agent/tool"
)

const (
	// gateKindReview marks the gate message with the reviewer's findings.
	gateKindReview = "review"
	reviewToolName = "submit_review"
	// reviewerSteps bounds the reviewer's run.
	reviewerSteps   = 40
	maxDiffFindings = 8
)

var (
	hunkHeader = regexp.MustCompile(`^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@`)
	assertion  = regexp.MustCompile(`\bassert\w*\s*[.(]|\bexpect\s*\(|\.should\b|\.to\.|\brequire\.\w+\(|\bt\.(?:Error|Fatal|Fail)\w*\(|\bself\.assert\w+\(|\bpytest\.raises\(`)
	skipOrOnly = regexp.MustCompile(`\b(?:it|describe|test|context)\.(?:only|skip)\(|\bx(?:it|describe|test)\(|\bt\.Skip(?:Now|f)?\(|@pytest\.mark\.skip|@unittest\.skip|\bpytest\.skip\(`)
	debugLine  = regexp.MustCompile(`\bconsole\.(?:log|debug|trace)\(|\bdebugger\b|\bfmt\.Print(?:ln|f)?\(|\bprintln\(|\bprint\(|\bpdb\.set_trace\(|\bbreakpoint\(\)|\bbinding\.pry\b`)
	goRequire  = regexp.MustCompile(`^\s*(?:require\s+)?([A-Za-z0-9.\-]+\.[A-Za-z]+/\S+)\s+v\d`)
	jsonDep    = regexp.MustCompile(`^\s*"([^"]+)"\s*:\s*"[\^~>=<]*\d`)
	pyRequire  = regexp.MustCompile(`^\s*([A-Za-z0-9][A-Za-z0-9._\-\[\]]*)\s*(?:[=<>!~]=|[<>]|$)`)

	codeExts  = []string{".go", ".js", ".jsx", ".ts", ".tsx", ".mjs", ".cjs", ".py", ".rb"}
	lockFiles = []string{"go.sum", "package-lock.json", "yarn.lock", "pnpm-lock.yaml", "poetry.lock", "Pipfile.lock", "uv.lock"}
)

type diffLine struct {
	line int
	text string
}

// fileDiff is one file of a unified diff: added lines numbered in the new
// file, removed lines in the old one.
type fileDiff struct {
	path           string
	added, removed []diffLine
}

// parseDiff reads `git diff` output.
func parseDiff(text string) []fileDiff {
	var files []fileDiff
	inHunk := false
	oldLine, newLine := 0, 0
	for _, l := range strings.Split(text, "\n") {
		switch {
		case strings.HasPrefix(l, "diff --git "):
			inHunk = false
			p := l[strings.LastIndex(l, " b/")+3:]
			files = append(files, fileDiff{path: p})
		case len(files) == 0:
		case !inHunk && strings.HasPrefix(l, "+++ "):
			if p := strings.TrimPrefix(l[4:], "b/"); p != "/dev/null" {
				files[len(files)-1].path = p
			}
		case strings.HasPrefix(l, "@@"):
			m := hunkHeader.FindStringSubmatch(l)
			if m == nil {
				continue
			}
			inHunk = true
			oldLine, _ = strconv.Atoi(m[1])
			newLine, _ = strconv.Atoi(m[2])
		case !inHunk:
		case strings.HasPrefix(l, "+"):
			f := &files[len(files)-1]
			f.added = append(f.added, diffLine{newLine, l[1:]})
			newLine++
		case strings.HasPrefix(l, "-"):
			f := &files[len(files)-1]
			f.removed = append(f.removed, diffLine{oldLine, l[1:]})
			oldLine++
		case strings.HasPrefix(l, " "):
			oldLine++
			newLine++
		}
	}
	return files
}

// checkDiff looks for what a careful reviewer flags on sight: files outside
// the patch intent, weakened or focused tests, debug output and new
// dependencies.
func checkDiff(files []fileDiff, intent []string) []string {
	var planned []string
	for _, p := range intent {
		planned = append(planned, strings.TrimPrefix(path.Clean("/"+p), "/"))
	}
	var findings, outside []string
	for _, f := range files {
		if isByproduct(f.path) {
			continue
		}
		base := path.Base(f.path)
		test := isTestFile(f.path)
		if len(planned) > 0 && !test && !slices.Contains(lockFiles, base) && !inIntent(f.path, planned) {
			outside = append(outside, f.path)
		}
		switch {
		case test:
			findings = append(findings, testFindings(f)...)
		case slices.Contains(codeExts, path.Ext(base)):
			for _, l := range f.added {
				if debugLine.MatchString(l.text) {
					findings = append(findings, fmt.Sprintf("`%s:%d` adds debug output: `%s`. Remove it unless the task asks for it.", f.path, l.line, clipLine(strings.TrimSpace(l.text), 120)))
				}
			}
		}
		if deps := newDependencies(f); len(deps) > 0 {
			findings = append(findings, fmt.Sprintf("`%s` adds dependencies: %s. Add one only when the task needs it and the project has no equivalent.", f.path, strings.Join(clipList(deps, 5), ", ")))
		}
	}
	if len(outside) > 0 {
		findings = append([]string{fmt.Sprintf("You changed files outside your patch_intent (%s): %s. Revert what the task does not need, or record why with repair set_hypothesis.", fileList(planned), fileList(outside))}, findings...)
	}
	return clipList(findings, maxDiffFindings)
}

func inIntent(p string, planned []string) bool {
	return slices.ContainsFunc(planned, func(i string) bool { return p == i || strings.HasPrefix(p, strings.TrimSuffix(i, "/")+"/") })
}

func testFindings(f fileDiff) []string {
	var out []string
	removed, added := 0, 0
	var gone []string
	for _, l := range f.removed {
		if assertion.MatchString(l.text) {
			removed++
			gone = append(gone, "`"+clipLine(strings.TrimSpace(l.text), 80)+"`")
		}
	}
	for _, l := range f.added {
		if assertion.MatchString(l.text) {
			added++
		}
		if skipOrOnly.MatchString(l.text) {
			out = append(out, fmt.Sprintf("`%s:%d` skips or focuses tests: `%s`. Do not skip or focus tests to get a passing run.", f.path, l.line, clipLine(strings.TrimSpace(l.text), 120)))
		}
	}
	if removed > added {
		out = append([]string{fmt.Sprintf("`%s` removes %d assertion(s) (%s). Never weaken a test so that the code passes; restore them unless the task changes that behavior.", f.path, removed-added, strings.Join(clipList(gone, 3), ", "))}, out...)
	}
	return out
}

func newDependencies(f fileDiff) []string {
	base := path.Base(f.path)
	var re *regexp.Regexp
	switch {
	case base == "go.mod":
		re = goRequire
	case base == "package.json":
		re = jsonDep
	case strings.HasPrefix(base, "requirements") && strings.HasSuffix(base, ".txt"):
		re = pyRequire
	default:
		return nil
	}
	var deps []string
	for _, l := range f.added {
		if m := re.FindStringSubmatch(l.text); m != nil && m[1] != "version" && !strings.HasPrefix(strings.TrimSpace(l.text), "#") {
			deps = append(deps, m[1])
		}
	}
	return deps
}

// workingDiff is the diff of the working tree, untracked files included,
// against HEAD, without touching the index.
func workingDiff(ctx context.Context, sb backend.SandboxBackend) (string, error) {
	return runScript(ctx, sb, `set -e
git rev-parse -q --verify HEAD >/dev/null
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
idx=$(git rev-parse --git-path index)
if [ -f "$idx" ]; then cp "$idx" "$tmp/index"; fi
export GIT_INDEX_FILE="$tmp/index"
git add -A
git -c core.quotepath=off diff --cached --no-color --no-ext-diff --no-renames -U0 HEAD`)
}

// Review is the reviewer's verdict on a change.
type Review struct {
	Verdict      string                `json:"verdict"`
	Issues       []ReviewIssue         `json:"issues"`
	Requirements []ReviewedRequirement `json:"requirements,omitempty"`
}

type ReviewIssue struct {
	Severity string `json:"severity"`
	File     string `json:"file,omitempty"`
	Line     int    `json:"line,omitempty"`
	Kind     string `json:"kind,omitempty"`
	Detail   string `json:"detail"`
}

type ReviewedRequirement struct {
	Requirement string `json:"requirement"`
	Met         bool   `json:"met"`
	Evidence    string `json:"evidence,omitempty"`
}

// ReviewRequest is what the reviewer is told about the change; it reads
// the diff itself.
type ReviewRequest struct {
	Task   string
	Record RepairRecord
	Files  []string
	// Check is the last passing check after the change.
	Check string
}

// Reviewer reviews a finished change independently of the agent that made
// it.
type Reviewer interface {
	Review(ctx context.Context, req ReviewRequest) (Review, error)
}

// reviewMessage asks the agent to fix the review's high-severity issues and
// unmet requirements, listing medium ones alongside; "" when there are
// none to fix.
func reviewMessage(r Review) string {
	var fix, consider []string
	for _, i := range r.Issues {
		switch i.Severity {
		case "high":
			fix = append(fix, issueLine(i))
		case "medium":
			consider = append(consider, issueLine(i))
		}
	}
	for _, q := range r.Requirements {
		if !q.Met {
			line := "- Requirement not met: " + clipLine(q.Requirement, 300)
			if q.Evidence != "" {
				line += " (" + clipLine(q.Evidence, 300) + ")"
			}
			fix = append(fix, line)
		}
	}
	if len(fix) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(verifyMarker + " An independent review of your change found problems. Fix them, rerun the affected tests, then finish:\n")
	b.WriteString(strings.Join(fix, "\n"))
	if len(consider) > 0 {
		b.WriteString("\nAlso worth a look:\n" + strings.Join(consider, "\n"))
	}
	b.WriteString("\nIf a finding is wrong, do not change the code for it; say why in your final answer.")
	return b.String()
}

func issueLine(i ReviewIssue) string {
	var b strings.Builder
	fmt.Fprintf(&b, "- [%s]", i.Severity)
	if i.File != "" {
		b.WriteString(" " + i.File)
		if i.Line > 0 {
			fmt.Fprintf(&b, ":%d", i.Line)
		}
	}
	if i.Kind != "" {
		fmt.Fprintf(&b, " (%s)", i.Kind)
	}
	b.WriteString(": " + clipLine(i.Detail, 400))
	return b.String()
}

// reviewerTools are the read-only tools the reviewer keeps.
var reviewerTools = []string{"ls", "read_file", "glob", "grep", codetools.GitStatusName, codetools.GitDiffName}

const reviewerPrompt = `You review a code change made by a coding agent, independently of it. You can only read: git_diff, git_status, read_file, grep, glob and ls.

Read the diff with git_diff, then the code around it. Check, in order:
1. Does the change do what the task asks? Check each requirement and expected behavior against the code.
2. Does it fix the cause, or only the symptom where the test looks?
3. Can it break existing behavior: other callers of changed functions, changed signatures, error paths, edge cases (empty, nil, zero, boundaries, concurrency)?
4. Were tests weakened: assertions removed, tests skipped, or expectations rewritten to match the code?
5. Is anything left that does not belong: debug output, unrelated edits, new dependencies, generated files?

Severity: high is wrong behavior, an unmet requirement, a likely regression or a weakened test; the agent must fix it. medium is a real risk worth a look. low is minor. Do not report style or naming. Report only what you verified in the code, with file and line.

The verdict is request_changes when there is a high issue or an unmet requirement, approve otherwise. Call submit_review exactly once, at the end.`

var reviewSchema = tool.Schema{
	"title":       reviewToolName,
	"description": "Submit your review of the change. Call it once, at the end.",
	"type":        "object",
	"properties": map[string]any{
		"verdict": map[string]any{"type": "string", "enum": []any{"approve", "request_changes"}},
		"issues": map[string]any{"type": "array", "items": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"severity": map[string]any{"type": "string", "enum": []any{"high", "medium", "low"}},
				"file":     map[string]any{"type": "string"},
				"line":     map[string]any{"type": "integer"},
				"kind":     map[string]any{"type": "string", "enum": []any{"logic", "requirement", "regression", "edge_case", "test", "scope", "debug", "dependency", "other"}},
				"detail":   map[string]any{"type": "string"},
			},
			"required":             []any{"severity", "detail"},
			"additionalProperties": false,
		}},
		"requirements": map[string]any{"type": "array", "description": "Each requirement or expected behavior of the task and whether the change meets it.", "items": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"requirement": map[string]any{"type": "string"},
				"met":         map[string]any{"type": "boolean"},
				"evidence":    map[string]any{"type": "string"},
			},
			"required":             []any{"requirement", "met"},
			"additionalProperties": false,
		}},
	},
	"required":             []any{"verdict", "issues"},
	"additionalProperties": false,
}

// modelReviewer runs a read-only agent that ends with submit_review.
type modelReviewer struct {
	agent core.Agent
}

// NewModelReviewer builds a reviewer that reads the workspace in b with
// model.
func NewModelReviewer(model llm.ChatModel, b backend.Backend) (Reviewer, error) {
	var git []tool.Tool
	for _, t := range codetools.New(b) {
		if slices.Contains(reviewerTools, t.Name()) {
			git = append(git, t)
		}
	}
	a, err := quill.CreateAgent(
		quill.WithModel(model),
		quill.WithBackend(b),
		quill.WithSystemPrompt(reviewerPrompt),
		quill.WithTools(git...),
		quill.WithName("reviewer"),
		quill.WithMiddleware(researchLimits{allowed: reviewerTools,
			refusal: "Error: `%s` is not available to the reviewer, which only reads; submit your review instead."}),
		quill.WithResponseFormat(core.ToolStrategy{Schema: reviewSchema}),
	)
	if err != nil {
		return nil, fmt.Errorf("reviewer: %w", err)
	}
	return modelReviewer{agent: a}, nil
}

func (r modelReviewer) Review(ctx context.Context, req ReviewRequest) (Review, error) {
	st, err := r.agent.Invoke(ctx, core.Input{Messages: []llm.Message{llm.NewUserMessage(reviewRequestText(req))}}, rt.RunConfig{RecursionLimit: reviewerSteps})
	if err != nil {
		return Review{}, fmt.Errorf("reviewer: %w", err)
	}
	if st.StructuredResponse == nil {
		return Review{}, errors.New("reviewer: finished without submit_review")
	}
	raw, err := json.Marshal(st.StructuredResponse)
	if err != nil {
		return Review{}, err
	}
	var out Review
	if err := json.Unmarshal(raw, &out); err != nil {
		return Review{}, fmt.Errorf("reviewer: %w", err)
	}
	return out, nil
}

func reviewRequestText(req ReviewRequest) string {
	var b strings.Builder
	b.WriteString("Review the uncommitted change for the task below: read it with git_diff, then call submit_review.\n\n<task>\n")
	b.WriteString(strings.TrimSpace(req.Task))
	b.WriteString("\n</task>\n")
	rec := req.Record
	if !rec.empty() {
		b.WriteString("\nWhat the agent recorded:\n")
		if rec.Task != nil {
			for _, e := range rec.Task.Expected {
				fmt.Fprintf(&b, "- expected: %s\n", e)
			}
			for _, c := range rec.Task.Constraints {
				fmt.Fprintf(&b, "- constraint: %s\n", c)
			}
		}
		if rec.Hypothesis != "" {
			fmt.Fprintf(&b, "- hypothesis: %s\n", rec.Hypothesis)
		}
		if rec.Intent != nil && len(rec.Intent.Files) > 0 {
			fmt.Fprintf(&b, "- planned files: %s\n", strings.Join(rec.Intent.Files, ", "))
		}
	}
	if len(req.Files) > 0 {
		fmt.Fprintf(&b, "\nChanged files: %s\n", strings.Join(req.Files, ", "))
	}
	if req.Check != "" {
		fmt.Fprintf(&b, "Last check after the change: `%s` passed.\n", req.Check)
	}
	return b.String()
}
