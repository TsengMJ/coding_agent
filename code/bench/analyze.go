package bench

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/tsengmingchun/quill/code/cost"
	"github.com/tsengmingchun/quill/code/trace"
	"github.com/tsengmingchun/quill/llm"
)

// Observed failure classes: what the grade and the trace show, not a root
// cause. The report leaves a column for the root cause a human infers.
const (
	ObservedResolved            = "resolved"
	ObservedBudget              = "budget"
	ObservedModelError          = "model_error"
	ObservedInfraError          = "infra_error"
	ObservedAgentError          = "agent_error"
	ObservedEmptyPatch          = "empty_patch"
	ObservedApplyFailed         = "patch_apply_failed"
	ObservedRegressionDismissed = "regression_dismissed"
	ObservedRegressionUnnoticed = "regression_unnoticed"
	ObservedWrongSolution       = "wrong_solution"
	ObservedLocalization        = "localization"
	ObservedUnknown             = "unknown"
)

// loopRepeat is how many identical main-agent calls flag an attempt as looping.
const loopRepeat = 5

// unexplainedJump is the input-token growth between consecutive main-agent
// calls, beyond the new messages and the previous reply, that the report
// counts: a sign the provider resent or re-tokenized hidden content.
const unexplainedJump = 10_000

// AttemptAnalysis is one attempt recomputed from its trace.
type AttemptAnalysis struct {
	Attempt  Attempt
	Metrics  Metrics
	Observed string
	Loop     bool
	// FailingP2P are the pass-to-pass tests the patch broke.
	FailingP2P []string
	// UnexplainedJumps counts main-agent calls whose input grew by more than
	// unexplainedJump tokens beyond what the new messages account for;
	// MaxUnexplained is the largest such growth.
	UnexplainedJumps int
	MaxUnexplained   int
	// Progress is the offline count of progress monitor interventions.
	Progress *ProgressReplay
	// Triggers counts what the agent's components actually did in the trace.
	Triggers Triggers
	// TraceError is set when the trace could not be read; Metrics then
	// holds the stored values.
	TraceError string
}

// AnalyzeAttempt recomputes metrics from calls and classifies the outcome.
// A nil calls with traceErr keeps the stored metrics.
func AnalyzeAttempt(a Attempt, task Task, tests []TestResult, calls []trace.LLMCallDetail, traceErr error, prices cost.Table) AttemptAnalysis {
	out := AttemptAnalysis{Attempt: a, Metrics: a.Metrics}
	for _, t := range tests {
		if t.Kind == "p2p" && t.Status != "PASSED" {
			out.FailingP2P = append(out.FailingP2P, t.Name)
		}
	}
	if traceErr != nil {
		out.TraceError = traceErr.Error()
	} else {
		out.Metrics = MetricsFromTrace(calls, prices)
		out.Metrics.AgentMS, out.Metrics.GradeMS = a.AgentMS, a.GradeMS
		out.Metrics.Process = ProcessFromTrace(calls, task.GoldPaths)
		if a.Process != nil {
			out.Metrics.Process.GoldSrcRecall = a.Process.GoldSrcRecall
		}
		if out.Metrics.Process.GoldSrcRecall == nil {
			if rc, ok := SourceRecall(ParsePatch(a.Patch).Files, task.GoldPaths); ok {
				out.Metrics.Process.GoldSrcRecall = &rc
			}
		}
		out.Loop = out.Metrics.Process.MaxRepeat >= loopRepeat
		out.UnexplainedJumps, out.MaxUnexplained = inputJumps(calls)
		progress := ReplayProgress(calls)
		out.Progress = &progress
		out.Triggers = TriggersFromTrace(calls)
		if a.Process != nil {
			out.Metrics.Process.BaselineVerdicts = a.Process.BaselineVerdicts
			for _, v := range a.Process.BaselineVerdicts {
				out.Triggers["baseline_"+v.Verdict]++
			}
		}
	}
	out.Observed = ObservedFailure(a, task, out.FailingP2P, toolOutputs(calls))
	return out
}

// ObservedFailure classifies an attempt. outputs are the tool results the
// agent saw; a broken test named in them was seen and then dismissed.
func ObservedFailure(a Attempt, task Task, failingP2P, outputs []string) string {
	switch {
	case a.Resolved:
		return ObservedResolved
	case a.FailureCategory == CategoryAgentTimeout || a.FailureCategory == CategoryBudget:
		return ObservedBudget
	case a.FailureCategory == CategoryModelError:
		return ObservedModelError
	case a.FailureCategory == CategoryInfraError:
		return ObservedInfraError
	case strings.TrimSpace(a.Patch) == "":
		return ObservedEmptyPatch
	case a.FailureCategory == CategoryApplyFailed:
		return ObservedApplyFailed
	case a.F2PTotal > 0 && a.F2PPassed == a.F2PTotal && len(failingP2P) > 0:
		for _, name := range failingP2P {
			leaf := testLeaf(name)
			if slices.ContainsFunc(outputs, func(o string) bool { return strings.Contains(o, leaf) }) {
				return ObservedRegressionDismissed
			}
		}
		return ObservedRegressionUnnoticed
	case a.F2PPassed < a.F2PTotal:
		files := ParsePatch(a.Patch).Files
		for _, g := range GoldSourcePaths(task.GoldPaths) {
			if slices.Contains(files, g) {
				return ObservedWrongSolution
			}
		}
		return ObservedLocalization
	case a.FailureCategory == CategoryAgentError:
		return ObservedAgentError
	}
	return ObservedUnknown
}

