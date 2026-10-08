package bench

import (
	"maps"
	"testing"

	codeagent "github.com/tsengmingchun/quill/apps/agent/code/agent"
	"github.com/tsengmingchun/quill/apps/agent/code/cost"
	"github.com/tsengmingchun/quill/apps/agent/code/trace"
	"github.com/tsengmingchun/quill/apps/agent/llm"
	rt "github.com/tsengmingchun/quill/apps/agent/runtime"
)

func toolMsg(id, text string) llm.Message {
	return llm.Message{Role: llm.RoleTool, ToolCallID: id, Content: []llm.ContentBlock{llm.TextBlock(text)}}
}

func synthetic(source, kind, text string) llm.Message {
	m := llm.NewUserMessage(text)
	m.Metadata = map[string]any{rt.SyntheticSourceKey: source}
	if kind != "" {
		m.Metadata[codeagent.GateKindKey] = kind
	}
	return m
}

func detail(msgs ...llm.Message) trace.LLMCallDetail {
	return trace.LLMCallDetail{LLMCall: trace.LLMCall{MessageCount: len(msgs)}, Messages: msgs}
}

func TestAnalyzeKeepsRecordedBaselineVerdicts(t *testing.T) {
	a := Attempt{InstanceID: "instance_x", Process: &Process{BaselineVerdicts: []BaselineVerdict{
		{Verdict: "preexisting", Tests: []string{"export posts"}}, {Verdict: "flaky"},
	}}}
	got := AnalyzeAttempt(a, Task{}, nil, []trace.LLMCallDetail{detail(llm.NewSystemMessage("sys"))}, nil, cost.Table{})
	if got.Triggers["baseline_preexisting"] != 1 || got.Triggers["baseline_flaky"] != 1 {
		t.Fatalf("triggers = %v", got.Triggers)
	}
	if v := got.Metrics.Process.BaselineVerdicts; len(v) != 2 || v[0].Tests[0] != "export posts" {
		t.Fatalf("verdicts = %+v", v)
	}
}

func TestTriggersCountsEachMarkerOnce(t *testing.T) {
	sys := llm.NewSystemMessage("sys")
	refusal := toolMsg("c1", "[progress] Not run: this exact run_tests call failed twice")
	note := toolMsg("c2", "FAIL x\n\n[progress] This failed the same way as before.")
	guard := toolMsg("c3", "Not run: deletes the project, and tool calls are auto-approved, so nobody confirmed it.")
	version := toolMsg("c4", "Error: /a.go changed since you last read it (by a command, the user or another editor). Read it again before changing it.")
	regex := toolMsg("c5", "a.go:1: x\n\nNote: the pattern matched nothing as literal text, so these are its matches as an RE2 regular expression.")
	hint := toolMsg("c6", "Error: connect ECONNREFUSED\n\n[hint] Connection refused on port 6379 (redis): the service is not running.")
	cut := toolMsg("c7", "head\n\n... Output truncated at 100000 bytes: 5 bytes were omitted")
	gate := synthetic(codeagent.VerifyGateName, "baseline", "These tests pass on HEAD")
	budget := synthetic(codeagent.BudgetSource, "", "80% of the time is used")

	first := []llm.Message{sys, refusal, note, guard, version, regex, hint, cut, gate}
	later := append(append([]llm.Message{}, first...), budget)
	calls := []trace.LLMCallDetail{detail(first...), detail(later...), detail(later...)}
	got := TriggersFromTrace(calls)
	want := Triggers{
		TriggerProgressRefusal: 1, TriggerProgressNote: 1, TriggerDestructive: 1, TriggerVersionGuard: 1,
		TriggerGrepRegex: 1, TriggerServiceHint: 1, TriggerShellTruncated: 1, "gate_baseline": 1,
		TriggerBudget: 2,
	}
	if !maps.Equal(got, want) {
		t.Fatalf("triggers = %v\nwant       %v", got, want)
	}
}

