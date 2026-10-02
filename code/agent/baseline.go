package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/tsengmingchun/deepagents_golang/backend"
	"github.com/tsengmingchun/deepagents_golang/code/codetools"
	"github.com/tsengmingchun/deepagents_golang/llm"
	"github.com/tsengmingchun/deepagents_golang/tool"
)

// baselineRef keeps the stashed changes reachable while the baseline runs,
// so they survive even if restoring them fails.
const baselineRef = "refs/dcode/baseline"

// gitTimeout bounds each git command of the baseline.
const gitTimeout = 2 * time.Minute

// verdictKind says how a failing check behaves without the agent's changes.
type verdictKind int

const (
	// verdictUnknown: the baseline could not run or gave no usable signal.
	verdictUnknown verdictKind = iota
	// verdictRegression: it passes on HEAD and still fails with the changes.
	verdictRegression
	// verdictPreexisting: the same tests also fail on HEAD.
	verdictPreexisting
	// verdictAlsoFails: the command also fails on HEAD; which tests is unknown.
	verdictAlsoFails
	// verdictFlaky: it passes on HEAD and passes again with the changes.
	verdictFlaky
)

type verdict struct {
	kind verdictKind
	// tests lists the failing tests the verdict is about, when known.
	tests []string
	// err explains verdictUnknown, or a restore problem the model must know.
	err string
}

// Baseline reruns a failing check on the unchanged HEAD: it stashes the
// working tree (untracked files included), runs the check, then restores
// the changes. It only suits a workspace no one else edits during the run,
// so dcode uses it for headless runs.
type Baseline struct {
	Workspace backend.SandboxBackend
	// RunTests reruns run_tests checks; execute checks rerun as commands.
	RunTests tool.Tool
}

// Compare tells whether the failing check (call, with result r) is caused by
// the agent's changes.
func (b *Baseline) Compare(ctx context.Context, call llm.ToolCall, r *checkResult) verdict {
	head, err := b.atHead(ctx, call)
	if err != nil {
		return verdict{err: err.Error()}
	}
	if head == nil {
		return verdict{err: "the check gave no usable result on HEAD"}
	}
	switch {
	case call.Name == codetools.RunTestsName && len(r.failedTests) > 0 && !head.passed:
		if len(head.failedTests) == 0 {
			return verdict{kind: verdictAlsoFails}
		}
		var regressions []string
		for _, t := range r.failedTests {
			if !slices.Contains(head.failedTests, t) {
				regressions = append(regressions, t)
			}
		}
		if len(regressions) == 0 {
			return verdict{kind: verdictPreexisting, tests: r.failedTests}
		}
		return b.confirm(ctx, call, regressions)
	case !head.passed:
		return verdict{kind: verdictAlsoFails}
	}
	return b.confirm(ctx, call, r.failedTests)
}

// confirm reruns the check with the changes to tell a regression from a
// flaky test.
func (b *Baseline) confirm(ctx context.Context, call llm.ToolCall, tests []string) verdict {
	again := b.run(ctx, call)
	if again == nil {
		return verdict{kind: verdictRegression, tests: tests}
	}
	if again.passed {
		return verdict{kind: verdictFlaky, tests: tests}
	}
	if len(tests) > 0 && len(again.failedTests) > 0 {
		var still []string
		for _, t := range tests {
			if slices.Contains(again.failedTests, t) {
				still = append(still, t)
			}
		}
		if len(still) == 0 {
			return verdict{kind: verdictFlaky, tests: tests}
		}
		tests = still
	}
	return verdict{kind: verdictRegression, tests: tests}
}

// atHead runs the check with the working tree stashed and restores it.
func (b *Baseline) atHead(ctx context.Context, call llm.ToolCall) (res *checkResult, err error) {
	before, _ := b.git(ctx, "rev-parse -q --verify refs/stash")
	if _, err := b.git(ctx, "stash push --include-untracked -q -m dcode-baseline"); err != nil {
		return nil, fmt.Errorf("could not stash the changes: %w", err)
	}
	sha, _ := b.git(ctx, "rev-parse -q --verify refs/stash")
	if sha == "" || sha == before {
		return nil, fmt.Errorf("there were no changes to stash")
	}
	if _, err := b.git(ctx, "update-ref "+baselineRef+" "+sha); err != nil {
		return nil, b.restore(ctx, sha, err)
	}
	res = b.run(ctx, call)
	if err := b.restore(ctx, sha, nil); err != nil {
		return nil, err
	}
	return res, nil
}

