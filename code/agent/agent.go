// Package agent builds the coding agent used by the CLI: system prompt,
// tools, backend routes, subagents and CLI-specific middleware
// (Python `deepagents_code/agent.py`).
package agent

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"runtime"
	"strings"

	"github.com/tsengmingchun/deepagents_golang"
	core "github.com/tsengmingchun/deepagents_golang/agent"
	"github.com/tsengmingchun/deepagents_golang/backend"
	"github.com/tsengmingchun/deepagents_golang/code/config"
	"github.com/tsengmingchun/deepagents_golang/code/trace"
	"github.com/tsengmingchun/deepagents_golang/internal/notimpl"
	"github.com/tsengmingchun/deepagents_golang/llm"
	"github.com/tsengmingchun/deepagents_golang/middleware/hitl"
	"github.com/tsengmingchun/deepagents_golang/middleware/todolist"
	rt "github.com/tsengmingchun/deepagents_golang/runtime"
	"github.com/tsengmingchun/deepagents_golang/tool"
)

//go:embed system_prompt.md
var systemPromptTemplate string

// Options configures CreateCLIAgent.
type Options struct {
	Settings  *config.Settings
	Model     llm.ChatModel
	AgentName string
	// Cwd is the project root; file tools see it as "/" and commands run in it.
	Cwd string
	// UserDir is ~/.deepagents; its <agent>/AGENTS.md is loaded as memory.
	// Empty skips user-level memory.
	UserDir string
	// Backend defaults to a LocalShellBackend rooted at Cwd in virtual mode.
	Backend backend.Backend
	// ExtraTools are added after WebTools(Settings).
	ExtraTools   []tool.Tool
	Checkpointer rt.Checkpointer
	Store        rt.Store
	// AutoApprove disables human review of tool calls (YOLO mode).
	AutoApprove bool
	// Interactive tailors the prompt for a human in the loop; false means headless.
	Interactive bool
	// ProjectContext is the rendered "Project Context" section;
	// CreateCLIAgent fills it from LocalContext.
	ProjectContext string
}

// CreateCLIAgent assembles the coding agent on top of deepagents.CreateDeepAgent.
func CreateCLIAgent(ctx context.Context, opts Options) (core.Agent, error) {
	if opts.Model == nil {
		return nil, errors.New("code/agent.CreateCLIAgent: Model is required")
	}
	if opts.Cwd == "" {
		return nil, errors.New("code/agent.CreateCLIAgent: Cwd is required")
	}
	b := opts.Backend
	if b == nil {
		b = NewBackend(opts.Cwd)
	}
	b = trace.WrapBackend(b)
	if opts.ProjectContext == "" {
		opts.ProjectContext = LocalContext(ctx, opts.Cwd)
	}
	prompt, err := SystemPrompt(opts)
	if err != nil {
		return nil, err
	}
	mem, err := newMemory(b, opts.UserDir, MemorySources(opts.UserDir, opts.AgentName), opts.Interactive)
	if err != nil {
		return nil, err
	}
	summ, err := NewSummarization(opts.Model, b)
	if err != nil {
		return nil, err
	}
	dopts := []deepagents.Option{
		deepagents.WithModel(opts.Model),
		deepagents.WithBackend(b),
		deepagents.WithSystemPrompt(prompt),
		deepagents.WithTools(append(WebTools(opts.Settings), opts.ExtraTools...)...),
		deepagents.WithMiddleware(summ, todolist.New("", ""), mem, trace.Middleware{}),
		deepagents.WithCheckpointer(opts.Checkpointer),
		deepagents.WithStore(opts.Store),
		deepagents.WithName(opts.AgentName),
	}
	if !opts.AutoApprove {
		dopts = append(dopts, deepagents.WithInterruptOn(InterruptOn(opts.Settings)))
	}
	return deepagents.CreateDeepAgent(dopts...)
}

// DefaultInterruptOn requires approval for tools that change files, run
// commands or reach the network. Per run, hitl.AutoApproveKey in RunConfig.Configurable skips it.
func DefaultInterruptOn() map[string]hitl.InterruptOnConfig {
	return map[string]hitl.InterruptOnConfig{
		"write_file": {},
		"edit_file":  {},
		"delete": {
			AllowedDecisions: []hitl.DecisionType{hitl.DecisionApprove, hitl.DecisionReject},
			Description:      "Action: Delete file or directory",
		},
		"execute":    {},
		"web_search": {Describe: FormatWebSearchDescription},
		"fetch_url":  {Describe: FormatFetchURLDescription},
	}
}

