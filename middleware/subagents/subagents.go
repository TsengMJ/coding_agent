// Package subagents delegates work to ephemeral subagents through the `task`
// tool (Python `SubAgentMiddleware`, `SubAgent`, `CompiledSubAgent`).
package subagents

import (
	"context"

	"github.com/tsengmingchun/deepagents_golang/agent"
	"github.com/tsengmingchun/deepagents_golang/backend"
	"github.com/tsengmingchun/deepagents_golang/internal/notimpl"
	"github.com/tsengmingchun/deepagents_golang/internal/stubtool"
	"github.com/tsengmingchun/deepagents_golang/llm"
	"github.com/tsengmingchun/deepagents_golang/middleware/hitl"
	"github.com/tsengmingchun/deepagents_golang/middleware/permissions"
	"github.com/tsengmingchun/deepagents_golang/runtime"
	"github.com/tsengmingchun/deepagents_golang/tool"
)

// TaskToolName is the delegation tool exposed to the parent agent.
const TaskToolName = "task"

// Defaults for the auto-added general-purpose subagent.
const (
	GeneralPurposeName        = "general-purpose"
	GeneralPurposeDescription = "General-purpose agent for researching complex questions, searching for files and content, and executing multi-step tasks. When you are searching for a keyword or file and are not confident that you will find the right match in the first few tries use this agent to perform the search for you. This agent has access to all tools as the main agent."
	DefaultSubagentPrompt     = "In order to complete the objective that the user asks of you, you have access to a number of standard tools.\n\nThe calling agent only sees your final assistant message, not your intermediate work, tool results, or status tracking. Ensure your final\nresponse contains the complete answer."
)

// Mode controls how much parent context a subagent sees.
type Mode string

const (
	// ModeIsolated passes only the task description (default).
	ModeIsolated Mode = "isolated"
	// ModeFork continues the parent conversation and rebuilds its system prompt (experimental).
	ModeFork Mode = "fork"
)

// Spec is implemented by every subagent kind accepted by CreateDeepAgent
// (SubAgent, CompiledSubAgent and asyncsubagents.AsyncSubAgent).
type Spec interface {
	SubAgentName() string
}

// SubAgent is a declarative synchronous subagent.
//
// For Tools and Permissions, nil means "inherit from the parent" and a
// non-nil empty slice means "none".
type SubAgent struct {
	Name         string
	Description  string
	SystemPrompt string
	Mode         Mode
	Tools        []tool.Tool
	// Model overrides the parent model; ModelSpec ("provider:model") is used when Model is nil.
	Model          llm.ChatModel
	ModelSpec      string
	Middleware     []agent.Middleware
	InterruptOn    map[string]hitl.InterruptOnConfig
	Skills         []string
	Permissions    []permissions.FilesystemPermission
	ResponseFormat agent.ResponseFormat
}

func (s SubAgent) SubAgentName() string { return s.Name }

// CompiledSubAgent wraps a prebuilt agent. Its result is the structured
// response if any, else the last non-empty assistant message.
type CompiledSubAgent struct {
	Name        string
	Description string
	Runnable    agent.Agent
	Mode        Mode
}

func (s CompiledSubAgent) SubAgentName() string { return s.Name }

// Options configures the middleware.
type Options struct {
	Backend   backend.Backend
	SubAgents []Spec
	// TaskDescription overrides the task tool description; must contain {available_agents}.
	TaskDescription string
	SystemPrompt    string
	// PrivateStateKeys are excluded from state passed to isolated subagents.
	PrivateStateKeys []string
	StateSchema      runtime.Schema
}

// Middleware provides the `task` tool.
type Middleware struct {
	opts Options
}

// New returns a subagent middleware. Declarative specs must already be fully
// resolved (model, middleware, prompt) by the caller, as CreateDeepAgent does.
func New(opts Options) (*Middleware, error) {
	return &Middleware{opts: opts}, nil
}

func (m *Middleware) Name() string { return "SubAgentMiddleware" }

// AddPrivateStateKeys extends the keys hidden from subagents after assembly.
func (m *Middleware) AddPrivateStateKeys(keys ...string) {
	m.opts.PrivateStateKeys = append(m.opts.PrivateStateKeys, keys...)
}

func (m *Middleware) Tools() []tool.Tool {
	return []tool.Tool{stubtool.New(TaskToolName, "")}
}

// WrapModelCall appends the subagent system prompt, if configured.
func (m *Middleware) WrapModelCall(ctx context.Context, req *agent.ModelRequest, next agent.ModelHandler) (*agent.ModelResponse, error) {
	return nil, notimpl.Err("subagents.Middleware.WrapModelCall")
}

// ValidateSpecs rejects duplicate names and invalid modes.
func ValidateSpecs(specs []Spec) error {
	return notimpl.Err("subagents.ValidateSpecs")
}
