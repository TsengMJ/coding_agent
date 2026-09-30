// Package tool defines the tool abstraction exposed to models
// (langchain `BaseTool` / `StructuredTool` / `ToolRuntime`).
package tool

import (
	"context"

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

type callIDKey struct{}

// WithCallID returns ctx annotated with the ID of the tool call being run,
// so code below the tool (backends, wrappers) can tell which call it serves.
func WithCallID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, callIDKey{}, id)
}

// CallIDFrom returns the tool call ID set by WithCallID, or "".
func CallIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(callIDKey{}).(string)
	return id
}

// Result is what a tool returns. A tool either returns content (turned into a
// ToolMessage) or a Command that updates state (and must itself include the
// ToolMessage in its update).
type Result struct {
	Content []llm.ContentBlock
	Command *runtime.Command
	// Update is applied together with the ToolMessage built from Content
	// (e.g. "files" written through StateBackend). Ignored with a Command.
	Update *runtime.Update
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
func ApplyDescriptionOverrides(tools []Tool, overrides map[string]string) []Tool {
	if len(overrides) == 0 {
		return tools
	}
	out := make([]Tool, len(tools))
	for i, t := range tools {
		if desc, ok := overrides[t.Name()]; ok {
			t = describedTool{Tool: t, description: desc}
		}
		out[i] = t
	}
	return out
}

type describedTool struct {
	Tool
	description string
}

func (d describedTool) Description() string { return d.description }
