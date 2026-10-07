package agent

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	core "github.com/tsengmingchun/quill/agent"
	"github.com/tsengmingchun/quill/llm"
	rt "github.com/tsengmingchun/quill/runtime"
)

const sampleDiff = `diff --git a/a.go b/a.go
--- a/a.go
+++ b/a.go
@@ -3 +3,2 @@
-func Two() int { return 3 }
+func Two() int { return 2 }
+func debug() { fmt.Println("here") }
diff --git a/b.go b/b.go
new file mode 100644
--- /dev/null
+++ b/b.go
@@ -0,0 +1 @@
+package m
diff --git a/a_test.go b/a_test.go
--- a/a_test.go
+++ b/a_test.go
@@ -5,3 +5,2 @@
-	if Two() != 2 {
-		t.Fatal("want 2")
+	t.Skip("flaky")
diff --git a/web/app.spec.js b/web/app.spec.js
--- a/web/app.spec.js
+++ b/web/app.spec.js
@@ -1 +1 @@
-it("works", () => { expect(run()).toBe(1) })
+it.only("works", () => { console.log(run()) })
diff --git a/go.mod b/go.mod
--- a/go.mod
+++ b/go.mod
@@ -3,0 +4 @@
+require github.com/pkg/errors v0.9.1
diff --git a/package.json b/package.json
--- a/package.json
+++ b/package.json
@@ -5,0 +6 @@
+    "lodash": "^4.17.21",
`

func findingWith(t *testing.T, findings []string, parts ...string) {
	t.Helper()
	if !slices.ContainsFunc(findings, func(f string) bool {
		for _, p := range parts {
			if !strings.Contains(f, p) {
				return false
			}
		}
		return true
	}) {
		t.Fatalf("no finding with %q in:\n%s", parts, strings.Join(findings, "\n"))
	}
}

func TestParseDiff(t *testing.T) {
	files := parseDiff(sampleDiff)
	if len(files) != 6 {
		t.Fatalf("files = %+v", files)
	}
	a := files[0]
	if a.path != "a.go" || len(a.added) != 2 || len(a.removed) != 1 || a.added[1].line != 4 {
		t.Fatalf("a.go = %+v", a)
	}
}

func TestCheckDiffFlagsFilesOutsideTheIntent(t *testing.T) {
	findings := checkDiff(parseDiff(sampleDiff), []string{"/a.go"})
	findingWith(t, findings, "b.go", "patch_intent")
	for _, f := range findings {
		_, outside, _ := strings.Cut(f, "): ")
		if strings.Contains(f, "patch_intent") && (strings.Contains(outside, "a_test.go") || strings.Contains(outside, "`a.go`")) {
			t.Fatalf("flagged a planned or test file: %s", f)
		}
	}
	if slices.ContainsFunc(checkDiff(parseDiff(sampleDiff), nil), func(f string) bool { return strings.Contains(f, "patch_intent") }) {
		t.Fatal("flagged scope without an intent")
	}
}

func TestCheckDiffFlagsWeakenedTests(t *testing.T) {
	findings := checkDiff(parseDiff(sampleDiff), nil)
	findingWith(t, findings, "a_test.go", "assertion")
	findingWith(t, findings, "a_test.go:5", "t.Skip")
	findingWith(t, findings, "web/app.spec.js:1", "it.only")
}

func TestCheckDiffFlagsDebugLeftovers(t *testing.T) {
	findings := checkDiff(parseDiff(sampleDiff), nil)
	findingWith(t, findings, "a.go:4", "fmt.Println")
	if slices.ContainsFunc(findings, func(f string) bool { return strings.Contains(f, "app.spec.js") && strings.Contains(f, "debug output") }) {
		t.Fatalf("flagged debug output in a test file:\n%s", strings.Join(findings, "\n"))
	}
}

func TestCheckDiffFlagsNewDependencies(t *testing.T) {
	findings := checkDiff(parseDiff(sampleDiff), nil)
	findingWith(t, findings, "go.mod", "github.com/pkg/errors")
	findingWith(t, findings, "package.json", "lodash")
}

