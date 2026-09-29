// Package stubtool builds placeholder tools whose invocation reports
// "not implemented", so middleware can expose its final tool surface early.
package stubtool

import (
	"context"

	"github.com/tsengmingchun/deepagents_golang/internal/notimpl"
	"github.com/tsengmingchun/deepagents_golang/tool"
)

// New returns a tool named name that fails with notimpl on every call.
func New(name, description string) tool.Tool {
	return &tool.Func{
		ToolName:        name,
		ToolDescription: description,
		ToolSchema:      tool.Schema{"type": "object"},
		Fn: func(ctx context.Context, rt *tool.Runtime, args map[string]any) (*tool.Result, error) {
			return nil, notimpl.Err("tool." + name)
		},
	}
}
