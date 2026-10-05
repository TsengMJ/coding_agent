package langsmith

import (
	"encoding/json"

	"github.com/tsengmingchun/quill/agent"
	"github.com/tsengmingchun/quill/llm"
	"github.com/tsengmingchun/quill/tracing"
)

// encodeInputs turns llm values into the OpenAI chat shapes LangSmith renders
// as a conversation: messages with roles, and function tools.
func encodeInputs(typ tracing.RunType, in map[string]any) map[string]any {
	out := convertValues(in)
	if typ == tracing.RunLLM {
		if specs, ok := in[agent.TraceToolsKey].([]llm.ToolSpec); ok {
			out[agent.TraceToolsKey] = encodeTools(specs)
		}
	}
	return out
}

// encodeOutputs reports an llm run's reply as an OpenAI choice with
// usage_metadata, which LangSmith reads for token counts and cost.
func encodeOutputs(typ tracing.RunType, in map[string]any) map[string]any {
	if typ == tracing.RunLLM {
		if m, ok := in[agent.TraceMessageKey].(llm.Message); ok {
			out := map[string]any{"choices": []any{map[string]any{"message": encodeMessage(m)}}}
			if u := m.Usage; u != nil {
				out["usage_metadata"] = encodeUsage(*u)
			}
			return out
		}
	}
	return convertValues(in)
}

func convertValues(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		switch x := v.(type) {
		case []llm.Message:
			out[k] = encodeMessages(x)
		case llm.Message:
			out[k] = encodeMessage(x)
		default:
			out[k] = v
		}
	}
	return out
}

func encodeMessages(msgs []llm.Message) []map[string]any {
	out := make([]map[string]any, len(msgs))
	for i, m := range msgs {
		out[i] = encodeMessage(m)
	}
	return out
}

func encodeMessage(m llm.Message) map[string]any {
	out := map[string]any{"role": string(m.Role), "content": m.Text()}
	if m.Name != "" && m.Role != llm.RoleTool {
		out["name"] = m.Name
	}
	if m.ToolCallID != "" {
		out["tool_call_id"] = m.ToolCallID
	}
	if len(m.ToolCalls) > 0 {
		calls := make([]map[string]any, len(m.ToolCalls))
		for i, c := range m.ToolCalls {
			args, _ := json.Marshal(c.Args)
			calls[i] = map[string]any{"id": c.ID, "type": "function", "function": map[string]any{"name": c.Name, "arguments": string(args)}}
		}
		out["tool_calls"] = calls
	}
	return out
}

func encodeTools(specs []llm.ToolSpec) []map[string]any {
	out := make([]map[string]any, len(specs))
	for i, s := range specs {
		out[i] = map[string]any{"type": "function", "function": map[string]any{
			"name": s.Name, "description": s.Description, "parameters": s.Parameters,
		}}
	}
	return out
}

func encodeUsage(u llm.Usage) map[string]any {
	total := u.TotalTokens
	if total == 0 {
		total = u.InputTokens + u.OutputTokens
	}
	out := map[string]any{"input_tokens": u.InputTokens, "output_tokens": u.OutputTokens, "total_tokens": total}
	if u.CacheRead > 0 || u.CacheWrite > 0 {
		out["input_token_details"] = map[string]any{"cache_read": u.CacheRead, "cache_creation": u.CacheWrite}
	}
	return out
}
