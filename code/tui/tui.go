// Package tui is the placeholder for the interactive terminal UI
// (Python Textual app: `app.py`, `tui/*`). The UI library is not chosen yet.
package tui

import (
	"context"

	"github.com/tsengmingchun/deepagents_golang/internal/notimpl"
)

// App is the interactive terminal application.
type App interface {
	Run(ctx context.Context) error
}

// New builds the TUI application.
func New() (App, error) {
	return nil, notimpl.Err("tui.New")
}
