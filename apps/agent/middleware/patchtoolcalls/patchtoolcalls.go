// Package patchtoolcalls repairs dangling tool calls (assistant tool calls
// with no matching tool message) before each run (Python `PatchToolCallsMiddleware`).
package patchtoolcalls

import (
	"context"
	"fmt"

	"github.com/tsengmingchun/quill/apps/agent/agent"
	"github.com/tsengmingchun/quill/apps/agent/llm"
	"github.com/tsengmingchun/quill/apps/agent/runtime"
)

// Middleware inserts cancellation tool messages for unanswered tool calls.
// Without them, a run cancelled while tools were executing leaves a history
// the model provider rejects on every later turn.
type Middleware struct{}

// New returns the middleware.
func New() *Middleware { return &Middleware{} }

func (m *Middleware) Name() string { return "PatchToolCallsMiddleware" }

func (m *Middleware) BeforeAgent(_ context.Context, state *runtime.State, _ *agent.Runtime) (*runtime.Update, error) {
	answered := map[string]bool{}
	for _, msg := range state.Messages {
		if msg.Role == llm.RoleTool {
			answered[msg.ToolCallID] = true
		}
	}
	dangling := false
	for _, msg := range state.Messages {
		for _, call := range msg.ToolCalls {
			if call.ID != "" && !answered[call.ID] {
				dangling = true
			}
		}
	}
	if !dangling {
		return nil, nil
	}
	patched := []llm.Message{{ID: runtime.RemoveAllMessages}}
	for _, msg := range state.Messages {
		patched = append(patched, msg)
		if msg.Role != llm.RoleAssistant {
			continue
		}
		for _, call := range msg.ToolCalls {
			if call.ID == "" || answered[call.ID] {
				continue
			}
			name := call.Name
			if name == "" {
				name = "unknown"
			}
			text := fmt.Sprintf("Tool call %s with id %s did not complete - no result was recorded. "+
				"It was cancelled or interrupted, possibly part-way through or after it already ran, "+
				"so files or processes it touches may already have changed. "+
				"Check the current state (for example with git_diff or read_file) before running it again.", name, call.ID)
			tm := llm.NewToolMessage(call.ID, name, text)
			tm.Status = llm.ToolStatusError
			patched = append(patched, tm)
		}
	}
	return &runtime.Update{Messages: patched}, nil
}
