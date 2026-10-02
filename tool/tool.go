// Package tool defines the tool abstraction exposed to models
// (langchain `BaseTool` / `StructuredTool` / `ToolRuntime`).
package tool

import (
	"context"
	"path"
	"strings"

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

// EffectKind is the broadest thing a tool call may do to the workspace.
type EffectKind int

const (
	// EffectExec may do anything (run commands, change any file); the call
	// runs alone. It is the zero value, so undeclared tools are safe.
	EffectExec EffectKind = iota
	// EffectRead only reads.
	EffectRead
	// EffectWrite changes the files in Effect.Paths.
	EffectWrite
)

// Effect is what one call does to the workspace. The agent loop runs the
// calls of a model turn concurrently except where effects conflict; then
// the later call waits for the earlier one.
type Effect struct {
	Kind EffectKind
	// Paths are the files read or written; nil means the whole workspace.
	Paths []string
}

// Effector is implemented by tools that declare their effect. A tool that
// does not is treated as EffectExec.
type Effector interface {
	Effect(args map[string]any) Effect
}

// EffectOf returns t's effect for args.
func EffectOf(t Tool, args map[string]any) Effect {
	if e, ok := t.(Effector); ok {
		return e.Effect(args)
	}
	return Effect{Kind: EffectExec}
}

// Conflicts reports whether e and o must not run at the same time: either
// executes, or one writes what the other reads or writes.
func (e Effect) Conflicts(o Effect) bool {
	switch {
	case e.Kind == EffectExec || o.Kind == EffectExec:
		return true
	case e.Kind == EffectRead && o.Kind == EffectRead:
		return false
	}
	return pathsOverlap(e.Paths, o.Paths)
}

func pathsOverlap(a, b []string) bool {
	if a == nil || b == nil {
		return true
	}
	for _, p := range a {
		for _, q := range b {
			p, q := path.Clean("/"+p), path.Clean("/"+q)
			if p == q || strings.HasPrefix(q, strings.TrimSuffix(p, "/")+"/") || strings.HasPrefix(p, strings.TrimSuffix(q, "/")+"/") {
				return true
			}
		}
	}
	return false
}

// Func adapts a function to the Tool interface (langchain `StructuredTool.from_function`).
type Func struct {
	ToolName        string
	ToolDescription string
	ToolSchema      Schema
	Fn              func(ctx context.Context, rt *Runtime, args map[string]any) (*Result, error)
	// ToolEffect declares the call's effect; nil means EffectExec.
	ToolEffect func(args map[string]any) Effect
}

func (f *Func) Name() string        { return f.ToolName }
func (f *Func) Description() string { return f.ToolDescription }
func (f *Func) Schema() Schema      { return f.ToolSchema }

func (f *Func) Effect(args map[string]any) Effect {
	if f.ToolEffect == nil {
		return Effect{Kind: EffectExec}
	}
	return f.ToolEffect(args)
}

// ReadOnly is a ToolEffect for tools that only read.
func ReadOnly(map[string]any) Effect { return Effect{Kind: EffectRead} }

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

func (d describedTool) Effect(args map[string]any) Effect { return EffectOf(d.Tool, args) }
