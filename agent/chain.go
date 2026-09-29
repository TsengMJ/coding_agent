package agent

import (
	"context"

	"github.com/tsengmingchun/deepagents_golang/tool"
)

// chainModel wraps inner with every WrapModelCallHook so that the first
// middleware in mws is the outermost layer.
func chainModel(mws []Middleware, inner ModelHandler) ModelHandler {
	h := inner
	for i := len(mws) - 1; i >= 0; i-- {
		hook, ok := mws[i].(WrapModelCallHook)
		if !ok {
			continue
		}
		next := h
		h = func(ctx context.Context, req *ModelRequest) (*ModelResponse, error) {
			return hook.WrapModelCall(ctx, req, next)
		}
	}
	return h
}

// chainTool wraps inner with every WrapToolCallHook so that the first
// middleware in mws is the outermost layer.
func chainTool(mws []Middleware, inner ToolHandler) ToolHandler {
	h := inner
	for i := len(mws) - 1; i >= 0; i-- {
		hook, ok := mws[i].(WrapToolCallHook)
		if !ok {
			continue
		}
		next := h
		h = func(ctx context.Context, req *ToolCallRequest) (*tool.Result, error) {
			return hook.WrapToolCall(ctx, req, next)
		}
	}
	return h
}