func TestReviewDiffIncludesDiffFindings(t *testing.T) {
	dir := goRepo(t, nil)
	writeFile(t, filepath.Join(dir, "m.go"), strings.Replace(mathGo, "return a + b", "println(\"add\")\n\treturn a + b", 1))
	writeFile(t, filepath.Join(dir, "extra.go"), "package m\n")
	msg := reviewDiff(context.Background(), workspace(t, dir), "fix Add", []string{"m.go"})
	for _, want := range []string{"[verify]", "m.go:", "println", "extra.go"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("review lacks %q:\n%s", want, msg)
		}
	}
}

func TestReviewMessage(t *testing.T) {
	r := Review{
		Verdict: "request_changes",
		Issues: []ReviewIssue{
			{Severity: "high", File: "m.go", Line: 7, Kind: "logic", Detail: "Mul adds instead of multiplying"},
			{Severity: "medium", File: "m.go", Kind: "edge_case", Detail: "overflow is not handled"},
			{Severity: "low", Detail: "naming"},
		},
		Requirements: []ReviewedRequirement{{Requirement: "Mul multiplies", Met: false, Evidence: "returns a + b"}, {Requirement: "Add unchanged", Met: true}},
	}
	msg := reviewMessage(r)
	for _, want := range []string{"[verify]", "[high] m.go:7 (logic): Mul adds", "[medium]", "Mul multiplies", "returns a + b"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("message lacks %q:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, "naming") || strings.Contains(msg, "Add unchanged") {
		t.Fatalf("message lists low issues or met requirements:\n%s", msg)
	}
	minor := Review{Verdict: "approve", Issues: []ReviewIssue{{Severity: "medium", Detail: "could be faster"}}}
	if msg := reviewMessage(minor); msg != "" {
		t.Fatalf("blocked without a high issue:\n%s", msg)
	}
}

type fakeReviewer struct {
	review Review
	err    error
	got    []ReviewRequest
}

func (f *fakeReviewer) Review(_ context.Context, req ReviewRequest) (Review, error) {
	f.got = append(f.got, req)
	return f.review, f.err
}

func verifiedTurn() []llm.Message {
	return transcript(editA, step{name: "execute", args: goTest, result: goPassed})
}

func TestVerifyGateAsksTheReviewerOnce(t *testing.T) {
	rev := &fakeReviewer{review: Review{Verdict: "request_changes", Issues: []ReviewIssue{{Severity: "high", File: "a.go", Detail: "wrong constant"}}}}
	g := VerifyGate{Reviewer: rev}
	msgs := verifiedTurn()
	state := &rt.State{Messages: msgs, Values: map[string]any{RepairStateKey: RepairRecord{Hypothesis: "Two returns 3"}}}
	cmd, err := g.AfterAgent(context.Background(), state, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cmd == nil || cmd.Update.Messages[0].Metadata[GateKindKey] != gateKindReview || !strings.Contains(cmd.Update.Messages[0].Text(), "wrong constant") {
		t.Fatalf("cmd = %+v", cmd)
	}
	if len(rev.got) != 1 || rev.got[0].Task != "fix the bug" || !slices.Equal(rev.got[0].Files, []string{"/a.go"}) || rev.got[0].Record.Hypothesis != "Two returns 3" || rev.got[0].Check != "go test ./..." {
		t.Fatalf("request = %+v", rev.got)
	}
	state.Messages = append(slices.Clone(msgs[:len(msgs)-1]), cmd.Update.Messages[0], llm.NewAssistantMessage("fixed"))
	if cmd, _ := g.AfterAgent(context.Background(), state, nil); cmd != nil {
		t.Fatalf("reviewed twice: %+v", cmd)
	}
}

func TestVerifyGateFinishesWhenTheReviewerFails(t *testing.T) {
	var got error
	rev := &fakeReviewer{err: errors.New("model down")}
	g := VerifyGate{Reviewer: rev, OnReview: func(_ Review, err error) { got = err }}
	if cmd, err := g.AfterAgent(context.Background(), &rt.State{Messages: verifiedTurn()}, nil); err != nil || cmd != nil {
		t.Fatalf("cmd = %+v, err = %v", cmd, err)
	}
	if got == nil {
		t.Fatal("OnReview did not get the error")
	}
}

func structuredCall(name string, args map[string]any) llm.Message {
	return llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "s", Name: name, Args: args}}}
}

