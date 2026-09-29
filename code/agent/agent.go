// Package agent builds the coding agent used by the CLI: system prompt,
// tools, backend routes, subagents and CLI-specific middleware
// (Python `deepagents_code/agent.py`).
package agent

import (
	"context"

	core "github.com/tsengmingchun/deepagents_golang/agent"
	"github.com/tsengmingchun/deepagents_golang/backend"
	"github.com/tsengmingchun/deepagents_golang/code/config"
	"github.com/tsengmingchun/deepagents_golang/internal/notimpl"
	"github.com/tsengmingchun/deepagents_golang/llm"
	"github.com/tsengmingchun/deepagents_golang/runtime"
	"github.com/tsengmingchun/deepagents_golang/tool"
)

// Options configures CreateCLIAgent.
type Options struct {
	Settings     *config.Settings
	Model        llm.ChatModel
	AgentName    string
	Cwd          string
	Backend      backend.Backend
	ExtraTools   []tool.Tool
	Checkpointer runtime.Checkpointer
	Store        runtime.Store
	// AutoApprove disables human review of tool calls (YOLO mode).
	AutoApprove bool
}

// CreateCLIAgent assembles the coding agent on top of deepagents.CreateDeepAgent.
func CreateCLIAgent(ctx context.Context, opts Options) (core.Agent, error) {
	return nil, notimpl.Err("code/agent.CreateCLIAgent")
}

// SystemPrompt renders the CLI system prompt (model identity, cwd, git, tools).
func SystemPrompt(opts Options) (string, error) {
	return "", notimpl.Err("code/agent.SystemPrompt")
}

// ListAgents returns agent names found under the user agents directory.
func ListAgents(p config.Paths) ([]string, error) {
	return nil, notimpl.Err("code/agent.ListAgents")
}

// ResetAgent restores an agent's AGENTS.md to defaults or copies another agent.
func ResetAgent(p config.Paths, name, copyFrom string) error {
	return notimpl.Err("code/agent.ResetAgent")
}

// ShellAllowListMiddleware auto-approves execute calls whose command matches
// the allow list and routes the rest to review.
type ShellAllowListMiddleware struct {
	allow []string
}

// NewShellAllowListMiddleware returns the middleware.
func NewShellAllowListMiddleware(allow []string) *ShellAllowListMiddleware {
	return &ShellAllowListMiddleware{allow: allow}
}

func (m *ShellAllowListMiddleware) Name() string { return "ShellAllowListMiddleware" }

func (m *ShellAllowListMiddleware) WrapToolCall(ctx context.Context, req *core.ToolCallRequest, next core.ToolHandler) (*tool.Result, error) {
	return nil, notimpl.Err("code/agent.ShellAllowListMiddleware.WrapToolCall")
}