func TestTriggersCountsRepairLoopNotes(t *testing.T) {
	sys := llm.NewSystemMessage("sys")
	fail := toolMsg("c1", "FAIL\n\n[repair] {\"category\":\"assertion\",\"tests\":[\"TestA\"]}")
	progressed := toolMsg("c2", "FAIL\n\n[repair] {\"category\":\"assertion\",\"delta\":{\"kind\":\"progressed\",\"fixed\":[\"TestA\"]}}\n\n"+
		"[checkpoint] {\"id\":\"cp2\",\"commit\":\"abc\",\"reason\":\"attempt 1: progressed\"}\nSaved the workspace as cp2; the rollback tool restores it.")
	unchanged := toolMsg("c3", "FAIL\n\n[repair] {\"category\":\"assertion\",\"delta\":{\"kind\":\"unchanged\"}}\n\n"+
		"[stuck] {\"rule\":\"same_failure\",\"detail\":\"...\",\"resets\":1}\nStop patching and change strategy")
	refused := toolMsg("c4", "[stuck] Edit not applied: you are stuck (same_failure: ...)")
	rolledBack := toolMsg("c5", "Error: edit rolled back: it introduced a syntax error\n\n[micro] {\"status\":\"rolled_back\",\"diagnostics\":[]}")
	compile := toolMsg("c6", "Successfully replaced\n\n[micro] {\"status\":\"failed\",\"diagnostics\":[]}")
	repro := toolMsg("c7", "Successfully replaced\n\n[repro] You edited code before any check in this turn failed.\n\n"+
		"[micro] {\"status\":\"environment\",\"env\":\"network\",\"diagnostics\":[]}")
	widen := synthetic(codeagent.VerifyGateName, "widen", "[verify] Your checks pass, but verification only reached: package")
	review := synthetic(codeagent.VerifyGateName, "review", "[verify] An independent review of your change found problems.")
	msgs := []llm.Message{sys, fail, progressed, unchanged, refused, rolledBack, compile, repro, widen, review}

	main := detail(msgs...)
	main.Response = &llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{
		{ID: "r1", Name: codeagent.RepairToolName}, {ID: "r2", Name: codeagent.RollbackToolName}, {ID: "r3", Name: "read_file"},
	}}
	sub := detail(sys)
	sub.Namespace = []string{"reviewer"}
	sub.Response = &llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "s1", Name: codeagent.RepairToolName}}}

	got := TriggersFromTrace([]trace.LLMCallDetail{main, detail(msgs...), sub})
	want := Triggers{
		"class_assertion": 3, "delta_progressed": 1, "delta_unchanged": 1,
		TriggerCheckpoint: 1, TriggerStuckReset: 1, TriggerStuckRefusal: 1,
		"micro_rolled_back": 1, "micro_failed": 1, "micro_environment": 1, TriggerRepro: 1,
		"gate_widen": 1, "gate_review": 1, TriggerRepairTool: 1, TriggerRollback: 1,
	}
	if !maps.Equal(got, want) {
		t.Fatalf("triggers = %v\nwant       %v", got, want)
	}
}

func TestAnalyzeCountsRecordedReviews(t *testing.T) {
	a := Attempt{InstanceID: "instance_x", Process: &Process{Reviews: []ReviewVerdict{
		{Verdict: "request_changes", High: 1}, {Verdict: "approve"}, {Error: "model failed"},
	}}}
	got := AnalyzeAttempt(a, Task{}, nil, []trace.LLMCallDetail{detail(llm.NewSystemMessage("sys"))}, nil, cost.Table{})
	want := map[string]int{"review_request_changes": 1, "review_approve": 1, "review_error": 1}
	for k, v := range want {
		if got.Triggers[k] != v {
			t.Fatalf("triggers = %v, want %s = %d", got.Triggers, k, v)
		}
	}
	if r := got.Metrics.Process.Reviews; len(r) != 3 || r[0].High != 1 {
		t.Fatalf("reviews = %+v", r)
	}
}
