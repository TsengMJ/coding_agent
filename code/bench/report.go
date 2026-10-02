package bench

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Rate is a resolve rate with its 95% Wilson interval.
type Rate struct {
	Resolved int
	Total    int
	Lo, Hi   float64
}

func newRate(k, n int) Rate {
	lo, hi := Wilson(k, n)
	return Rate{Resolved: k, Total: n, Lo: lo, Hi: hi}
}

// Pct is the rate as a percentage.
func (r Rate) Pct() float64 {
	if r.Total == 0 {
		return 0
	}
	return 100 * float64(r.Resolved) / float64(r.Total)
}

func (r Rate) String() string {
	return fmt.Sprintf("%d/%d = %.1f%% [%.1f, %.1f]", r.Resolved, r.Total, r.Pct(), 100*r.Lo, 100*r.Hi)
}

// Summary aggregates one run. Infra errors are excluded from every rate.
type Summary struct {
	Attempts   int
	InfraError int
	// Overall counts scored attempts; with several trials, PassAtK counts a
	// task as resolved when any trial resolved it.
	Overall    Rate
	PassAtK    Rate
	ByLanguage map[string]Rate
	ByRepo     map[string]Rate
	ByHard     map[string]Rate
	Categories map[string]int
	// Medians over scored attempts.
	MedianSteps    float64
	MedianAgentMin float64
	MedianTokensIn float64
	MedianCostUSD  float64
	TotalCostUSD   float64
	MeanGoldRecall float64
	// GoldRecallN counts attempts whose patch had a defined gold-file recall.
	GoldRecallN     int
	MeanTestRuns    float64
	TouchesTestsPct float64
	EmptyPatchPct   float64
	// Delegation: share of attempts that called task, and per-attempt means.
	TaskUsePct           float64
	MeanTaskCalls        float64
	MeanSubagentLLMCalls float64
}

// Summarize aggregates attempts; tasks provides language, repo and hard.
func Summarize(attempts []Attempt, tasks map[string]Task) Summary {
	s := Summary{ByLanguage: map[string]Rate{}, ByRepo: map[string]Rate{}, ByHard: map[string]Rate{}, Categories: map[string]int{}}
	type counts struct{ k, n int }
	lang, repo, hard := map[string]*counts{}, map[string]*counts{}, map[string]*counts{}
	bump := func(m map[string]*counts, key string, ok bool) {
		c := m[key]
		if c == nil {
			c = &counts{}
			m[key] = c
		}
		c.n++
		if ok {
			c.k++
		}
	}
	anyResolved := map[string]bool{}
	var steps, agentMin, tokens, costs []float64
	var recallSum float64
	var recallN, testRuns, touches, empty, k, n, taskUsers, taskCalls, subCalls int
	for _, a := range attempts {
		if a.Phase != PhaseDone {
			continue
		}
		s.Attempts++
		s.Categories[a.FailureCategory]++
		if a.FailureCategory == CategoryInfraError {
			s.InfraError++
			continue
		}
		n++
		if a.Resolved {
			k++
		}
		t := tasks[a.InstanceID]
		bump(lang, t.Bucket(), a.Resolved)
		bump(repo, t.Repo, a.Resolved)
		bump(hard, map[bool]string{true: "hard", false: "not hard"}[t.Hard], a.Resolved)
		anyResolved[a.InstanceID] = anyResolved[a.InstanceID] || a.Resolved
		steps = append(steps, float64(a.Steps))
		agentMin = append(agentMin, float64(a.AgentMS)/60000)
		tokens = append(tokens, float64(a.TokensIn))
		if a.CostUSD != nil {
			costs = append(costs, *a.CostUSD)
			s.TotalCostUSD += *a.CostUSD
		}
		if a.GoldRecall != nil {
			recallSum += *a.GoldRecall
			recallN++
		}
		testRuns += a.TestRuns
		if c := a.ToolCalls["task"]; c > 0 {
			taskUsers++
			taskCalls += c
		}
		subCalls += a.SubagentLLMCalls
		if a.TouchesTests {
			touches++
		}
		if strings.TrimSpace(a.Patch) == "" {
			empty++
		}
	}
	s.Overall = newRate(k, n)
	solved := 0
	for _, ok := range anyResolved {
		if ok {
			solved++
		}
	}
	s.PassAtK = newRate(solved, len(anyResolved))
	for key, c := range lang {
		s.ByLanguage[key] = newRate(c.k, c.n)
	}
	for key, c := range repo {
		s.ByRepo[key] = newRate(c.k, c.n)
	}
	for key, c := range hard {
		s.ByHard[key] = newRate(c.k, c.n)
	}
	s.MedianSteps, s.MedianAgentMin = Median(steps), Median(agentMin)
	s.MedianTokensIn, s.MedianCostUSD = Median(tokens), Median(costs)
	s.GoldRecallN = recallN
	if recallN > 0 {
		s.MeanGoldRecall = recallSum / float64(recallN)
	}
	if n > 0 {
		s.MeanTestRuns = float64(testRuns) / float64(n)
		s.TouchesTestsPct = 100 * float64(touches) / float64(n)
		s.EmptyPatchPct = 100 * float64(empty) / float64(n)
		s.TaskUsePct = 100 * float64(taskUsers) / float64(n)
		s.MeanTaskCalls = float64(taskCalls) / float64(n)
		s.MeanSubagentLLMCalls = float64(subCalls) / float64(n)
	}
	return s
}

