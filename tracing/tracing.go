// Package tracing records agent runs as a tree of runs (LangSmith run trees):
// one chain run per agent invocation, with llm and tool runs below it, and a
// subagent's chain run below the tool run that started it.
//
// The agent loop starts runs through the Tracer in its config or, when that is
// unset, the one carried by ctx, so nested agents join their parent's trace.
package tracing

import (
	"context"
	"time"
)

// RunType is the LangSmith run type.
type RunType string

const (
	RunChain RunType = "chain"
	RunLLM   RunType = "llm"
	RunTool  RunType = "tool"
)

// Metadata keys LangSmith uses to tell agents apart.
const (
	// AgentTypeKey is "root" for the top-level agent and "subagent" for
	// task subagents (Python `ls_agent_type`).
	AgentTypeKey = "ls_agent_type"
	// AgentNameKey names the agent that made the run (Python `lc_agent_name`).
	AgentNameKey = "lc_agent_name"
	ThreadIDKey  = "thread_id"
)

// RunInfo describes a run as it starts.
type RunInfo struct {
	Name     string
	Type     RunType
	Inputs   map[string]any
	Metadata map[string]any
	Tags     []string
	// Start defaults to now.
	Start time.Time
}

// Run is a started run. End must be called exactly once.
type Run interface {
	End(outputs map[string]any, err error)
}

// Tracer starts runs. The returned ctx makes the new run the parent of runs
// started below it. Implementations must not block on network I/O and must
// never fail the agent: errors are theirs to report.
type Tracer interface {
	Start(ctx context.Context, info RunInfo) (context.Context, Run)
}

type tracerKey struct{}

// WithTracer makes t the tracer for runs started below ctx.
func WithTracer(ctx context.Context, t Tracer) context.Context {
	return context.WithValue(ctx, tracerKey{}, t)
}

// FromContext returns the tracer set by WithTracer, or nil.
func FromContext(ctx context.Context) Tracer {
	t, _ := ctx.Value(tracerKey{}).(Tracer)
	return t
}

// Start starts a run with the tracer in ctx; without one it returns ctx and
// a Run whose End does nothing.
func Start(ctx context.Context, info RunInfo) (context.Context, Run) {
	t := FromContext(ctx)
	if t == nil {
		return ctx, noop{}
	}
	return t.Start(ctx, info)
}

type noop struct{}

func (noop) End(map[string]any, error) {}