// testLeaf is the part of a test ID a runner prints: the title after the
// file for mocha ("file | title"), the function for pytest ("a::B::c").
// Long titles keep their tail, which runners rarely truncate.
func testLeaf(name string) string {
	if i := strings.LastIndex(name, " | "); i >= 0 {
		name = name[i+3:]
	}
	if i := strings.LastIndex(name, "::"); i >= 0 {
		name = name[i+2:]
	}
	if len(name) > 60 {
		name = name[len(name)-60:]
	}
	return strings.TrimSpace(name)
}

func toolOutputs(calls []trace.LLMCallDetail) []string {
	seen := map[string]bool{}
	var out []string
	for _, c := range calls {
		for _, m := range c.Messages {
			if m.Role == llm.RoleTool && !seen[m.ToolCallID] {
				seen[m.ToolCallID] = true
				out = append(out, m.Text())
			}
		}
	}
	return out
}

// inputJumps compares each main-agent call's input tokens with the previous
// call's, minus the previous reply and a 4-chars-per-token estimate of the
// messages added since. Summarization shrinks the prompt and never counts.
func inputJumps(calls []trace.LLMCallDetail) (count, largest int) {
	var prev *trace.LLMCallDetail
	for i := range calls {
		c := &calls[i]
		if len(c.Namespace) != 0 || c.Usage == nil {
			continue
		}
		if prev != nil && prev.Usage != nil {
			added := 0
			for _, m := range c.Messages[min(c.NewFrom, len(c.Messages)):] {
				added += len(m.Text()) / 4
				for _, tc := range m.ToolCalls {
					added += len(hashJSON(tc.Args)) / 4
				}
			}
			extra := c.Usage.InputTokens - prev.Usage.InputTokens - prev.Usage.OutputTokens - added
			if extra > unexplainedJump {
				count++
				largest = max(largest, extra)
			}
		}
		prev = c
	}
	return count, largest
}

// AnalyzeMarkdown renders the analysis of one run.
func AnalyzeMarkdown(run Run, rows []AttemptAnalysis) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Analysis of %s\n\n", run.ID)
	fmt.Fprintf(&b, "Model `%s`, subset `%s`, commit `%s` (dirty: %v).\n\n", run.Model, run.Subset, shortSHA(run.GitCommit), run.GitDirty)
	b.WriteString("Metrics are recomputed from the trace; `stored tests` is the value the run recorded.\n")
	b.WriteString("`observed` is what the grade and trace show; fill `root cause` by reading the trace.\n\n")
	b.WriteString("| task | t | observed | loop | steps | calls | tests | stored tests | dup | max rep | todo-only | verify | tokens in (M) | cache % | peak ctx (k) | jumps | gold src recall | first gold touch | tool wall s / sum s | root cause |\n")
	b.WriteString("|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|\n")
	counts := map[string]int{}
	outChars := map[string]int{}
	for _, r := range rows {
		counts[r.Observed]++
		m, p := r.Metrics, r.Metrics.Process
		if p == nil {
			p = &Process{}
		}
		for k, v := range p.ToolOutputChars {
			outChars[k] += v
		}
		loop := ""
		if r.Loop {
			loop = "yes"
		}
		if r.TraceError != "" {
			loop = "trace: " + r.TraceError
		}
		fmt.Fprintf(&b, "| %s | %d | %s | %s | %d | %d | %d | %d | %d | %d | %d | %d | %.2f | %s | %d | %d | %s | %s | %.0f / %.0f | |\n",
			shortInstance(r.Attempt.InstanceID), r.Attempt.Trial, r.Observed, loop, m.Steps, m.LLMCalls, m.TestRuns,
			r.Attempt.TestRuns, p.DuplicateToolCalls, p.MaxRepeat, p.TodoOnlyTurns, p.VerifyBlocks,
			float64(m.TokensIn)/1e6, cachePct(m), m.PeakContextTokens/1000, r.UnexplainedJumps,
			fmtFloatPtr(p.GoldSrcRecall), fmtIntPtr(p.FirstGoldSrcTouchStep),
			float64(p.ToolWallMS)/1000, float64(m.ToolMS)/1000)
	}
	b.WriteString("\n## Observed outcomes\n\n")
	for _, k := range sortedKeys(counts) {
		fmt.Fprintf(&b, "- %s: %d\n", k, counts[k])
	}
	b.WriteString("\n## Broken pass-to-pass tests\n\n")
	for _, r := range rows {
		if len(r.FailingP2P) == 0 {
			continue
		}
		fmt.Fprintf(&b, "- %s#%d (%s):\n", shortInstance(r.Attempt.InstanceID), r.Attempt.Trial, r.Observed)
		for _, t := range r.FailingP2P[:min(len(r.FailingP2P), 5)] {
			fmt.Fprintf(&b, "  - `%s`\n", t)
		}
		if len(r.FailingP2P) > 5 {
			fmt.Fprintf(&b, "  - ... %d more\n", len(r.FailingP2P)-5)
		}
	}
	b.WriteString("\n## Tool output share\n\n")
	total := 0
	for _, v := range outChars {
		total += v
	}
	keys := sortedKeys(outChars)
	sort.SliceStable(keys, func(i, j int) bool { return outChars[keys[i]] > outChars[keys[j]] })
	for _, k := range keys {
		name := k
		if name == "" {
			name = "(unknown)"
		}
		fmt.Fprintf(&b, "- %s: %d chars (%.0f%%)\n", name, outChars[k], 100*float64(outChars[k])/float64(max(total, 1)))
	}
	writeProgressReplay(&b, rows)
	writeTriggers(&b, rows)
	return b.String()
}

