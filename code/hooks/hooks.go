// Package hooks runs user lifecycle hooks declared in hooks.json
// (Python `hooks/*`, see HOOKS.md).
package hooks

import (
	"context"

	"github.com/tsengmingchun/deepagents_golang/internal/notimpl"
)

// Event is a lifecycle point hooks can attach to.
type Event string

const (
	EventSessionStart       Event = "SessionStart"
	EventUserPromptSubmit   Event = "UserPromptSubmit"
	EventSessionEnd         Event = "SessionEnd"
	EventPermissionRequest  Event = "PermissionRequest"
	EventNotification       Event = "Notification"
	EventPreToolUse         Event = "PreToolUse"
	EventPostToolUse        Event = "PostToolUse"
	EventPostToolUseFailure Event = "PostToolUseFailure"
	EventPreCompact         Event = "PreCompact"
	EventStop               Event = "Stop"
	EventSubagentStart      Event = "SubagentStart"
	EventSubagentStop       Event = "SubagentStop"
)

// Hook is one configured command.
type Hook struct {
	Event   Event
	Matcher string
	Command string
	Timeout int
}

// Outcome is a hook's decision; Block stops the triggering action.
type Outcome struct {
	Block   bool
	Reason  string
	Context string
}

// Engine loads hooks and dispatches events.
type Engine interface {
	Fire(ctx context.Context, ev Event, payload map[string]any) ([]Outcome, error)
}

// Load reads hooks.json files and returns an engine.
func Load(paths ...string) (Engine, error) {
	return nil, notimpl.Err("hooks.Load")
}
