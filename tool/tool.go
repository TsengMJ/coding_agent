// Package tool defines the tool abstraction exposed to models
// (langchain `BaseTool` / `StructuredTool` / `ToolRuntime`).
package tool

import (
	"context"

	"github.com/tsengmingchun/deepagents_golang/internal/notimpl"
	"github.com/tsengmingchun/deepagents_golang/llm"
	"github.com/tsengmingchun/deepagents_golang/runtime"
)

// Schema is a JSON Schema object describing tool arguments.
type Schema map[string]any

// Runtime is injected into tool invocations (langchain `ToolRuntime`).
type Runtime struct {
	ToolCallID string
	State      *runtime.State
	Store      runtime.Store
	Config     runtime.RunConfig
	// Context is the immutable run-scoped context (`context_schema`).
	Context any
	Emit    runtime.Emitter
}

// Result is what a tool returns. A tool either returns content (turned into a
// ToolMessage) or a Command that updates state (and must itself include the
// ToolMessage in its update).
type Result struct {
	Content []llm.ContentBlock
	Command *runtime.Command
	// Artifact is attached to the ToolMessage but not sent to the model.
	Artifact any
	IsError  bool
}

// Tool is a callable the model may request.
type Tool interface {
	Name() string
	Description() string
	Schema() Schema
	Invoke(ctx context.Context, rt *Runtime, args map[string]any) (*Result, error)
}

// Func adapts a function to the Tool interface (langchain `StructuredTool.from_function`).
type Func struct {
	ToolName        string
	ToolDescription string
	ToolSchema      Schema
	Fn              func(ctx context.Context, rt *Runtime, args map[string]any) (*Result, error)
}

func (f *Func) Name() string        { return f.ToolName }
func (f *Func) Description() string { return f.ToolDescription }
func (f *Func) Schema() Schema      { return f.ToolSchema }

func (f *Func) Invoke(ctx context.Context, rt *Runtime, args map[string]any) (*Result, error) {
	return f.Fn(ctx, rt, args)
}

// Spec converts a tool into the model-facing spec.
func Spec(t Tool) llm.ToolSpec {
	return llm.ToolSpec{Name: t.Name(), Description: t.Description(), Parameters: t.Schema()}
}

// ApplyDescriptionOverrides returns tools with descriptions replaced per
// overrides, keyed by tool name (`_apply_tool_description_overrides`).
func ApplyDescriptionOverrides(tools []Tool, overrides map[string]string) ([]Tool, error) {
	return nil, notimpl.Err("tool.ApplyDescriptionOverrides")
}
