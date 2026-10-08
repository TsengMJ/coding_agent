package agent

import (
	"testing"

	"github.com/tsengmingchun/quill/apps/agent/middleware/hitl"
	"github.com/tsengmingchun/quill/apps/agent/tool"
)

func mcpTools(names ...string) []tool.Tool {
	var out []tool.Tool
	for _, n := range names {
		out = append(out, &tool.Func{ToolName: n})
	}
	return out
}

func TestMCPToolsNeedApprovalByDefault(t *testing.T) {
	cfg := map[string]hitl.InterruptOnConfig{}
	reviewMCPTools(cfg, mcpTools("mcp__pw__browser_click"), nil)
	if _, ok := cfg["mcp__pw__browser_click"]; !ok {
		t.Fatalf("cfg = %v, want mcp__pw__browser_click reviewed", cfg)
	}
}

func TestAllowListedMCPToolsSkipApproval(t *testing.T) {
	cfg := map[string]hitl.InterruptOnConfig{}
	reviewMCPTools(cfg, mcpTools("mcp__pw__browser_snapshot", "mcp__pw__browser_click"), []string{"mcp__pw__browser_snapshot"})
	if _, ok := cfg["mcp__pw__browser_snapshot"]; ok {
		t.Error("allow-listed tool is reviewed")
	}
	if _, ok := cfg["mcp__pw__browser_click"]; !ok {
		t.Error("tool outside the allow list is not reviewed")
	}
}
