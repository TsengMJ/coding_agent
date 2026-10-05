package trace

import (
	"context"

	"github.com/tsengmingchun/quill/agent"
	"github.com/tsengmingchun/quill/middleware/subagents"
	"github.com/tsengmingchun/quill/tool"
)

// Middleware puts each task call's subagent run in its own namespace, the
// same "task:<tool call id>" the event stream uses, so records made by the
// subagent's model calls and file changes can be told apart.
type Middleware struct{}

func (Middleware) Name() string { return "trace" }

func (Middleware) WrapToolCall(ctx context.Context, req *agent.ToolCallRequest, next agent.ToolHandler) (*tool.Result, error) {
	if req.ToolCall.Name == subagents.TaskToolName {
		ctx = WithNamespace(ctx, subagents.NamespacePrefix+req.ToolCall.ID)
	}
	return next(ctx, req)
}