// triggerColumns orders the component trigger table.
var triggerColumns = []string{
	"gate_check", "gate_baseline", "gate_test_edit", "gate_diff", TriggerProgressRefusal, TriggerProgressNote,
	TriggerBudget, TriggerDestructive, TriggerVersionGuard,
	TriggerGrepRegex, TriggerServiceHint, TriggerShellTruncated,
	"baseline_regression", "baseline_preexisting", "baseline_flaky", "baseline_also_fails", "baseline_unknown",
}

// writeTriggers lists what each component did in the recorded run, for
// finding components that never fire.
func writeTriggers(b *strings.Builder, rows []AttemptAnalysis) {
	b.WriteString("\n## Component triggers\n\n")
	b.WriteString("What the agent's components did in the recorded trace: tool results and gate messages are counted once, budget notices per request; `baseline_*` are the gate's HEAD baseline verdicts the run recorded (none for runs before they were recorded).\n\n")
	b.WriteString("| task | t | observed | " + strings.Join(triggerColumns, " | ") + " |\n")
	b.WriteString("|---|---|---|" + strings.Repeat("---|", len(triggerColumns)) + "\n")
	sum := Triggers{}
	for _, r := range rows {
		if r.Triggers == nil {
			continue
		}
		fmt.Fprintf(b, "| %s | %d | %s |", shortInstance(r.Attempt.InstanceID), r.Attempt.Trial, r.Observed)
		for _, c := range triggerColumns {
			sum[c] += r.Triggers[c]
			fmt.Fprintf(b, " %d |", r.Triggers[c])
		}
		b.WriteString("\n")
	}
	b.WriteString("| total | | |")
	for _, c := range triggerColumns {
		fmt.Fprintf(b, " %d |", sum[c])
	}
	b.WriteString("\n")
}

// writeProgressReplay lists the attempts where the progress monitor would
// have stepped in.
func writeProgressReplay(b *strings.Builder, rows []AttemptAnalysis) {
	b.WriteString("\n## Progress monitor replay\n\n")
	b.WriteString("Times the monitor would have refused an unchanged repeat of a failing call, or noted a repeated failure.\n\n")
	b.WriteString("| task | t | observed | refusals | repeat notes |\n|---|---|---|---|---|\n")
	var sum ProgressReplay
	for _, r := range rows {
		p := r.Progress
		if p == nil {
			continue
		}
		sum.Refusals += p.Refusals
		sum.RepeatNotes += p.RepeatNotes
		fmt.Fprintf(b, "| %s | %d | %s | %d | %d |\n", shortInstance(r.Attempt.InstanceID), r.Attempt.Trial, r.Observed, p.Refusals, p.RepeatNotes)
	}
	fmt.Fprintf(b, "| total | | | %d | %d |\n", sum.Refusals, sum.RepeatNotes)
}

func cachePct(m Metrics) string {
	if m.TokensIn == 0 {
		return "-"
	}
	return fmt.Sprintf("%.0f", 100*float64(m.CacheRead)/float64(m.TokensIn))
}

func fmtFloatPtr(p *float64) string {
	if p == nil {
		return "-"
	}
	return fmt.Sprintf("%.2f", *p)
}

func fmtIntPtr(p *int) string {
	if p == nil {
		return "-"
	}
	return fmt.Sprint(*p)
}

func shortSHA(s string) string { return s[:min(len(s), 12)] }

// shortInstance turns instance_NodeBB__NodeBB-b398321a...-v... into NodeBB-b398.
func shortInstance(id string) string {
	s := strings.TrimPrefix(id, "instance_")
	if i := strings.Index(s, "__"); i >= 0 {
		s = s[i+2:]
	}
	if i := strings.Index(s, "-"); i >= 0 && len(s) > i+5 {
		s = s[:i+5]
	}
	return s
}
