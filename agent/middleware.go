// Package agent provides the model/tool loop and its middleware contract,
// mirroring langchain `create_agent` and `AgentMiddleware`.
//
// A middleware implements Name and any subset of the hook interfaces below;
// the loop discovers hooks with type assertions.
package agent

import (
	"context"
	"slices"

	"github.com/tsengmingchun/deepagents_golang/llm"
	"github.com/tsengmingchun/deepagents_golang/runtime"
	"github.com/tsengmingchun/deepagents_golang/tool"
)

// Runtime is the per-run context handed to hooks (langchain `Runtime`).
type Runtime struct {
	Config  runtime.RunConfig
	Store   runtime.Store
	Context any
	Emit    runtime.Emitter
}

// ModelRequest is the request a middleware may rewrite before the model call.
type ModelRequest struct {
	Model llm.ChatModel
	// SystemMessage is kept as a message so content blocks (cache markers) survive.
	SystemMessage  *llm.Message
	Messages       []llm.Message
	Tools          []tool.Tool
	ToolChoice     string
	ResponseFormat ResponseFormat
	ModelSettings  map[string]any
	State          *runtime.State
	Runtime        *Runtime
}

// ModelResponse is the model result, optionally with extra state updates
// (langchain `ModelResponse` / `ExtendedModelResponse`).
type ModelResponse struct {
	Messages           []llm.Message
	StructuredResponse any
	Update             *runtime.Update
}

// ToolCallRequest is one tool execution a middleware may intercept.
type ToolCallRequest struct {
	ToolCall llm.ToolCall
	// Tool is nil when the model called an unknown tool.
	Tool    tool.Tool
	State   *runtime.State
	Runtime *Runtime
}

// ModelHandler invokes the next layer of model-call handling.
type ModelHandler func(ctx context.Context, req *ModelRequest) (*ModelResponse, error)

// ToolHandler invokes the next layer of tool-call handling.
type ToolHandler func(ctx context.Context, req *ToolCallRequest) (*tool.Result, error)

// Middleware is the only required method; everything else is an optional hook.
type Middleware interface {
	Name() string
}

// BeforeAgentHook runs once at the start of each invocation.
type BeforeAgentHook interface {
	BeforeAgent(ctx context.Context, state *runtime.State, rt *Runtime) (*runtime.Update, error)
}

// BeforeModelHook runs before every model call.
type BeforeModelHook interface {
	BeforeModel(ctx context.Context, state *runtime.State, rt *Runtime) (*runtime.Update, error)
}

// AfterModelHook runs after every model call.
type AfterModelHook interface {
	AfterModel(ctx context.Context, state *runtime.State, rt *Runtime) (*runtime.Update, error)
}

// AfterAgentHook runs once when the loop is about to finish. Returning a
// Command with Goto "model" re-enters the loop (used by RubricMiddleware).
type AfterAgentHook interface {
	AfterAgent(ctx context.Context, state *runtime.State, rt *Runtime) (*runtime.Command, error)
}

// WrapModelCallHook wraps the model call; middleware earlier in the stack is outermost.
type WrapModelCallHook interface {
	WrapModelCall(ctx context.Context, req *ModelRequest, next ModelHandler) (*ModelResponse, error)
}

// WrapToolCallHook wraps each tool execution.
type WrapToolCallHook interface {
	WrapToolCall(ctx context.Context, req *ToolCallRequest, next ToolHandler) (*tool.Result, error)
}

// ToolProvider contributes tools to the agent (`AgentMiddleware.tools`).
type ToolProvider interface {
	Tools() []tool.Tool
}

// StateSchemaProvider contributes state keys (`AgentMiddleware.state_schema`).
type StateSchemaProvider interface {
	StateSchema() runtime.Schema
}

// AppendToSystemMessage returns a copy of msg with text appended as a new
// block, keeping existing blocks (and their cache markers) intact
// (`append_to_system_message`).
func AppendToSystemMessage(msg *llm.Message, text string) *llm.Message {
	if msg == nil {
		out := llm.NewSystemMessage(text)
		return &out
	}
	out := *msg
	if len(out.Content) > 0 {
		text = "\n\n" + text
	}
	out.Content = append(slices.Clone(msg.Content), llm.TextBlock(text))
	return &out
}
