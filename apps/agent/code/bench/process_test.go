package bench

import (
	"strings"
	"testing"
	"time"

	"github.com/tsengmingchun/quill/apps/agent/code/cost"
	"github.com/tsengmingchun/quill/apps/agent/code/trace"
	"github.com/tsengmingchun/quill/apps/agent/llm"
)

func toolResult(id, text string, d time.Duration) llm.Message {
	return llm.WithTiming(llm.NewToolMessage(id, "", text), llm.Timing{Start: time.Now(), Duration: d})
}

func mainCall(calls ...llm.ToolCall) trace.LLMCallDetail {
	return trace.LLMCallDetail{LLMCall: trace.LLMCall{ToolCount: 5}, Response: &llm.Message{ToolCalls: calls}}
}

func TestMetricsFromTraceCountsRunTests(t *testing.T) {
	calls := []trace.LLMCallDetail{mainCall(llm.ToolCall{ID: "r1", Name: "run_tests"}, llm.ToolCall{ID: "e1", Name: "execute",
		Args: map[string]any{"command": "pytest -q"}})}
	if m := MetricsFromTrace(calls, cost.Table{}); m.TestRuns != 2 {
		t.Fatalf("TestRuns = %d, want 2", m.TestRuns)
	}
}

func TestProcessFromTrace(t *testing.T) {
	grep := func(id string) llm.ToolCall {
		return llm.ToolCall{ID: id, Name: "grep", Args: map[string]any{"pattern": "x"}}
	}
	verify := llm.NewUserMessage("[verify] run the tests")
	calls := []trace.LLMCallDetail{
		mainCall(grep("g1"), llm.ToolCall{ID: "r1", Name: "read_file", Args: map[string]any{"file_path": "/README.md"}}),
		{LLMCall: trace.LLMCall{ToolCount: 5}, Messages: []llm.Message{toolResult("g1", "aaaa", 3*time.Second), toolResult("r1", "bb", time.Second)},
			Response: &llm.Message{ToolCalls: []llm.ToolCall{grep("g2"), {ID: "w1", Name: "write_todos"}}}},
		{LLMCall: trace.LLMCall{ToolCount: 5}, Messages: []llm.Message{toolResult("g2", "cc", 2*time.Second), toolResult("w1", "", 0), verify},
			Response: &llm.Message{ToolCalls: []llm.ToolCall{{ID: "w2", Name: "write_todos", Args: map[string]any{"todos": "b"}}}}},
		{LLMCall: trace.LLMCall{ToolCount: 5}, Messages: []llm.Message{verify},
			Response: &llm.Message{ToolCalls: []llm.ToolCall{{ID: "p1", Name: "apply_patch",
				Args: map[string]any{"patch": "*** Begin Patch\n*** Update File: /src/user.js\n-a\n+b\n*** End Patch"}}}}},
		{LLMCall: trace.LLMCall{ToolCount: 5, Namespace: []string{"task:x"}}, Response: &llm.Message{ToolCalls: []llm.ToolCall{grep("s1")}}},
	}
	p := ProcessFromTrace(calls, []string{"src/user.js", "test/user.js", "docs/x.md"})
	if p.DuplicateToolCalls != 1 || p.MaxRepeat != 2 {
		t.Errorf("duplicates %d max repeat %d, want 1 and 2 (subagent calls do not count)", p.DuplicateToolCalls, p.MaxRepeat)
	}
	if p.ToolOutputChars["grep"] != 6 || p.ToolOutputChars["read_file"] != 2 {
		t.Errorf("output chars %v", p.ToolOutputChars)
	}
	if p.TodoOnlyTurns != 1 {
		t.Errorf("todo-only turns %d, want 1 (mixed responses do not count)", p.TodoOnlyTurns)
	}
	if p.VerifyBlocks != 1 {
		t.Errorf("verify blocks %d, want 1 (a resent message counts once)", p.VerifyBlocks)
	}
	if p.ToolWallMS != 5000 {
		t.Errorf("tool wall %d ms, want 5000 (3s + 2s: parallel calls count their slowest)", p.ToolWallMS)
	}
	if p.FirstGoldSrcTouchStep == nil || *p.FirstGoldSrcTouchStep != 4 {
		t.Errorf("first gold source touch %v, want step 4 (README.md is not source)", p.FirstGoldSrcTouchStep)
	}
	if p.ToolsHash != "" {
		t.Errorf("tools hash %q without tools", p.ToolsHash)
	}
}

func TestGoldSourcePaths(t *testing.T) {
	got := GoldSourcePaths([]string{"lib/a.py", "test/units/test_a.py", "changelogs/fragments/x.yml", "docs/docsite/a.rst", "README.md", "src/b.js"})
	if strings.Join(got, ",") != "lib/a.py,src/b.js" {
		t.Fatalf("got %v", got)
	}
	rc, ok := SourceRecall([]string{"lib/a.py", "test/units/test_a.py"}, []string{"lib/a.py", "src/b.js", "test/units/test_a.py"})
	if !ok || rc != 0.5 {
		t.Fatalf("recall %v %v, want 0.5", rc, ok)
	}
	if _, ok := SourceRecall(nil, []string{"README.md"}); ok {
		t.Fatal("recall without gold source files should be unknown")
	}
}

