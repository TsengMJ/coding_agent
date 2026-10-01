package planmode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/tsengmingchun/deepagents_golang/llm"
	"github.com/tsengmingchun/deepagents_golang/middleware/todolist"
	"github.com/tsengmingchun/deepagents_golang/runtime"
	"github.com/tsengmingchun/deepagents_golang/tool"
)

func (m *Middleware) Tools() []tool.Tool {
	return []tool.Tool{&tool.Func{
		ToolName:        ToolName,
		ToolDescription: createPlanDescription,
		ToolSchema: tool.Schema{
			"type": "object",
			"properties": map[string]any{
				"name":     map[string]any{"type": "string", "description": "Short title of the plan."},
				"overview": map[string]any{"type": "string", "description": "One or two sentences: what will be done and why."},
				"plan":     map[string]any{"type": "string", "description": "The plan body in markdown (without the todo checklist, which comes from `todos`)."},
				"todos": map[string]any{
					"type":     "array",
					"minItems": 1,
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"id":      map[string]any{"type": "string", "description": "Short unique id, e.g. \"add-flag\"."},
							"content": map[string]any{"type": "string", "description": "One concrete, verifiable implementation step naming the files involved."},
						},
						"required":             []string{"id", "content"},
						"additionalProperties": false,
					},
				},
			},
			"required":             []string{"name", "overview", "plan", "todos"},
			"additionalProperties": false,
		},
		Fn: m.createPlan,
	}}
}

type planArgs struct {
	Name     string `json:"name"`
	Overview string `json:"overview"`
	Plan     string `json:"plan"`
	Todos    []struct {
		ID      string `json:"id"`
		Content string `json:"content"`
	} `json:"todos"`
}

func (m *Middleware) createPlan(ctx context.Context, rt *tool.Runtime, args map[string]any) (*tool.Result, error) {
	var a planArgs
	raw, err := json.Marshal(args)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, fmt.Errorf("create_plan arguments: %w", err)
	}
	if strings.TrimSpace(a.Name) == "" || strings.TrimSpace(a.Plan) == "" {
		return nil, errors.New("create_plan needs a name and a plan body")
	}
	if len(a.Todos) == 0 {
		return nil, errors.New("create_plan needs at least one todo")
	}
	todos := make([]todolist.Todo, len(a.Todos))
	for i, t := range a.Todos {
		if t.ID == "" || strings.TrimSpace(t.Content) == "" {
			return nil, fmt.Errorf("todos[%d] needs an id and content", i)
		}
		todos[i] = todolist.Todo{ID: t.ID, Content: t.Content, Status: todolist.StatusPending}
	}
	if todos, err = todolist.ParseTodos(todos); err != nil {
		return nil, err
	}
	p := Plan{Name: a.Name, Overview: a.Overview, Body: a.Plan, Todos: todos, Status: StatusDraft}
	var state *runtime.State
	threadID := ""
	if rt != nil {
		state, threadID = rt.State, rt.Config.ThreadID
	}
	if prev := PlanOf(state); prev != nil && prev.ID != "" {
		p.ID = prev.ID
	} else {
		p.ID = planID(threadID, a.Name)
	}
	if err := m.store.Save(ctx, threadID, p); err != nil {
		return nil, fmt.Errorf("save plan: %w", err)
	}
	text := fmt.Sprintf("Plan %s saved with %d todos. End your turn now with a short summary of the plan; the user will review it and either ask for changes or start building.", p.ID, len(todos))
	callID := ""
	if rt != nil {
		callID = rt.ToolCallID
	}
	return &tool.Result{Command: &runtime.Command{Update: &runtime.Update{
		Values:   map[string]any{PlanStateKey: p},
		Messages: []llm.Message{llm.NewToolMessage(callID, ToolName, text)},
	}}}, nil
}

func planID(threadID, name string) string {
	prefix := slug(threadID)
	if threadID == "" {
		prefix = "thread"
	}
	return prefix + "-" + slug(name)
}

// ParsePlan decodes a plan state value (a Plan or its JSON form after a
// checkpoint round trip); nil gives nil.
func ParsePlan(v any) (*Plan, error) {
	switch p := v.(type) {
	case nil:
		return nil, nil
	case Plan:
		return &p, nil
	case *Plan:
		return p, nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var p Plan
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("plan state: %w", err)
	}
	return &p, nil
}
