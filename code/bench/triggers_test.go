package bench

import (
	"maps"
	"testing"

	codeagent "github.com/tsengmingchun/quill/code/agent"
	"github.com/tsengmingchun/quill/code/cost"
	"github.com/tsengmingchun/quill/code/trace"
	"github.com/tsengmingchun/quill/llm"
	rt "github.com/tsengmingchun/quill/runtime"
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
