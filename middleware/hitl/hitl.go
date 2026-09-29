// Package hitl pauses tool calls for human approval
// (langchain `HumanInTheLoopMiddleware`).
package hitl

import (
	"context"

	"github.com/tsengmingchun/deepagents_golang/agent"
	"github.com/tsengmingchun/deepagents_golang/internal/notimpl"
	"github.com/tsengmingchun/deepagents_golang/llm"
	"github.com/tsengmingchun/deepagents_golang/runtime"
)

// DecisionType is a reviewer's response to an interrupted tool call.
type DecisionType string

const (
	DecisionApprove DecisionType = "approve"
	DecisionEdit    DecisionType = "edit"
	DecisionReject  DecisionType = "reject"
	DecisionRespond DecisionType = "respond"
)

// InterruptOnConfig configures review for one tool (`InterruptOnConfig`).
type InterruptOnConfig struct {
	AllowedDecisions []DecisionType
	Description      string
	// When restricts the interrupt to matching calls; nil means always.
	When func(call llm.ToolCall) bool
}

// ActionRequest describes one tool call awaiting review (the interrupt value).
type ActionRequest struct {
	Name        string         `json:"name"`
	Args        map[string]any `json:"args"`
	Description string         `json:"description,omitempty"`
}

// Decision is the resume value supplied by a reviewer for one ActionRequest.
type Decision struct {
	Type       DecisionType   `json:"type"`
	EditedArgs map[string]any `json:"edited_args,omitempty"`
	Message    string         `json:"message,omitempty"`
}

// Middleware interrupts after the model proposes calls to configured tools.
type Middleware struct {
	interruptOn map[string]InterruptOnConfig
}

// New returns a HITL middleware for the given per-tool configs.
func New(interruptOn map[string]InterruptOnConfig) *Middleware {
	return &Middleware{interruptOn: interruptOn}
}

func (m *Middleware) Name() string { return "HumanInTheLoopMiddleware" }

func (m *Middleware) AfterModel(ctx context.Context, state *runtime.State, rt *agent.Runtime) (*runtime.Update, error) {
	return nil, notimpl.Err("hitl.Middleware.AfterModel")
}

// Merge overlays user configs on generated ones; user entries win per tool.
// Returns nil when both are empty (`_merge_fs_interrupt_on`).
func Merge(generated, user map[string]InterruptOnConfig) map[string]InterruptOnConfig {
	return nil
}
