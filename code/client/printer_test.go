package client

import (
	"testing"

	"github.com/tsengmingchun/deepagents_golang/llm"
)

func TestToolCallLineSummarizesWebTools(t *testing.T) {
	cases := map[string]llm.ToolCall{
		"[tool] query_docs /vercel/next.js · middleware": {Name: "query_docs", Args: map[string]any{"library_id": "/vercel/next.js", "query": "middleware"}},
		"[tool] resolve_library_id next.js":              {Name: "resolve_library_id", Args: map[string]any{"library_name": "next.js", "query": "routing"}},
		"[tool] task_output t1":                          {Name: "task_output", Args: map[string]any{"task_id": "t1"}},
		"[tool] web_search go 1.27":                      {Name: "web_search", Args: map[string]any{"query": "go 1.27"}},
	}
	for want, call := range cases {
		if got := ToolCallLine(call); got != want {
			t.Errorf("ToolCallLine(%s) = %q, want %q", call.Name, got, want)
		}
	}
}
