package backend

import (
	"context"

	"github.com/tsengmingchun/quill/apps/agent/runtime"
)

// RunContext exposes the executing agent's state and store to backends that
// need them (StateBackend, StoreBackend). Python obtains the same data
// implicitly through LangGraph `get_config()` / `get_store()`.
type RunContext struct {
	State  *runtime.State
	Store  runtime.Store
	Config runtime.RunConfig
	// Pending collects state updates (e.g. "files") produced by backend writes.
	Pending *runtime.Update
}

type runContextKey struct{}

// WithRunContext returns ctx carrying rc.
func WithRunContext(ctx context.Context, rc *RunContext) context.Context {
	return context.WithValue(ctx, runContextKey{}, rc)
}

// RunContextFrom returns the RunContext stored in ctx, or nil.
func RunContextFrom(ctx context.Context) *RunContext {
	rc, _ := ctx.Value(runContextKey{}).(*RunContext)
	return rc
}
