package bench

import (
	"encoding/json"

	codeagent "github.com/tsengmingchun/deepagents_golang/code/agent"
	"github.com/tsengmingchun/deepagents_golang/code/cost"
	"github.com/tsengmingchun/deepagents_golang/code/trace"
	"github.com/tsengmingchun/deepagents_golang/llm"
)

// InputCost is the estimated input of a sequence of model calls. Cached is
// the part each request shares as a prefix with the previous request of the
// same agent, which is what the provider can serve from its cache.
type InputCost struct {
	Tokens int
	Cached int
	USD    float64
}

// MaskingReplay compares the recorded requests with the same requests after
// codeagent.MaskObservations. It is an estimate: a masked run would have
// taken different steps, such as reading a masked file again.
type MaskingReplay struct {
	Recorded InputCost
	Masked   InputCost
}

// ReplayMasking estimates what observation masking would have saved on a
// recorded trace. Tokens are counted with llm.ApproximateTokenCounter for
// both sides, so the comparison does not depend on provider usage reports.
func ReplayMasking(calls []trace.LLMCallDetail, prices cost.Table) MaskingReplay {
	var out MaskingReplay
	prevRecorded := map[string][]string{}
	prevMasked := map[string][]string{}
	for _, c := range calls {
		ns := namespaceKey(c.Namespace)
		masked, _ := codeagent.MaskObservations(c.Messages)
		out.Recorded.add(c, c.Messages, prevRecorded, ns, prices)
		out.Masked.add(c, masked, prevMasked, ns, prices)
	}
	return out
}

func (in *InputCost) add(c trace.LLMCallDetail, msgs []llm.Message, prev map[string][]string, ns string, prices cost.Table) {
	keys := make([]string, len(msgs))
	for i, m := range msgs {
		keys[i] = messageKey(m)
	}
	shared := 0
	for shared < len(keys) && shared < len(prev[ns]) && keys[shared] == prev[ns][shared] {
		shared++
	}
	prev[ns] = keys
	tokens := llm.ApproximateTokenCounter(msgs, c.Tools)
	cached := 0
	if shared > 0 {
		cached = llm.ApproximateTokenCounter(msgs[:shared], c.Tools)
	}
	in.Tokens += tokens
	in.Cached += cached
	if usd, err := prices.Estimate(c.Model, llm.Usage{InputTokens: tokens, CacheRead: cached}); err == nil {
		in.USD += usd
	}
}

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

func messageKey(m llm.Message) string {
	raw, err := json.Marshal(m)
	if err != nil {
		return ""
	}
	return string(raw)
}

func namespaceKey(ns []string) string {
	raw, _ := json.Marshal(ns)
	return string(raw)
}