func TestObservedFailure(t *testing.T) {
	task := Task{GoldPaths: []string{"src/user.js", "test/user.js"}}
	patch := func(file string) string {
		return "diff --git a/" + file + " b/" + file + "\n--- a/" + file + "\n+++ b/" + file + "\n@@ -1 +1 @@\n-a\n+b\n"
	}
	broken := []string{"test/user.js | User should export posts"}
	cases := []struct {
		name    string
		a       Attempt
		failing []string
		outputs []string
		want    string
	}{
		{"resolved", Attempt{Resolved: true}, nil, nil, ObservedResolved},
		{"timeout", Attempt{FailureCategory: CategoryAgentTimeout, Patch: patch("src/user.js")}, broken, nil, ObservedBudget},
		{"empty", Attempt{FailureCategory: CategoryEmptyPatch}, nil, nil, ObservedEmptyPatch},
		{"regression seen", Attempt{Patch: patch("src/user.js"), F2PTotal: 2, F2PPassed: 2}, broken,
			[]string{"1 failing\n  User should export posts"}, ObservedRegressionDismissed},
		{"regression unseen", Attempt{Patch: patch("src/user.js"), F2PTotal: 2, F2PPassed: 2}, broken,
			[]string{"all passing"}, ObservedRegressionUnnoticed},
		{"wrong solution", Attempt{Patch: patch("src/user.js"), F2PTotal: 2, F2PPassed: 1}, nil, nil, ObservedWrongSolution},
		{"localization", Attempt{Patch: patch("src/other.js"), F2PTotal: 2}, nil, nil, ObservedLocalization},
	}
	for _, tc := range cases {
		if got := ObservedFailure(tc.a, task, tc.failing, tc.outputs); got != tc.want {
			t.Errorf("%s: got %s want %s", tc.name, got, tc.want)
		}
	}
}

func TestTestLeaf(t *testing.T) {
	cases := map[string]string{
		"test/user.js | User should export posts":                  "User should export posts",
		"test/units/test_a.py::TestA::test_b":                      "test_b",
		"test/units/test_a.py::test_c[param]":                      "test_c[param]",
		"x | " + strings.Repeat("a", 50) + strings.Repeat("b", 20): strings.Repeat("a", 40) + strings.Repeat("b", 20),
	}
	for in, want := range cases {
		if got := testLeaf(in); got != want {
			t.Errorf("testLeaf(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestInputJumpsIgnoresExplainedGrowth(t *testing.T) {
	big := strings.Repeat("x", 80_000)
	calls := []trace.LLMCallDetail{
		{LLMCall: trace.LLMCall{Usage: &llm.Usage{InputTokens: 1000, OutputTokens: 100}}},
		{LLMCall: trace.LLMCall{NewFrom: 0, Usage: &llm.Usage{InputTokens: 21_100}}, Messages: []llm.Message{llm.NewUserMessage(big)}},
		{LLMCall: trace.LLMCall{NewFrom: 1, Usage: &llm.Usage{InputTokens: 50_000}}, Messages: []llm.Message{llm.NewUserMessage(big)}},
	}
	n, largest := inputJumps(calls)
	if n != 1 || largest != 28_900 {
		t.Fatalf("jumps %d largest %d, want 1 and 28900", n, largest)
	}
}

func TestAnalyzeMarkdownGolden(t *testing.T) {
	rows := []AttemptAnalysis{{
		Attempt: Attempt{InstanceID: "instance_NodeBB__NodeBB-bd80d36e0dcf-v4fb", Metrics: Metrics{TestRuns: 0}},
		Metrics: Metrics{Steps: 12, LLMCalls: 14, TestRuns: 3, TokensIn: 2_500_000, CacheRead: 2_000_000, PeakContextTokens: 90_000, ToolMS: 9000,
			Process: &Process{DuplicateToolCalls: 2, MaxRepeat: 2, ToolWallMS: 6000, ToolOutputChars: map[string]int{"execute": 300, "read_file": 100}}},
		Observed:   ObservedRegressionDismissed,
		FailingP2P: []string{"test/user.js | User should export posts"},
	}}
	got := AnalyzeMarkdown(Run{ID: "r1", Model: "m", Subset: "regress-v1", GitCommit: "0123456789abcdef"}, rows)
	want := `| NodeBB-bd80 | 0 | regression_dismissed |  | 12 | 14 | 3 | 0 | 2 | 2 | 0 | 0 | 2.50 | 80 | 90 | 0 | - | - | 6 / 9 | |`
	if !strings.Contains(got, want) {
		t.Errorf("row missing:\n%s", got)
	}
	for _, s := range []string{"commit `0123456789ab`", "- regression_dismissed: 1", "`test/user.js | User should export posts`", "- execute: 300 chars (75%)"} {
		if !strings.Contains(got, s) {
			t.Errorf("missing %q in:\n%s", s, got)
		}
	}
}
