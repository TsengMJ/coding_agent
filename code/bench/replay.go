package bench

import (
	codeagent "github.com/tsengmingchun/deepagents_golang/code/agent"
	"github.com/tsengmingchun/deepagents_golang/code/trace"
	"github.com/tsengmingchun/deepagents_golang/llm"
)

// ProgressReplay counts what codeagent.ProgressMonitor would have done on
// a recorded main-agent conversation. Counts after the first refusal are
// estimates: the real run would have diverged there.
type ProgressReplay struct {
	Refusals, RepeatNotes int
}

// ReplayProgress runs the progress monitor over the longest main-agent
// conversation in calls.
func ReplayProgress(calls []trace.LLMCallDetail) ProgressReplay {
	var msgs []llm.Message
	for _, c := range calls {
		if len(c.Namespace) == 0 && len(c.Messages) >= len(msgs) {
			msgs = c.Messages
			if c.Response != nil {
				msgs = append(msgs[:len(msgs):len(msgs)], *c.Response)
			}
		}
	}
	var out ProgressReplay
	calledBy := map[string]llm.ToolCall{}
	for i, m := range msgs {
		for _, c := range m.ToolCalls {
			calledBy[c.ID] = c
		}
		call, ok := calledBy[m.ToolCallID]
		if m.Role != llm.RoleTool || !ok {
			continue
		}
		history := msgs[:i]
		if codeagent.RepeatRefusal(history, call) != "" {
			out.Refusals++
			continue
		}
		if codeagent.ProgressNote(history, call, m.Text(), m.Status) != "" {
			out.RepeatNotes++
		}
	}
	return out
}