// TaskOutcome is one side's per-task score: the share of scored trials
// that resolved it.
type TaskOutcome map[string]float64

// Outcomes scores every task with at least one scored attempt.
func Outcomes(attempts []Attempt) TaskOutcome {
	k, n := map[string]int{}, map[string]int{}
	for _, a := range attempts {
		if a.Phase != PhaseDone || a.FailureCategory == CategoryInfraError {
			continue
		}
		n[a.InstanceID]++
		if a.Resolved {
			k[a.InstanceID]++
		}
	}
	out := TaskOutcome{}
	for id, total := range n {
		out[id] = float64(k[id]) / float64(total)
	}
	return out
}

// ReferenceOutcomes turns published grades into outcomes.
func ReferenceOutcomes(refs []Reference) TaskOutcome {
	out := TaskOutcome{}
	for _, r := range refs {
		out[r.InstanceID] = map[bool]float64{true: 1, false: 0}[r.Resolved]
	}
	return out
}

// Comparison pairs two sides on the tasks both scored.
type Comparison struct {
	Shared     int
	A, B       Rate
	OnlyA      []string
	OnlyB      []string
	McNemarP   float64
	DiffLo     float64
	DiffHi     float64
	DiffPoints float64
}

// Compare pairs a and b; a task counts as resolved when at least half of
// its trials resolved it.
func Compare(a, b TaskOutcome) Comparison {
	var ids []string
	for id := range a {
		if _, ok := b[id]; ok {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	c := Comparison{Shared: len(ids)}
	as, bs := make([]float64, len(ids)), make([]float64, len(ids))
	var ka, kb int
	for i, id := range ids {
		as[i], bs[i] = a[id], b[id]
		ra, rb := a[id] >= 0.5, b[id] >= 0.5
		if ra {
			ka++
		}
		if rb {
			kb++
		}
		switch {
		case ra && !rb:
			c.OnlyA = append(c.OnlyA, id)
		case rb && !ra:
			c.OnlyB = append(c.OnlyB, id)
		}
	}
	c.A, c.B = newRate(ka, len(ids)), newRate(kb, len(ids))
	c.McNemarP = McNemar(len(c.OnlyA), len(c.OnlyB))
	c.DiffLo, c.DiffHi = BootstrapDiff(as, bs, 10000, 1)
	c.DiffLo, c.DiffHi = 100*c.DiffLo, 100*c.DiffHi
	c.DiffPoints = c.B.Pct() - c.A.Pct()
	return c
}

// RenderReport formats a run summary as markdown.
func RenderReport(run *Run, s Summary) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# dbench report: %s\n\n", run.ID)
	fmt.Fprintf(&b, "| | |\n|---|---|\n")
	fmt.Fprintf(&b, "| benchmark | %s / %s |\n| solver | %s |\n| model | %s |\n| trials | %d |\n", run.Benchmark, run.Subset, run.Solver, orDash(run.Model), run.Trials)
	fmt.Fprintf(&b, "| status | %s |\n| started | %s |\n", run.Status, run.StartedAt.Format(time.RFC3339))
	commit := run.GitCommit
	if len(commit) > 12 {
		commit = commit[:12]
	}
	if run.GitDirty {
		commit += " (dirty)"
	}
	fmt.Fprintf(&b, "| dcode commit | %s |\n| tasks commit | %s |\n", orDash(commit), orDash(run.TasksRepoCommit))
	if run.Notes != "" {
		fmt.Fprintf(&b, "| notes | %s |\n", run.Notes)
	}
	fmt.Fprintf(&b, "\n## Resolve rate\n\n**%s** (95%% Wilson interval), %d attempts, %d infra errors excluded.\n", s.Overall, s.Attempts, s.InfraError)
	if run.Trials > 1 {
		fmt.Fprintf(&b, "\npass@%d: %s\n", run.Trials, s.PassAtK)
	}
	writeRates(&b, "By language", s.ByLanguage)
	writeRates(&b, "By difficulty", s.ByHard)
	writeRates(&b, "By repository", s.ByRepo)
	b.WriteString("\n## Failure categories\n\n| category | attempts |\n|---|---|\n")
	for _, k := range sortedKeys(s.Categories) {
		fmt.Fprintf(&b, "| %s | %d |\n", k, s.Categories[k])
	}
	b.WriteString("\n## How the agent worked (scored attempts)\n\n| metric | value |\n|---|---|\n")
	fmt.Fprintf(&b, "| median steps | %.0f |\n| median agent minutes | %.1f |\n| median input tokens | %.0f |\n", s.MedianSteps, s.MedianAgentMin, s.MedianTokensIn)
	fmt.Fprintf(&b, "| median cost | $%.3f |\n| total cost | $%.2f |\n", s.MedianCostUSD, s.TotalCostUSD)
	recall := "-"
	if s.GoldRecallN > 0 {
		recall = fmt.Sprintf("%.2f (%d patches)", s.MeanGoldRecall, s.GoldRecallN)
	}
	fmt.Fprintf(&b, "| mean test runs | %.1f |\n| mean gold-file recall | %s |\n", s.MeanTestRuns, recall)
	fmt.Fprintf(&b, "| patches touching tests | %.0f%% |\n| empty patches | %.0f%% |\n", s.TouchesTestsPct, s.EmptyPatchPct)
	fmt.Fprintf(&b, "| attempts using subagents | %.0f%% |\n| mean task calls | %.1f |\n| mean subagent model calls | %.1f |\n",
		s.TaskUsePct, s.MeanTaskCalls, s.MeanSubagentLLMCalls)
	return b.String()
}

// RenderComparison formats a comparison as markdown.
func RenderComparison(nameA, nameB string, c Comparison) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s vs %s\n\n%d shared tasks.\n\n", nameA, nameB, c.Shared)
	fmt.Fprintf(&b, "| side | resolve rate |\n|---|---|\n| %s | %s |\n| %s | %s |\n\n", nameA, c.A, nameB, c.B)
	fmt.Fprintf(&b, "Difference (B - A): **%+.1f points**, paired bootstrap 95%% interval [%+.1f, %+.1f]; exact McNemar p = %.3f.\n",
		c.DiffPoints, c.DiffLo, c.DiffHi, c.McNemarP)
	writeList(&b, "Resolved only by "+nameA, c.OnlyA)
	writeList(&b, "Resolved only by "+nameB, c.OnlyB)
	return b.String()
}

func writeRates(b *strings.Builder, title string, rates map[string]Rate) {
	if len(rates) == 0 {
		return
	}
	fmt.Fprintf(b, "\n### %s\n\n| group | resolved |\n|---|---|\n", title)
	for _, k := range sortedKeys(rates) {
		fmt.Fprintf(b, "| %s | %s |\n", k, rates[k])
	}
}

func writeList(b *strings.Builder, title string, ids []string) {
	fmt.Fprintf(b, "\n## %s (%d)\n\n", title, len(ids))
	for _, id := range ids {
		fmt.Fprintf(b, "- %s\n", id)
	}
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
