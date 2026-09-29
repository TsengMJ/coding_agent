// Package todolist provides the write_todos planning tool
// (langchain `TodoListMiddleware`).
package todolist

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/tsengmingchun/deepagents_golang/agent"
	"github.com/tsengmingchun/deepagents_golang/llm"
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

const parallelCallsError = "Error: The `write_todos` tool should never be called multiple times in parallel. Please call it only once per model invocation to update the todo list."

// Middleware exposes write_todos and its usage prompt.
type Middleware struct {
	systemPrompt    string
	toolDescription string
}

// New returns the middleware; empty strings use the default prompts.
func New(systemPrompt, toolDescription string) *Middleware {
	if systemPrompt == "" {
		systemPrompt = SystemPrompt
	}
	if toolDescription == "" {
		toolDescription = ToolDescription
	}
	return &Middleware{systemPrompt: systemPrompt, toolDescription: toolDescription}
}

func (m *Middleware) Name() string { return "TodoListMiddleware" }

func (m *Middleware) Tools() []tool.Tool {
	return []tool.Tool{&tool.Func{
		ToolName:        ToolName,
		ToolDescription: m.toolDescription,
		ToolSchema: tool.Schema{
			"type": "object",
			"properties": map[string]any{
				"todos": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"content": map[string]any{"type": "string", "description": "The content/description of the todo item."},
							"status": map[string]any{
								"type":        "string",
								"enum":        []string{string(StatusPending), string(StatusInProgress), string(StatusCompleted)},
								"description": "The current status of the todo item.",
							},
						},
						"required":             []string{"content", "status"},
						"additionalProperties": false,
					},
				},
			},
			"required":             []string{"todos"},
			"additionalProperties": false,
		},
		Fn: writeTodos,
	}}
}

func (m *Middleware) StateSchema() runtime.Schema {
	return runtime.Schema{TodosStateKey: {Name: TodosStateKey, OmitFromInput: true}}
}

// WrapModelCall appends the write_todos usage prompt.
func (m *Middleware) WrapModelCall(ctx context.Context, req *agent.ModelRequest, next agent.ModelHandler) (*agent.ModelResponse, error) {
	updated := *req
	updated.SystemMessage = agent.AppendToSystemMessage(req.SystemMessage, m.systemPrompt)
	return next(ctx, &updated)
}

// AfterModel answers every write_todos call with an error when the model
// issued more than one in the same turn: each call replaces the whole list,
// so parallel calls have no well-defined winner.
func (m *Middleware) AfterModel(_ context.Context, state *runtime.State, _ *agent.Runtime) (*runtime.Update, error) {
	var last *llm.Message
	for i := len(state.Messages) - 1; i >= 0; i-- {
		if state.Messages[i].Role == llm.RoleAssistant {
			last = &state.Messages[i]
			break
		}
	}
	if last == nil {
		return nil, nil
	}
	var errs []llm.Message
	for _, call := range last.ToolCalls {
		if call.Name == ToolName {
			msg := llm.NewToolMessage(call.ID, ToolName, parallelCallsError)
			msg.Status = llm.ToolStatusError
			errs = append(errs, msg)
		}
	}
	if len(errs) < 2 {
		return nil, nil
	}
	return &runtime.Update{Messages: errs}, nil
}

func writeTodos(_ context.Context, rt *tool.Runtime, args map[string]any) (*tool.Result, error) {
	todos, err := ParseTodos(args["todos"])
	if err != nil {
		return nil, err
	}
	text, err := json.Marshal(todos)
	if err != nil {
		return nil, err
	}
	return &tool.Result{Command: &runtime.Command{Update: &runtime.Update{
		Values:   map[string]any{TodosStateKey: todos},
		Messages: []llm.Message{llm.NewToolMessage(rt.ToolCallID, ToolName, "Updated todo list to "+string(text))},
	}}}, nil
}

// ParseTodos decodes the write_todos `todos` argument (or a todos state
// value read back from a JSON checkpoint).
func ParseTodos(v any) ([]Todo, error) {
	if todos, ok := v.([]Todo); ok {
		return todos, nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("todos: %w", err)
	}
	var todos []Todo
	if err := json.Unmarshal(raw, &todos); err != nil || todos == nil {
		return nil, fmt.Errorf("todos must be a list of {content, status} objects")
	}
	for i, t := range todos {
		switch t.Status {
		case StatusPending, StatusInProgress, StatusCompleted:
		default:
			return nil, fmt.Errorf("todos[%d].status must be pending, in_progress or completed, got %q", i, t.Status)
		}
	}
	return todos, nil
}
