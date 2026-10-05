package subagents

import (
	"encoding/json"
	"slices"
	"strings"

	"github.com/tsengmingchun/quill/llm"
	"github.com/tsengmingchun/quill/runtime"
)

// UsageKey is the Metadata key, on the message carrying a subagent's report
// (the task tool message, a task_output result or a <task-notification>),
// holding the subagent's token usage as []ModelUsage.
const UsageKey = "subagent_usage"

// ModelNameKey is the reply Metadata key naming the model that produced it,
// when the caller's model tags replies.
const ModelNameKey = "model_name"

// ModelUsage totals one model's replies inside a subagent run, including
// the subagent's own subagents.
type ModelUsage struct {
	// Model is empty when neither the reply nor the subagent names it.
	Model        string `json:"model"`
	Calls        int    `json:"calls"`
	InputTokens  int    `json:"input_tokens"`
	OutputTokens int    `json:"output_tokens"`
	CacheRead    int    `json:"cache_read_tokens,omitempty"`
	CacheWrite   int    `json:"cache_write_tokens,omitempty"`
}

// usageOf totals the assistant replies in st by model, plus the usage of
// any subagents they called; untagged replies count toward model.
func usageOf(st *runtime.State, model string) []ModelUsage {
	if st == nil {
		return nil
	}
	by := map[string]*ModelUsage{}
	add := func(u ModelUsage) {
		cur := by[u.Model]
		if cur == nil {
			cur = &ModelUsage{Model: u.Model}
			by[u.Model] = cur
		}
		cur.Calls += u.Calls
		cur.InputTokens += u.InputTokens
		cur.OutputTokens += u.OutputTokens
		cur.CacheRead += u.CacheRead
		cur.CacheWrite += u.CacheWrite
	}
	for _, m := range st.Messages {
		for _, nested := range ParseUsage(m.Metadata[UsageKey]) {
			add(nested)
		}
		if m.Role != llm.RoleAssistant || m.Usage == nil {
			continue
		}
		name, _ := m.Metadata[ModelNameKey].(string)
		if name == "" {
			name = model
		}
		add(ModelUsage{Model: name, Calls: 1, InputTokens: m.Usage.InputTokens, OutputTokens: m.Usage.OutputTokens,
			CacheRead: m.Usage.CacheRead, CacheWrite: m.Usage.CacheWrite})
	}
	out := make([]ModelUsage, 0, len(by))
	for _, u := range by {
		out = append(out, *u)
	}
	slices.SortFunc(out, func(a, b ModelUsage) int { return strings.Compare(a.Model, b.Model) })
	return out
}

// ParseUsage reads a UsageKey value, also after it went through JSON (a
// checkpoint); anything else is nil.
func ParseUsage(v any) []ModelUsage {
	switch u := v.(type) {
	case nil:
		return nil
	case []ModelUsage:
		return u
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var out []ModelUsage
	if json.Unmarshal(raw, &out) != nil {
		return nil
	}
	return out
}

// withUsage returns metadata with usage under UsageKey; empty usage adds
// nothing.
func withUsage(md map[string]any, usage []ModelUsage) map[string]any {
	if len(usage) == 0 {
		return md
	}
	if md == nil {
		md = map[string]any{}
	}
	md[UsageKey] = usage
	return md
}

// specOf names a model as "provider:model".
func specOf(m llm.ChatModel) string {
	info := m.Info()
	if info.Provider == "" {
		return info.Model
	}
	return info.Provider + ":" + info.Model
}