// restore removes what the HEAD run left behind and reapplies the stash.
// On failure the changes stay in baselineRef and the error says how to get
// them back.
func (b *Baseline) restore(ctx context.Context, sha string, cause error) error {
	ctx = context.WithoutCancel(ctx)
	_, err := b.git(ctx, "reset -q --hard HEAD")
	if err == nil {
		_, err = b.git(ctx, "clean -q -fd")
	}
	if err == nil {
		_, err = b.git(ctx, "stash pop -q")
	}
	if err != nil {
		return fmt.Errorf("your changes were stashed for a baseline run and could not be restored automatically (%v); "+
			"they are saved in %s: run `git stash apply %s` to get them back", err, baselineRef, sha)
	}
	_, _ = b.git(ctx, "update-ref -d "+baselineRef)
	if cause != nil {
		return fmt.Errorf("baseline run aborted: %w", cause)
	}
	return nil
}

func (b *Baseline) git(ctx context.Context, args string) (string, error) {
	t := gitTimeout
	resp := b.Workspace.Execute(ctx, "git "+args, &t)
	out := strings.TrimSpace(resp.Output)
	if resp.ExitCode == nil || *resp.ExitCode != 0 {
		return out, fmt.Errorf("git %s: %s", strings.Fields(args)[0], clipLine(out, 300))
	}
	return out, nil
}

// run reruns the check call and reads its result like the gate does.
func (b *Baseline) run(ctx context.Context, call llm.ToolCall) *checkResult {
	switch call.Name {
	case codetools.RunTestsName:
		if b.RunTests == nil {
			return nil
		}
		res, err := b.RunTests.Invoke(ctx, nil, call.Args)
		if err != nil || res == nil {
			return nil
		}
		return classifyCheck(call, resultText(res))
	case executeToolName:
		cmd, _ := call.Args["command"].(string)
		var timeout *time.Duration
		if secs, ok := asSeconds(call.Args["timeout"]); ok {
			timeout = &secs
		}
		resp := b.Workspace.Execute(ctx, cmd, timeout)
		if resp.ExitCode == nil {
			return nil
		}
		return classifyCheck(call, resp.Output+"\n[Command failed with exit code "+strconv.Itoa(*resp.ExitCode)+"]")
	}
	return nil
}

func resultText(res *tool.Result) string {
	var b strings.Builder
	for _, c := range res.Content {
		if c.Type == llm.BlockText {
			b.WriteString(c.Text)
		}
	}
	return b.String()
}

func asSeconds(v any) (time.Duration, bool) {
	switch n := v.(type) {
	case float64:
		return time.Duration(n * float64(time.Second)), n > 0
	case int:
		return time.Duration(n) * time.Second, n > 0
	case json.Number:
		f, err := n.Float64()
		return time.Duration(f * float64(time.Second)), err == nil && f > 0
	}
	return 0, false
}

// baselineMessage explains a verdict to the model; "" when the gate should
// not block because of it.
func baselineMessage(v verdict, r *checkResult) string {
	switch v.kind {
	case verdictRegression:
		var b strings.Builder
		fmt.Fprintf(&b, "%s The check `%s` passes on the unchanged HEAD (your changes were stashed, the check rerun, then restored) and fails with your changes, so your change broke it.",
			verifyMarker, clipLine(r.command, 200))
		if len(v.tests) > 0 {
			b.WriteString("\nBroken by your change:")
			for _, t := range firstN(v.tests, 10) {
				b.WriteString("\n- " + t)
			}
		}
		b.WriteString("\nThese are not unrelated or pre-existing failures. Find what in your change causes them, fix it, and rerun the check.")
		return b.String()
	case verdictAlsoFails:
		return "\nThe same check also fails on the unchanged HEAD (checked by stashing your changes), so some failures are not caused by your change; compare the failures to find any that are."
	case verdictUnknown:
		if strings.Contains(v.err, "could not be restored") {
			return "\n" + v.err
		}
	}
	return ""
}

func firstN(xs []string, n int) []string {
	if len(xs) > n {
		return xs[:n]
	}
	return xs
}
