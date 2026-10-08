package summarization

import (
	"github.com/tsengmingchun/quill/apps/agent/llm"
)

// defaultMessagesToKeep is the keep fallback when a fraction policy has no
// model profile to resolve against.
const defaultMessagesToKeep = 20

// threshold resolves size to a token count; ok is false for fractions
// without a known context window.
func threshold(size ContextSize, model llm.ChatModel) (int, bool) {
	switch size.Kind {
	case SizeTokens:
		return max(int(size.Value), 1), true
	case SizeFraction:
		if model == nil || model.Profile().MaxInputTokens <= 0 {
			return 0, false
		}
		return max(int(float64(model.Profile().MaxInputTokens)*size.Value), 1), true
	}
	return 0, false
}

// sizeReached reports whether msgs / total meet size.
func sizeReached(size ContextSize, msgs []llm.Message, total int, model llm.ChatModel) bool {
	if size.Kind == SizeMessages {
		return len(msgs) >= int(size.Value)
	}
	limit, ok := threshold(size, model)
	if !ok {
		return false
	}
	return total >= limit || reportedTokens(msgs) >= limit
}

// reportedTokens is the provider-reported total of the latest assistant
// message, which is more accurate than the character estimate.
func reportedTokens(msgs []llm.Message) int {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == llm.RoleAssistant {
			if u := msgs[i].Usage; u != nil {
				return u.TotalTokens
			}
			return 0
		}
	}
	return 0
}

func (m *Middleware) shouldSummarize(msgs []llm.Message, total int, model llm.ChatModel) bool {
	for _, clause := range m.opts.Trigger {
		if len(clause) == 0 {
			continue
		}
		met := true
		for _, size := range clause {
			if !sizeReached(size, msgs, total, model) {
				met = false
				break
			}
		}
		if met {
			return true
		}
	}
	return false
}

// cutoffIndex is the first message to keep under the keep policy, moved so a
// tool result is never separated from the assistant message that called it.
func (m *Middleware) cutoffIndex(msgs []llm.Message, model llm.ChatModel) int {
	keep := m.opts.Keep
	if keep.Kind == SizeMessages {
		return safeCutoff(msgs, int(keep.Value))
	}
	target, ok := threshold(keep, model)
	if !ok {
		return safeCutoff(msgs, defaultMessagesToKeep)
	}
	if len(msgs) == 0 || m.opts.TokenCounter(msgs, nil) <= target {
		return 0
	}
	// Earliest index whose suffix fits the budget.
	left, right := 0, len(msgs)
	for left < right {
		mid := (left + right) / 2
		if m.opts.TokenCounter(msgs[mid:], nil) <= target {
			right = mid
		} else {
			left = mid + 1
		}
	}
	cutoff := left
	if cutoff >= len(msgs) {
		if len(msgs) == 1 {
			return 0
		}
		cutoff = len(msgs) - 1
	}
	return safeCutoffPoint(msgs, cutoff)
}

func safeCutoff(msgs []llm.Message, keep int) int {
	if len(msgs) <= keep {
		return 0
	}
	return safeCutoffPoint(msgs, len(msgs)-keep)
}

// safeCutoffPoint moves a cutoff that lands on tool results back to the
// assistant message that issued the calls, or past the results when that
// message is missing.
func safeCutoffPoint(msgs []llm.Message, cutoff int) int {
	if cutoff >= len(msgs) || msgs[cutoff].Role != llm.RoleTool {
		return cutoff
	}
	ids := map[string]bool{}
	end := cutoff
	for ; end < len(msgs) && msgs[end].Role == llm.RoleTool; end++ {
		if id := msgs[end].ToolCallID; id != "" {
			ids[id] = true
		}
	}
	for i := cutoff - 1; i >= 0; i-- {
		if msgs[i].Role != llm.RoleAssistant {
			continue
		}
		for _, call := range msgs[i].ToolCalls {
			if ids[call.ID] {
				return i
			}
		}
	}
	return end
}

// truncateArgs clips long string arguments of write_file / edit_file calls
// in messages older than the truncation keep window.
func (m *Middleware) truncateArgs(msgs []llm.Message, total int, model llm.ChatModel) ([]llm.Message, bool) {
	t := m.opts.TruncateArgs
	if t == nil || t.Trigger == nil || !sizeReached(*t.Trigger, msgs, total, model) {
		return msgs, false
	}
	cutoff := m.truncateCutoff(msgs, model)
	out := msgs
	changed := false
	for i := range min(cutoff, len(msgs)) {
		msg := msgs[i]
		if msg.Role != llm.RoleAssistant || len(msg.ToolCalls) == 0 {
			continue
		}
		calls, ok := clipCalls(msg.ToolCalls, t)
		if !ok {
			continue
		}
		if !changed {
			out = append([]llm.Message(nil), msgs...)
			changed = true
		}
		out[i].ToolCalls = calls
	}
	return out, changed
}

func (m *Middleware) truncateCutoff(msgs []llm.Message, model llm.ChatModel) int {
	keep := m.opts.TruncateArgs.Keep
	if keep.Kind == SizeMessages {
		return max(len(msgs)-int(keep.Value), 0)
	}
	target, ok := threshold(keep, model)
	if !ok {
		return max(len(msgs)-defaultMessagesToKeep, 0)
	}
	kept := 0
	for i := len(msgs) - 1; i >= 0; i-- {
		kept += m.opts.TokenCounter(msgs[i:i+1], nil)
		if kept > target {
			return i + 1
		}
	}
	return 0
}

func clipCalls(calls []llm.ToolCall, t *TruncateArgsSettings) ([]llm.ToolCall, bool) {
	var out []llm.ToolCall
	for i, call := range calls {
		if call.Name != "write_file" && call.Name != "edit_file" {
			continue
		}
		var args map[string]any
		for k, v := range call.Args {
			s, ok := v.(string)
			if !ok || len([]rune(s)) <= t.MaxLength {
				continue
			}
			if args == nil {
				args = make(map[string]any, len(call.Args))
				for k2, v2 := range call.Args {
					args[k2] = v2
				}
			}
			args[k] = string([]rune(s)[:20]) + t.TruncationText
		}
		if args == nil {
			continue
		}
		if out == nil {
			out = append([]llm.ToolCall(nil), calls...)
		}
		out[i].Args = args
	}
	return out, out != nil
}
