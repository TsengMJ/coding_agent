// Package commands is the slash-command registry (Python `command_registry.py`).
package commands

import (
	"context"

	"github.com/tsengmingchun/deepagents_golang/internal/notimpl"
)

// Command is one slash command.
type Command struct {
	Name        string
	Aliases     []string
	Description string
	// Hidden commands are omitted from autocomplete and help.
	Hidden bool
}

// Handler executes a command with its raw argument string.
type Handler func(ctx context.Context, args string) error

// Catalog lists every command from COMMANDS.md.
var Catalog = []Command{
	{Name: "/agents", Description: "Browse and switch between available agents"},
	{Name: "/auth", Aliases: []string{"/connect"}, Description: "Connect and manage provider and service credentials"},
	{Name: "/auto", Description: "Switch to Auto approval mode or manage its classifier model"},
	{Name: "/auto-update", Description: "Turn automatic updates on or off"},
	{Name: "/btw", Description: "Ask a side question without changing the conversation"},
	{Name: "/changelog", Description: "Open the changelog in a browser"},
	{Name: "/clear", Description: "Start a fresh thread"},
	{Name: "/context", Description: "Show current context window usage"},
	{Name: "/context-doctor", Description: "Audit what a session injects and its estimated token cost"},
	{Name: "/copy", Description: "Copy the latest assistant message to clipboard"},
	{Name: "/cost", Description: "Show estimated thread cost"},
	{Name: "/docs", Description: "Open the docs"},
	{Name: "/editor", Description: "Open prompt in an external editor ($EDITOR)"},
	{Name: "/effort", Description: "Set reasoning effort for the current model"},
	{Name: "/extensions", Description: "List loaded Python extensions and their provenance"},
	{Name: "/feedback", Description: "Send feedback or report an issue"},
	{Name: "/force-clear", Description: "Recover a stuck session with a fresh thread"},
	{Name: "/goal", Description: "Set and manage a persistent objective with acceptance criteria"},
	{Name: "/help", Description: "Show help and available commands"},
	{Name: "/install", Description: "Install an optional integration"},
	{Name: "/line-numbers", Description: "Show or hide line numbers in file diffs"},
	{Name: "/manual", Description: "Switch to Manual approval mode"},
	{Name: "/mcp", Description: "Manage MCP servers and authentication"},
	{Name: "/model", Description: "Switch models or edit model settings"},
	{Name: "/notifications", Description: "Review notifications and configure warning settings"},
	{Name: "/offload", Aliases: []string{"/compact"}, Description: "Summarize and offload older messages to free context"},
	{Name: "/plugins", Description: "Manage plugins"},
	{Name: "/prompts", Description: "Search and reuse a previous prompt"},
	{Name: "/quit", Aliases: []string{"/q"}, Description: "Exit app"},
	{Name: "/reload", Description: "Reload environment and config"},
	{Name: "/remember", Description: "Save useful context to memory or skills"},
	{Name: "/restart", Description: "Restart the agent server"},
	{Name: "/rubric", Aliases: []string{"/criteria"}, Description: "Set explicit acceptance criteria for rubric grading"},
	{Name: "/scrollbar", Description: "Show or hide the chat scrollbar"},
	{Name: "/skill-creator", Description: "Create or refine agent skills"},
	{Name: "/summarization-model", Description: "Set the model used for context-compaction summaries"},
	{Name: "/theme", Description: "Change color theme"},
	{Name: "/threads", Description: "Browse and resume past threads"},
	{Name: "/timestamps", Description: "Show or hide message timestamps"},
	{Name: "/tokens", Description: "Show token usage"},
	{Name: "/tools", Description: "List the tools available to the agent"},
	{Name: "/trace", Description: "Open this thread in LangSmith"},
	{Name: "/uninstall", Description: "Remove an installed optional extra"},
	{Name: "/update", Description: "Check for and install updates"},
	{Name: "/version", Aliases: []string{"/about"}, Description: "Show version information"},
	{Name: "/yolo", Description: "Switch to YOLO approval mode (no review)"},
	{Name: "/debug", Hidden: true},
	{Name: "/debug-error", Hidden: true},
}

// Registry maps command names and aliases to handlers.
type Registry struct {
	handlers map[string]Handler
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry { return &Registry{handlers: map[string]Handler{}} }

// Register binds a handler to a command and its aliases.
func (r *Registry) Register(cmd Command, h Handler) error {
	return notimpl.Err("commands.Registry.Register")
}

// Dispatch parses "/name args" and runs the matching handler.
func (r *Registry) Dispatch(ctx context.Context, input string) error {
	return notimpl.Err("commands.Registry.Dispatch")
}
