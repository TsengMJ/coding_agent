// Package todolist provides the write_todos planning tool
// (langchain `TodoListMiddleware`).
package todolist

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/tsengmingchun/deepagents_golang/agent"
	"github.com/tsengmingchun/deepagents_golang/llm"
	"github.com/tsengmingchun/deepagents_golang/runtime"
	"github.com/tsengmingchun/deepagents_golang/tool"
)

// ToolName is the planning tool name.
const ToolName = "write_todos"

// TodosStateKey stores the current todo list.
const TodosStateKey = "todos"

// ResultPrefix starts a successful write_todos result; the full list
// follows as JSON.
const ResultPrefix = "Updated todo list to "

// Status is a todo item's progress.
type Status string

const (
	StatusPending    Status = "pending"
	StatusInProgress Status = "in_progress"
	StatusCompleted  Status = "completed"
	StatusCancelled  Status = "cancelled"
)

// Todo is one plan item.
type Todo struct {
	ID      string `json:"id"`
	Content string `json:"content"`
	Status  Status `json:"status"`
	// Notes holds findings, open questions or how the item will be verified.
	Notes string `json:"notes,omitempty"`
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
					"type":        "array",
					"description": "The todo items. With merge=false this is the whole list; with merge=true only the items to add or update.",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"id":      map[string]any{"type": "string", "description": "Short unique id of the item, kept stable across updates (e.g. \"fix-parser\")."},
							"content": map[string]any{"type": "string", "description": "What to do, specific enough to act on: name the files, functions or commands involved."},
							"status": map[string]any{
								"type":        "string",
								"enum":        []string{string(StatusPending), string(StatusInProgress), string(StatusCompleted), string(StatusCancelled)},
								"description": "The current status of the todo item.",
							},
							"notes": map[string]any{"type": "string", "description": "Optional findings, open questions still to verify, or how the item will be checked."},
						},
						"required":             []string{"id", "content", "status"},
						"additionalProperties": false,
					},
				},
				"merge": map[string]any{
					"type":        "boolean",
					"description": "true: update items by id and append new ones, keeping the rest. false (default): replace the whole list.",
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
	incoming, err := ParseTodos(args["todos"])
	if err != nil {
		return nil, err
	}
	todos := incoming
	if merge, _ := args["merge"].(bool); merge {
		var current []Todo
		if rt != nil && rt.State != nil {
			if v, ok := rt.State.Values[TodosStateKey]; ok && v != nil {
				if current, err = ParseTodos(v); err != nil {
					return nil, fmt.Errorf("current todos: %w", err)
				}
			}
		}
		todos = Merge(current, incoming)
	}
	text, err := json.Marshal(todos)
	if err != nil {
		return nil, err
	}
	return &tool.Result{Command: &runtime.Command{Update: &runtime.Update{
		Values:   map[string]any{TodosStateKey: todos},
		Messages: []llm.Message{llm.NewToolMessage(rt.ToolCallID, ToolName, ResultPrefix+string(text))},
	}}}, nil
}

// Merge updates current items whose id matches an incoming item and appends
// the rest; empty notes keep the current notes.
func Merge(current, incoming []Todo) []Todo {
	out := append([]Todo(nil), current...)
	index := map[string]int{}
	for i, t := range out {
		index[t.ID] = i
	}
	for _, t := range incoming {
		i, ok := index[t.ID]
		if !ok {
			index[t.ID] = len(out)
			out = append(out, t)
			continue
		}
		if t.Notes == "" {
			t.Notes = out[i].Notes
		}
		out[i] = t
	}
	return out
}

// ParseTodos decodes the write_todos `todos` argument (or a todos state
// value read back from a JSON checkpoint). Items without an id (older
// checkpoints) get their 1-based position as id.
func ParseTodos(v any) ([]Todo, error) {
	todos, ok := v.([]Todo)
	if !ok {
		raw, err := json.Marshal(v)
		if err != nil {
			return nil, fmt.Errorf("todos: %w", err)
		}
		if err := json.Unmarshal(raw, &todos); err != nil || todos == nil {
			return nil, fmt.Errorf("todos must be a list of {id, content, status} objects")
		}
	}
	todos = append([]Todo(nil), todos...)
	seen := map[string]bool{}
	for _, t := range todos {
		if t.ID != "" {
			seen[t.ID] = true
		}
	}
	ids := map[string]bool{}
	for i := range todos {
		t := &todos[i]
		switch t.Status {
		case StatusPending, StatusInProgress, StatusCompleted, StatusCancelled:
		default:
			return nil, fmt.Errorf("todos[%d].status must be pending, in_progress, completed or cancelled, got %q", i, t.Status)
		}
		if t.ID == "" {
			t.ID = freeID(i+1, seen)
		}
		if ids[t.ID] {
			return nil, fmt.Errorf("todos[%d].id %q is used by another item; ids must be unique", i, t.ID)
		}
		ids[t.ID] = true
	}
	return todos, nil
}

func freeID(n int, taken map[string]bool) string {
	for {
		id := strconv.Itoa(n)
		if !taken[id] {
			taken[id] = true
			return id
		}
		n++
	}
}

// ParseResult reads the full list back from a successful write_todos tool
// result; ok is false for any other text.
func ParseResult(text string) ([]Todo, bool) {
	rest, ok := strings.CutPrefix(text, ResultPrefix)
	if !ok {
		return nil, false
	}
	var todos []Todo
	if err := json.Unmarshal([]byte(rest), &todos); err != nil {
		return nil, false
	}
	return todos, true
}
