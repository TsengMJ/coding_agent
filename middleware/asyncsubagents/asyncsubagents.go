// Package asyncsubagents launches and manages background subagent runs on a
// remote agent server (Python `AsyncSubAgentMiddleware`).
package asyncsubagents

import (
	"context"

	"github.com/tsengmingchun/deepagents_golang/agent"
	"github.com/tsengmingchun/deepagents_golang/internal/notimpl"
	"github.com/tsengmingchun/deepagents_golang/internal/stubtool"
	"github.com/tsengmingchun/deepagents_golang/runtime"
	"github.com/tsengmingchun/deepagents_golang/tool"
)

// Tool names exposed by the middleware.
const (
	ToolStart  = "start_async_task"
	ToolCheck  = "check_async_task"
	ToolUpdate = "update_async_task"
	ToolCancel = "cancel_async_task"
	ToolList   = "list_async_tasks"
)

// TasksStateKey tracks launched tasks in graph state.
const TasksStateKey = "async_tasks"

// AsyncSubAgent is a remote graph run in the background.
type AsyncSubAgent struct {
	Name        string
	Description string
	GraphID     string
	// URL of the agent server; empty means the same deployment (ASGI transport).
	URL     string
	Headers map[string]string
}

func (a AsyncSubAgent) SubAgentName() string { return a.Name }

// Task is a tracked background run.
type Task struct {
	TaskID    string `json:"task_id"`
	AgentName string `json:"agent_name"`
	ThreadID  string `json:"thread_id"`
	RunID     string `json:"run_id"`
	Status    string `json:"status"`
}

// Client is the subset of the agent-server API the tools call
// (LangGraph SDK threads/runs).
type Client interface {
	CreateRun(ctx context.Context, graphID, threadID string, input map[string]any) (runID string, err error)
	GetRun(ctx context.Context, threadID, runID string) (status string, err error)
	CancelRun(ctx context.Context, threadID, runID string) error
	GetThreadState(ctx context.Context, threadID string) (*runtime.State, error)
}

// Middleware provides the five async task tools.
type Middleware struct {
	agents       []AsyncSubAgent
	systemPrompt string
}

// New returns the middleware; at least one agent is required and names must be unique.
func New(agents []AsyncSubAgent, systemPrompt string) (*Middleware, error) {
	return &Middleware{agents: agents, systemPrompt: systemPrompt}, nil
}

func (m *Middleware) Name() string { return "AsyncSubAgentMiddleware" }

func (m *Middleware) Tools() []tool.Tool {
	return []tool.Tool{
		stubtool.New(ToolStart, ""),
		stubtool.New(ToolCheck, ""),
		stubtool.New(ToolUpdate, ""),
		stubtool.New(ToolCancel, ""),
		stubtool.New(ToolList, ""),
	}
}

func (m *Middleware) StateSchema() runtime.Schema {
	return runtime.Schema{TasksStateKey: {Name: TasksStateKey}}
}

func (m *Middleware) WrapModelCall(ctx context.Context, req *agent.ModelRequest, next agent.ModelHandler) (*agent.ModelResponse, error) {
	return nil, notimpl.Err("asyncsubagents.Middleware.WrapModelCall")
}
