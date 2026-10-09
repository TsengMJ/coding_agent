package bench

import (
	"math"
	"strings"
	"testing"
)

func near(a, b, tol float64) bool { return math.Abs(a-b) <= tol }

func TestWilson(t *testing.T) {
	lo, hi := Wilson(10, 50)
	if !near(lo, 0.1124, 0.001) || !near(hi, 0.3304, 0.001) {
		t.Fatalf("Wilson(10,50) = %.4f %.4f", lo, hi)
	}
	if lo, hi := Wilson(0, 0); lo != 0 || hi != 0 {
		t.Fatal("empty interval")
	}
	if lo, _ := Wilson(0, 20); lo != 0 {
		t.Fatalf("lower bound %f", lo)
	}
}

func TestMcNemar(t *testing.T) {
	if p := McNemar(0, 0); p != 1 {
		t.Fatalf("p %f", p)
	}
	// Binomial(10, .5): P(X<=1) = 11/1024.
	if p := McNemar(1, 9); !near(p, 22.0/1024, 1e-9) {
		t.Fatalf("p %f", p)
	}
	if p := McNemar(5, 5); p != 1 {
		t.Fatalf("p %f", p)
	}
}

func TestBootstrapDiffIsDeterministicAndBracketsMean(t *testing.T) {
	a := []float64{0, 0, 1, 0, 1, 0, 0, 1, 0, 0}
	b := []float64{1, 0, 1, 1, 1, 0, 1, 1, 0, 1}
	lo, hi := BootstrapDiff(a, b, 2000, 7)
	lo2, hi2 := BootstrapDiff(a, b, 2000, 7)
	if lo != lo2 || hi != hi2 {
		t.Fatal("bootstrap must be deterministic for a seed")
	}
	if !(lo <= 0.4 && 0.4 <= hi) {
		t.Fatalf("interval [%f, %f] misses the mean difference 0.4", lo, hi)
	}
}

func TestSummarizeExcludesInfraErrors(t *testing.T) {
	tasks := map[string]Task{
		"a": {InstanceID: "a", Language: "go", Repo: "r1"},
		"b": {InstanceID: "b", Language: "ts", Repo: "r2", Hard: true},
		"c": {InstanceID: "c", Language: "python", Repo: "r3"},
	}
	cost := 0.5
	attempts := []Attempt{
		{InstanceID: "a", Phase: PhaseDone, Resolved: true, FailureCategory: CategoryResolved, Patch: "diff", Metrics: Metrics{Steps: 10, CostUSD: &cost}},
		{InstanceID: "a", Trial: 1, Phase: PhaseDone, FailureCategory: CategoryIncompleteFix, Patch: "diff", Metrics: Metrics{Steps: 30}},
		{InstanceID: "b", Phase: PhaseDone, FailureCategory: CategoryEmptyPatch},
		{InstanceID: "c", Phase: PhaseDone, FailureCategory: CategoryInfraError},
		{InstanceID: "c", Trial: 1, Phase: PhaseAgentDone},
	}
	s := Summarize(attempts, tasks)
	if s.Overall.Resolved != 1 || s.Overall.Total != 3 || s.InfraError != 1 || s.Attempts != 4 {
		t.Fatalf("summary %+v", s)
	}
	if s.PassAtK.Resolved != 1 || s.PassAtK.Total != 2 {
		t.Fatalf("pass@k %+v", s.PassAtK)
	}
	if s.ByLanguage["js"].Total != 1 || s.ByHard["hard"].Total != 1 || s.ByRepo["r1"].Resolved != 1 {
		t.Fatalf("breakdowns %+v %+v %+v", s.ByLanguage, s.ByHard, s.ByRepo)
	}
	if s.MedianSteps != 10 || s.TotalCostUSD != 0.5 || !near(s.EmptyPatchPct, 100.0/3, 1e-9) || s.GoldRecallN != 0 {
		t.Fatalf("metrics %+v", s)
	}
	md := RenderReport(&Run{ID: "r", Trials: 2}, s)
	for _, want := range []string{"1/3 = 33.3%", "pass@2", "| empty_patch | 1 |", "### By language", "| mean gold-file recall | - |"} {
		if !strings.Contains(md, want) {
			t.Errorf("report lacks %q", want)
		}
	}
}

