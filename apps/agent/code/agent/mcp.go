package agent

import (
	"github.com/tsengmingchun/quill/apps/agent/code/mcp"
	"github.com/tsengmingchun/quill/apps/agent/middleware/hitl"
	"github.com/tsengmingchun/quill/apps/agent/tool"
)

// reviewMCPTools requires approval for every MCP tool not matched by allow
// (mcp.allow_tools): an MCP server can do anything its process can.
func reviewMCPTools(cfg map[string]hitl.InterruptOnConfig, tools []tool.Tool, allow []string) {
	for _, t := range tools {
		if !mcp.ToolAllowed(t.Name(), allow) {
			cfg[t.Name()] = hitl.InterruptOnConfig{}
		}
	}
}