// InterruptOn is DefaultInterruptOn narrowed by the shell allow list:
// allow-listed execute calls run without review, and "all" drops review of
// execute entirely.
func InterruptOn(s *config.Settings) map[string]hitl.InterruptOnConfig {
	cfg := DefaultInterruptOn()
	switch {
	case s == nil:
	case s.ShellAllowAll:
		delete(cfg, "execute")
	case len(s.ShellAllowList) > 0:
		allow := s.ShellAllowList
		cfg["execute"] = hitl.InterruptOnConfig{When: func(call llm.ToolCall) bool {
			cmd, _ := call.Args["command"].(string)
			return !IsShellCommandAllowed(cmd, allow, false)
		}}
	}
	return cfg
}

// SystemPrompt renders the CLI system prompt (mode, model identity, cwd,
// project context).
func SystemPrompt(opts Options) (string, error) {
	if opts.Cwd == "" {
		return "", errors.New("code/agent.SystemPrompt: Cwd is required")
	}
	r := strings.NewReplacer(
		"{mode_description}", pick(opts.Interactive, "an interactive TUI on the user's computer",
			"non-interactive (headless) mode — there is no human operator monitoring your output in real time"),
		"{interactive_preamble}", pick(opts.Interactive,
			"The user sends you messages and you respond with text and tool calls. The user can see your responses and tool outputs in real time, so keep them informed — but don't over-explain.",
			"You received a single task and must complete it fully and autonomously. There is no human available to answer follow-up questions, so do NOT ask for clarification — make reasonable assumptions and proceed."),
		"{ambiguity_guidance}", pick(opts.Interactive,
			"- If the request is ambiguous, ask questions before acting.\n- If asked how to approach something, explain first, then act.",
			"- Do NOT ask clarifying questions — there is no human to answer them. Make reasonable assumptions and proceed.\n"+
				"- If you encounter ambiguity, choose the most reasonable interpretation and note your assumption briefly.\n"+
				"- Always use non-interactive command variants — no human is available to respond to prompts. Examples: `npm init -y` not `npm init`, `--no-input`/`--non-interactive` flags where available. Never run commands that block waiting for stdin."),
		"{blocked_task_guidance}", pick(opts.Interactive, "Only ask when genuinely blocked.",
			"If essential information cannot be obtained from available sources, report the blocker and any completed work. Do not invent required identifiers or permissions."),
		"{substitution_guidance}", pick(opts.Interactive, "Don't substitute without asking.",
			"If a required tool or dependency is unavailable, report the blocker instead of silently substituting another."),
		"{failure_recovery_guidance}", pick(opts.Interactive,
			"- On the third attempt, stop and ask the user what to do\n- If you notice yourself going in circles, stop and ask the user for help",
			"- After repeated failures, use a different permitted approach. If none is available, report the blocker and any completed work."),
		"{model_identity_section}", modelIdentitySection(opts.Model),
		"{working_dir_section}", workingDirSection(opts.Cwd),
		"{web_search_section}", webSearchSection(opts),
		"{project_context_section}", opts.ProjectContext,
	)
	return r.Replace(systemPromptTemplate), nil
}

func pick(interactive bool, yes, no string) string {
	if interactive {
		return yes
	}
	return no
}

func modelIdentitySection(m llm.ChatModel) string {
	if m == nil || m.Info().Model == "" {
		return ""
	}
	info := m.Info()
	s := fmt.Sprintf("### Model Identity\n\nYou are running as model `%s`", info.Model)
	if info.Provider != "" {
		s += fmt.Sprintf(" (provider: %s)", info.Provider)
	}
	s += ".\n"
	if limit := m.Profile().MaxInputTokens; limit > 0 {
		s += fmt.Sprintf("Your context window is %d tokens.\n", limit)
	}
	return s + "\n"
}

func workingDirSection(cwd string) string {
	return fmt.Sprintf("### Current Working Directory\n\n"+
		"The project root is `%s` on %s.\n\n"+
		"### File System and Paths\n\n"+
		"**IMPORTANT - Path Handling:**\n"+
		"- File tools (`ls`, `read_file`, `write_file`, `edit_file`, `delete`, `glob`, `grep`) use virtual absolute paths where `/` is the project root.\n"+
		"- Example: to create `%s/cmd/app/main.go`, call `write_file(file_path=\"/cmd/app/main.go\", ...)`.\n"+
		"- File tools cannot reach paths outside the project root.\n"+
		"- `execute` runs shell commands with the project root as the working directory, so use paths relative to it there (e.g. `go test ./...`).\n\n",
		cwd, runtime.GOOS, cwd)
}

// ListAgents returns agent names found under the user agents directory.
func ListAgents(p config.Paths) ([]string, error) {
	return nil, notimpl.Err("code/agent.ListAgents")
}

// ResetAgent restores an agent's AGENTS.md to defaults or copies another agent.
func ResetAgent(p config.Paths, name, copyFrom string) error {
	return notimpl.Err("code/agent.ResetAgent")
}
