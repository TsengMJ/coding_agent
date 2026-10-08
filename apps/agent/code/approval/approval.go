// Package approval tracks the per-thread tool approval mode
// (Python `approval_mode.py`, `auto_mode.py`).
package approval

import (
	"context"

	"github.com/tsengmingchun/quill/apps/agent/internal/notimpl"
	"github.com/tsengmingchun/quill/apps/agent/llm"
	"github.com/tsengmingchun/quill/apps/agent/runtime"
)

// Mode is how tool calls are reviewed.
type Mode string

const (
	// ModeManual asks the user to approve each gated call.
	ModeManual Mode = "manual"
	// ModeAuto lets a classifier model approve safe calls.
	ModeAuto Mode = "auto"
	// ModeYOLO approves everything without review.
	ModeYOLO Mode = "yolo"
)

// Next cycles manual -> auto -> yolo -> manual.
func Next(m Mode) Mode { return m }

// Read loads the thread's mode from the store (default manual).
func Read(ctx context.Context, s runtime.Store, threadID string) (Mode, error) {
	return ModeManual, notimpl.Err("approval.Read")
}

// Write persists the thread's mode.
func Write(ctx context.Context, s runtime.Store, threadID string, m Mode) error {
	return notimpl.Err("approval.Write")
}

// Verdict is the auto-mode classifier's decision for one call.
type Verdict struct {
	Allow  bool
	Reason string
}

// Classifier decides whether a tool call is safe to auto-approve.
type Classifier interface {
	Classify(ctx context.Context, call llm.ToolCall, transcript []llm.Message) (Verdict, error)
}
