// Package goal tracks a persistent objective with acceptance criteria and
// grades progress with a rubric (Python `goal_tools.py`, `goal_rubric.py`).
package goal

import (
	"context"

	"github.com/tsengmingchun/quill/agent"
	"github.com/tsengmingchun/quill/internal/notimpl"
	"github.com/tsengmingchun/quill/internal/stubtool"
	"github.com/tsengmingchun/quill/runtime"
	"github.com/tsengmingchun/quill/tool"
)

// Goal is the active objective.
type Goal struct {
	Objective string
	Criteria  []string
	Status    string
}

// StateKey stores the goal in graph state.
const StateKey = "goal"

// ToolsMiddleware exposes update_goal and injects goal-state notices.
type ToolsMiddleware struct{}

// NewToolsMiddleware returns the middleware.
func NewToolsMiddleware() *ToolsMiddleware { return &ToolsMiddleware{} }

func (m *ToolsMiddleware) Name() string { return "GoalToolsMiddleware" }

func (m *ToolsMiddleware) Tools() []tool.Tool {
	return []tool.Tool{stubtool.New("update_goal", "")}
}

func (m *ToolsMiddleware) StateSchema() runtime.Schema {
	return runtime.Schema{StateKey: {Name: StateKey}}
}

func (m *ToolsMiddleware) WrapModelCall(ctx context.Context, req *agent.ModelRequest, next agent.ModelHandler) (*agent.ModelResponse, error) {
	return nil, notimpl.Err("goal.ToolsMiddleware.WrapModelCall")
}
