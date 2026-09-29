// Package patchtoolcalls repairs dangling tool calls (assistant tool calls
// with no matching tool message) before each run (Python `PatchToolCallsMiddleware`).
package patchtoolcalls

import (
	"context"

	"github.com/tsengmingchun/deepagents_golang/agent"
	"github.com/tsengmingchun/deepagents_golang/internal/notimpl"
	"github.com/tsengmingchun/deepagents_golang/runtime"
)

// Middleware inserts cancellation tool messages for unanswered tool calls.
type Middleware struct{}

// New returns the middleware.
func New() *Middleware { return &Middleware{} }

func (m *Middleware) Name() string { return "PatchToolCallsMiddleware" }

func (m *Middleware) BeforeAgent(ctx context.Context, state *runtime.State, rt *agent.Runtime) (*runtime.Update, error) {
	return nil, notimpl.Err("patchtoolcalls.Middleware.BeforeAgent")
}
