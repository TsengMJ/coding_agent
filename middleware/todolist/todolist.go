// Package todolist provides the write_todos planning tool
// (langchain `TodoListMiddleware`).
package todolist

import (
	"context"

	"github.com/tsengmingchun/deepagents_golang/agent"
	"github.com/tsengmingchun/deepagents_golang/internal/notimpl"
	"github.com/tsengmingchun/deepagents_golang/internal/stubtool"
	"github.com/tsengmingchun/deepagents_golang/runtime"
	"github.com/tsengmingchun/deepagents_golang/tool"
)

// ToolName is the planning tool name.
const ToolName = "write_todos"

// TodosStateKey stores the current todo list.
const TodosStateKey = "todos"

// Status is a todo item's progress.
type Status string

const (
	StatusPending    Status = "pending"
	StatusInProgress Status = "in_progress"
	StatusCompleted  Status = "completed"
)

// Todo is one plan item.
type Todo struct {
	Content string `json:"content"`
	Status  Status `json:"status"`
}

// Middleware exposes write_todos and its usage prompt.
type Middleware struct {
	systemPrompt    string
	toolDescription string
}

// New returns the middleware; empty strings use the default prompts.
func New(systemPrompt, toolDescription string) *Middleware {
	return &Middleware{systemPrompt: systemPrompt, toolDescription: toolDescription}
}

func (m *Middleware) Name() string { return "TodoListMiddleware" }

func (m *Middleware) Tools() []tool.Tool {
	return []tool.Tool{stubtool.New(ToolName, m.toolDescription)}
}

func (m *Middleware) StateSchema() runtime.Schema {
	return runtime.Schema{TodosStateKey: {Name: TodosStateKey}}
}

func (m *Middleware) WrapModelCall(ctx context.Context, req *agent.ModelRequest, next agent.ModelHandler) (*agent.ModelResponse, error) {
	return nil, notimpl.Err("todolist.Middleware.WrapModelCall")
}
