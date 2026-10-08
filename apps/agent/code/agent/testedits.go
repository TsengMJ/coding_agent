package agent

import (
	"context"
	"fmt"
	"maps"
	"path"
	"slices"
	"strings"

	"github.com/tsengmingchun/quill/apps/agent/backend"
	"github.com/tsengmingchun/quill/apps/agent/code/codetools"
	"github.com/tsengmingchun/quill/apps/agent/llm"
	"github.com/tsengmingchun/quill/apps/agent/tool"
)

const gateKindTestEdit = "test_edit"

// testEditsRef keeps the agent's test changes reachable while the original
// tests run, so they survive even if restoring them fails.
const testEditsRef = "refs/quill/test-edits"

// TestEdits runs the HEAD version of the existing test files the agent
// changed against the agent's source changes. A test that fails there was
// rewritten to match the new code: either the task changes that behaviour,
// or the change broke it and the test edit hid the regression.
type TestEdits struct {
	Workspace backend.SandboxBackend
	RunTests  tool.Tool
}

// Check returns the gate message, or "" when the original tests pass, the
// agent changed no existing test, or the tests gave no usable result.
// last is the turn's last check, whose language, dir and timeout it reuses.
func (te TestEdits) Check(ctx context.Context, last llm.ToolCall) string {
	if te.RunTests == nil {
		return ""
	}
	t := gitTimeout
	resp := te.Workspace.Execute(ctx, "git status --porcelain=v1 -uall --no-renames", &t)
	if resp.ExitCode == nil || *resp.ExitCode != 0 {
		return ""
	}
	_, files := classifyStatus(resp.Output)
	if len(files) == 0 {
		return ""
	}
	results, err := te.runOriginal(ctx, files, last)
	if err != nil {
		return verifyMarker + " " + err.Error()
	}
	var failures []string
	for _, r := range results {
		if r != nil && len(r.failedTests) > 0 {
			failures = append(failures, r.failures...)
		}
	}
	if len(failures) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s You changed existing tests (%s). Their original versions, run against your current code (your test changes were stashed, the tests run, then restored), fail:", verifyMarker, fileList(files))
	for _, f := range failures {
		b.WriteString("\n- " + clipLine(f, 300))
	}
	b.WriteString("\nSo your change alters behaviour these tests check, and you edited the tests to match. " +
		"Unless the task explicitly requires this behaviour change, restore the original tests (git checkout -- <file>) and fix the code so they pass. " +
		"If the task does require it, keep the change and name the requirement in your final reply.")
	return b.String()
}

// runOriginal stashes only the changed test files, runs them, and restores
// them.
func (te TestEdits) runOriginal(ctx context.Context, files []string, last llm.ToolCall) ([]*checkResult, error) {
	b := Baseline{Workspace: te.Workspace}
	quoted := make([]string, len(files))
	for i, f := range files {
		quoted[i] = shellQuote(f)
	}
	pathspec := strings.Join(quoted, " ")
	before, _ := b.git(ctx, "rev-parse -q --verify refs/stash")
	if _, err := b.git(ctx, "stash push -q -m quill-original-tests -- "+pathspec); err != nil {
		return nil, nil
	}
	sha, _ := b.git(ctx, "rev-parse -q --verify refs/stash")
	if sha == "" || sha == before {
		return nil, nil
	}
	if _, err := b.git(ctx, "update-ref "+testEditsRef+" "+sha); err != nil {
		return nil, te.restore(ctx, b, sha, pathspec)
	}
	var results []*checkResult
	for _, args := range originalTestArgs(files, last) {
		call := llm.ToolCall{Name: codetools.RunTestsName, Args: args}
		res, err := te.RunTests.Invoke(ctx, nil, args)
		if err == nil && res != nil {
			results = append(results, classifyCheck(call, resultText(res)))
		}
	}
	return results, te.restore(ctx, b, sha, pathspec)
}

// restore puts the original test files back to HEAD, in case the run
// touched them, and reapplies the stashed changes; nothing else is reset.
func (te TestEdits) restore(ctx context.Context, b Baseline, sha, pathspec string) error {
	ctx = context.WithoutCancel(ctx)
	_, err := b.git(ctx, "checkout -q HEAD -- "+pathspec)
	if err == nil {
		_, err = b.git(ctx, "stash pop -q")
	}
	if err != nil {
		return fmt.Errorf("your test changes were stashed to run the original tests and could not be restored automatically (%v); "+
			"they are saved in %s: run `git stash apply %s` to get them back", err, testEditsRef, sha)
	}
	_, _ = b.git(ctx, "update-ref -d "+testEditsRef)
	return nil
}

// originalTestArgs builds run_tests calls for the files: Go test files run
// by package, other files by name. language, dir and timeout come from the
// turn's last run_tests call.
func originalTestArgs(files []string, last llm.ToolCall) []map[string]any {
	base := map[string]any{}
	dir := ""
	if last.Name == codetools.RunTestsName {
		for _, k := range []string{"language", "dir", "timeout_seconds"} {
			if v, ok := last.Args[k]; ok {
				base[k] = v
			}
		}
		dir, _ = last.Args["dir"].(string)
	}
	var pkgs, others []string
	for _, f := range files {
		if dir != "" {
			f = strings.TrimPrefix(f, strings.TrimSuffix(dir, "/")+"/")
		}
		if strings.HasSuffix(f, "_test.go") {
			if p := "./" + path.Dir(f); !slices.Contains(pkgs, p) {
				pkgs = append(pkgs, p)
			}
		} else {
			others = append(others, f)
		}
	}
	var out []map[string]any
	if len(pkgs) > 0 {
		args := maps.Clone(base)
		args["packages"] = toAny(pkgs)
		out = append(out, args)
	}
	if len(others) > 0 {
		args := maps.Clone(base)
		args["files"] = toAny(others)
		out = append(out, args)
	}
	return out
}

func toAny(xs []string) []any {
	out := make([]any, len(xs))
	for i, x := range xs {
		out[i] = x
	}
	return out
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
