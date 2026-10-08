package bench

import (
	"encoding/json"
	"fmt"
	"strings"

	codeagent "github.com/tsengmingchun/quill/apps/agent/code/agent"
	"github.com/tsengmingchun/quill/apps/agent/code/trace"
	"github.com/tsengmingchun/quill/apps/agent/llm"
	rt "github.com/tsengmingchun/quill/apps/agent/runtime"
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

// Repair loop trigger names. A [repair] note also counts as
// "class_<category>" and, after an edit, "delta_<kind>"; a [micro] note
// as "micro_<status>"; a recorded review as "review_<verdict>".
const (
	TriggerRepairTool   = "repair_tool"
	TriggerRollback     = "rollback"
	TriggerCheckpoint   = "checkpoint"
	TriggerStuckReset   = "stuck_reset"
	TriggerStuckRefusal = "stuck_refusal"
	TriggerRepro        = "repro_reminder"
	TriggerReviewError  = "review_error"
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
				countRepairNotes(out, text)
			}
		}
		if budget {
			out[TriggerBudget]++
		}
		if c.Response != nil && len(c.Namespace) == 0 {
			for _, call := range c.Response.ToolCalls {
				switch call.Name {
				case codeagent.RepairToolName:
					out[TriggerRepairTool]++
				case codeagent.RollbackToolName:
					out[TriggerRollback]++
				}
			}
		}
	}
	return out
}

// countRepairNotes counts the repair loop notes in one tool result; unlike
// toolMarkers, one result can carry several.
func countRepairNotes(out Triggers, text string) {
	var repair struct {
		Category string `json:"category"`
		Delta    *struct {
			Kind string `json:"kind"`
		} `json:"delta"`
	}
	if noteJSON(text, "[repair]", &repair) {
		out["class_"+repair.Category]++
		if repair.Delta != nil {
			out["delta_"+repair.Delta.Kind]++
		}
	}
	var micro struct {
		Status string `json:"status"`
	}
	if noteJSON(text, "[micro]", &micro) {
		out["micro_"+micro.Status]++
	}
	var obj struct{}
	if noteJSON(text, "[checkpoint]", &obj) {
		out[TriggerCheckpoint]++
	}
	switch {
	case strings.Contains(text, "[stuck] Edit not applied"):
		out[TriggerStuckRefusal]++
	case noteJSON(text, "[stuck]", &obj):
		out[TriggerStuckReset]++
	}
	if strings.Contains(text, "[repro] ") {
		out[TriggerRepro]++
	}
}

// noteJSON decodes the JSON object that follows marker on its line.
func noteJSON(text, marker string, v any) bool {
	i := strings.Index(text, marker+" {")
	if i < 0 {
		return false
	}
	line := text[i+len(marker)+1:]
	if j := strings.IndexByte(line, '\n'); j >= 0 {
		line = line[:j]
	}
	return json.Unmarshal([]byte(line), v) == nil
}
