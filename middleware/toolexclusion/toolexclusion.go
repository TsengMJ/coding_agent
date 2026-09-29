// Package toolexclusion strips harness-profile excluded tools from every
// model request (Python `_ToolExclusionMiddleware`).
package toolexclusion

import (
	"context"

	"github.com/tsengmingchun/deepagents_golang/agent"
	"github.com/tsengmingchun/deepagents_golang/internal/notimpl"
)

// Middleware must run last so it also removes middleware-injected tools.
type Middleware struct {
	excluded map[string]struct{}
}

// New returns the middleware for the given tool names.
func New(excluded []string) *Middleware {
	set := make(map[string]struct{}, len(excluded))
	for _, n := range excluded {
		set[n] = struct{}{}
	}
	return &Middleware{excluded: set}
}

func (m *Middleware) Name() string { return "_ToolExclusionMiddleware" }

func (m *Middleware) WrapModelCall(ctx context.Context, req *agent.ModelRequest, next agent.ModelHandler) (*agent.ModelResponse, error) {
	return nil, notimpl.Err("toolexclusion.Middleware.WrapModelCall")
}