func TestSummarizeReportsDelegation(t *testing.T) {
	attempts := []Attempt{
		{InstanceID: "a", Phase: PhaseDone, Metrics: Metrics{ToolCalls: map[string]int{"task": 3}, SubagentLLMCalls: 12}},
		{InstanceID: "b", Phase: PhaseDone, Metrics: Metrics{ToolCalls: map[string]int{"read_file": 4}}},
	}
	s := Summarize(attempts, map[string]Task{})
	if s.TaskUsePct != 50 || s.MeanTaskCalls != 1.5 || s.MeanSubagentLLMCalls != 6 {
		t.Fatalf("summary %+v", s)
	}
	if md := RenderReport(&Run{ID: "r"}, s); !strings.Contains(md, "| attempts using subagents | 50% |") {
		t.Fatalf("report lacks delegation:\n%s", md)
	}
}

func TestCompareOnSharedTasks(t *testing.T) {
	a := TaskOutcome{"x": 1, "y": 0, "z": 0, "only-a": 1}
	b := TaskOutcome{"x": 1, "y": 1, "z": 0.5}
	c := Compare(a, b)
	if c.Shared != 3 || c.A.Resolved != 1 || c.B.Resolved != 3 {
		t.Fatalf("comparison %+v", c)
	}
	if len(c.OnlyB) != 2 || len(c.OnlyA) != 0 || c.McNemarP != 0.5 {
		t.Fatalf("comparison %+v", c)
	}
	md := RenderComparison("base", "new", c)
	if !strings.Contains(md, "Resolved only by new (2)") {
		t.Fatalf("markdown %s", md)
	}
}

func TestRegressedAllowsADropWithinTheTolerance(t *testing.T) {
	base := TaskOutcome{"a": 1, "b": 1, "c": 1, "d": 0}
	for _, tc := range []struct {
		next    TaskOutcome
		maxDrop float64
		want    bool
	}{
		{TaskOutcome{"a": 1, "b": 1, "c": 1, "d": 1}, 0, false},
		{TaskOutcome{"a": 1, "b": 1, "c": 0, "d": 0}, 25, false},
		{TaskOutcome{"a": 1, "b": 1, "c": 0, "d": 0}, 24, true},
		{TaskOutcome{"a": 1, "b": 0, "c": 0, "d": 1}, 0, true},
	} {
		if got := Compare(base, tc.next).Regressed(tc.maxDrop); got != tc.want {
			t.Errorf("%v with max drop %v: regressed = %v", tc.next, tc.maxDrop, got)
		}
	}
}

func TestOutcomesSkipInfraAndAverageTrials(t *testing.T) {
	o := Outcomes([]Attempt{
		{InstanceID: "a", Phase: PhaseDone, Resolved: true},
		{InstanceID: "a", Trial: 1, Phase: PhaseDone},
		{InstanceID: "b", Phase: PhaseDone, FailureCategory: CategoryInfraError},
	})
	if len(o) != 1 || o["a"] != 0.5 {
		t.Fatalf("outcomes %v", o)
	}
}

func TestParseReferences(t *testing.T) {
	refs, err := ParseReferences([]byte(`{"b": false, "a": true}`), "mini-swe-agent", "glm-5.3", "eval_results.json")
	if err != nil || len(refs) != 2 || refs[0].InstanceID != "a" || !refs[0].Resolved || refs[1].Harness != "mini-swe-agent" {
		t.Fatalf("json refs %+v %v", refs, err)
	}
	csvData := "instance_id,resolved,steps,cost_usd\na,1,40,0.8\nb,false,,\n"
	refs, err = ParseReferences([]byte(csvData), "pi", "kimi-k3", "x.csv")
	if err != nil || len(refs) != 2 || *refs[0].Steps != 40 || *refs[0].CostUSD != 0.8 || refs[1].Resolved || refs[1].Steps != nil {
		t.Fatalf("csv refs %+v %v", refs, err)
	}
	if _, err := ParseReferences([]byte("id,ok\na,1\n"), "pi", "m", ""); err == nil {
		t.Fatal("missing columns should fail")
	}
	if _, err := ParseReferences([]byte("instance_id,resolved\na,maybe\n"), "pi", "m", ""); err == nil {
		t.Fatal("non-boolean should fail")
	}
	if _, err := ParseReferences([]byte(`{}`), "", "m", ""); err == nil {
		t.Fatal("missing harness should fail")
	}
}
