package bench

import (
	"fmt"
	"strings"

	codeagent "github.com/tsengmingchun/deepagents_golang/code/agent"
	"github.com/tsengmingchun/deepagents_golang/code/trace"
	"github.com/tsengmingchun/deepagents_golang/llm"
	rt "github.com/tsengmingchun/deepagents_golang/runtime"
)

// Triggers counts how often each agent component visibly acted in an
// attempt. The architecture review uses it: a component that never fires
// is a candidate for removal.
type Triggers map[string]int

// Trigger names. Verify gate messages are counted as "gate_<kind>".
const (
	TriggerProgressRefusal = "progress_refusal"
	TriggerProgressNote    = "progress_note"
	TriggerDestructive     = "destructive_refusal"
	TriggerVersionGuard    = "version_guard"
	TriggerGrepRegex       = "grep_regex_retry"
	TriggerServiceHint     = "service_hint"
	TriggerShellTruncated  = "shell_truncated"
	TriggerBudget          = "budget_notice"
)

// toolMarkers are the texts components add to tool results, checked in
// order; the first match names the trigger.
var toolMarkers = []struct{ name, text string }{
	{TriggerProgressRefusal, "[progress] Not run:"},
	{TriggerProgressNote, "[progress]"},
	{TriggerDestructive, "auto-approved, so nobody confirmed it"},
	{TriggerVersionGuard, "Read it with read_file before changing it."},
	{TriggerVersionGuard, "Read it again before changing it."},
	{TriggerGrepRegex, "matched nothing as literal text"},
	{TriggerServiceHint, "[hint] "},
	{TriggerShellTruncated, "... Output truncated at "},
}

// TriggersFromTrace counts component triggers in an attempt's model calls.
// Tool results and gate messages are resent with every later request, so
// each is counted once; budget notices, which are request-only, are
// counted per request.
func TriggersFromTrace(calls []trace.LLMCallDetail) Triggers {
	out := Triggers{}
	seen := map[string]bool{}
	for _, c := range calls {
		ns := strings.Join(c.Namespace, "/")
		budget := false
		for i, m := range c.Messages {
			source, _ := m.Metadata[rt.SyntheticSourceKey].(string)
			switch {
			case source == codeagent.BudgetSource:
				budget = true
			case source == codeagent.VerifyGateName:
				key := fmt.Sprintf("gate|%s|%d|%s", ns, i, m.Text())
				if !seen[key] {
					seen[key] = true
					kind, _ := m.Metadata[codeagent.GateKindKey].(string)
					out["gate_"+kind]++
				}
			case m.Role == llm.RoleTool:
				text := m.Text()
				key := "tool|" + ns + "|" + m.ToolCallID
				if seen[key] {
					continue
				}
				seen[key] = true
				for _, mk := range toolMarkers {
					if strings.Contains(text, mk.text) {
						out[mk.name]++
						break
					}
				}
			}
		}
		if budget {
			out[TriggerBudget]++
		}
	}
	return out
}