func TestModelReviewerReturnsTheStructuredReview(t *testing.T) {
	dir := goRepo(t, nil)
	model := llm.NewFakeModel(
		llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "d", Name: "git_diff", Args: map[string]any{}}}},
		structuredCall(reviewToolName, map[string]any{
			"verdict":      "request_changes",
			"issues":       []any{map[string]any{"severity": "high", "file": "m.go", "line": 3, "kind": "logic", "detail": "wrong"}},
			"requirements": []any{map[string]any{"requirement": "Mul multiplies", "met": false, "evidence": "adds"}},
		}),
	)
	b := NewBackend(dir)
	rev, err := NewModelReviewer(model, b)
	if err != nil {
		t.Fatal(err)
	}
	r, err := rev.Review(context.Background(), ReviewRequest{Task: "add Mul", Files: []string{"m.go"}, Check: "go test ./...", Record: RepairRecord{Hypothesis: "h"}})
	if err != nil {
		t.Fatal(err)
	}
	if r.Verdict != "request_changes" || len(r.Issues) != 1 || r.Issues[0].Line != 3 || len(r.Requirements) != 1 || r.Requirements[0].Met {
		t.Fatalf("review = %+v", r)
	}
	first := model.Requests()[0]
	var tools []string
	for _, s := range first.Tools {
		tools = append(tools, s.Name)
	}
	for _, name := range tools {
		if name != reviewToolName && !slices.Contains(reviewerTools, name) {
			t.Fatalf("reviewer offered %q (tools %v)", name, tools)
		}
	}
	prompt := first.Messages[len(first.Messages)-1].Text()
	for _, want := range []string{"add Mul", "m.go", "go test ./...", "hypothesis"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("review request lacks %q:\n%s", want, prompt)
		}
	}
}

func TestAgentFixesWhatTheReviewerFound(t *testing.T) {
	dir := goRepo(t, nil)
	call := func(id, name string, args map[string]any) llm.Message {
		return llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: id, Name: name, Args: args}}}
	}
	model := llm.NewFakeModel(
		call("r", "read_file", map[string]any{"file_path": "/m.go"}),
		call("e1", "edit_file", map[string]any{"file_path": "/m.go", "old_string": "func Sub", "new_string": "func Mul(a, b int) int { return a + b }\n\nfunc Sub"}),
		call("t1", "execute", goTest),
		llm.NewAssistantMessage("Added Mul."),
		call("e2", "edit_file", map[string]any{"file_path": "/m.go", "old_string": "Mul(a, b int) int { return a + b }", "new_string": "Mul(a, b int) int { return a * b }"}),
		call("t2", "execute", goTest),
		llm.NewAssistantMessage("Fixed Mul."),
	)
	reviewer := llm.NewFakeModel(structuredCall(reviewToolName, map[string]any{
		"verdict": "request_changes",
		"issues":  []any{map[string]any{"severity": "high", "file": "m.go", "line": 7, "kind": "logic", "detail": "Mul returns a + b"}},
	}))
	a, err := CreateCLIAgent(context.Background(), Options{Model: model, ReviewModel: reviewer, Cwd: dir, Backend: NewBackend(dir), AutoApprove: true, NoWebTools: true,
		Disable: []string{FeaturePlanner}})
	if err != nil {
		t.Fatal(err)
	}
	st, err := a.Invoke(context.Background(), core.Input{Messages: []llm.Message{llm.NewUserMessage("add Mul(a, b) that multiplies")}}, rt.RunConfig{})
	if err != nil {
		t.Fatal(err)
	}
	var review string
	for _, m := range st.Messages {
		if m.Metadata[GateKindKey] == gateKindReview {
			review = m.Text()
		}
	}
	if !strings.Contains(review, "Mul returns a + b") {
		t.Fatalf("no review message; last: %s", st.Messages[len(st.Messages)-1].Text())
	}
	if got := readFile(t, filepath.Join(dir, "m.go")); !strings.Contains(got, "return a * b") {
		t.Fatalf("m.go = %s", got)
	}
	if last := st.Messages[len(st.Messages)-1].Text(); last != "Fixed Mul." {
		t.Fatalf("last = %q", last)
	}
}
