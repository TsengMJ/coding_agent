// Package memory loads AGENTS.md-style memory files and injects them into
// the system prompt (Python `MemoryMiddleware`).
package memory

import (
	"context"

	"github.com/tsengmingchun/deepagents_golang/agent"
	"github.com/tsengmingchun/deepagents_golang/backend"
	"github.com/tsengmingchun/deepagents_golang/internal/notimpl"
	"github.com/tsengmingchun/deepagents_golang/runtime"
)

// ContentsStateKey holds loaded memory keyed by source path (private).
const ContentsStateKey = "memory_contents"

// Options configures the middleware.
type Options struct {
	Backend backend.Backend
	// Sources are memory file paths loaded in order.
	Sources []string
	// AddCacheControl marks the memory block as an Anthropic cache breakpoint.
	AddCacheControl bool
	// SystemPrompt is the template; empty uses the default MEMORY_SYSTEM_PROMPT.
	SystemPrompt string
}

// Middleware loads memory once per thread and injects it on every model call.
type Middleware struct {
	opts Options
}

// New returns a memory middleware.
func New(opts Options) *Middleware { return &Middleware{opts: opts} }

func (m *Middleware) Name() string { return "MemoryMiddleware" }

func (m *Middleware) StateSchema() runtime.Schema {
	return runtime.Schema{ContentsStateKey: {Name: ContentsStateKey, Private: true}}
}

func (m *Middleware) BeforeAgent(ctx context.Context, state *runtime.State, rt *agent.Runtime) (*runtime.Update, error) {
	return nil, notimpl.Err("memory.Middleware.BeforeAgent")
}

func (m *Middleware) WrapModelCall(ctx context.Context, req *agent.ModelRequest, next agent.ModelHandler) (*agent.ModelResponse, error) {
	return nil, notimpl.Err("memory.Middleware.WrapModelCall")
}
