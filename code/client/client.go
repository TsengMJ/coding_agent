// Package client drives the agent server from the terminal, either
// interactively (TUI) or headless (Python `client/*`, `app.py`).
package client

import (
	"context"
	"io"

	"github.com/tsengmingchun/deepagents_golang/code/server"
	"github.com/tsengmingchun/deepagents_golang/internal/notimpl"
)

// OutputFormat selects headless output.
type OutputFormat string

const (
	OutputText OutputFormat = "text"
	OutputJSON OutputFormat = "json"
)

// NonInteractiveOptions configures a headless run (`dcode -n "prompt"`).
type NonInteractiveOptions struct {
	Prompt   string
	ThreadID string
	Format   OutputFormat
	// AutoApprove approves tool calls that would otherwise need review.
	AutoApprove bool
	Stdout      io.Writer
	Stderr      io.Writer
}

// RunNonInteractive sends one prompt, streams output and returns the exit code.
func RunNonInteractive(ctx context.Context, srv server.Server, opts NonInteractiveOptions) (int, error) {
	return 1, notimpl.Err("client.RunNonInteractive")
}

// InteractiveOptions configures the TUI session.
type InteractiveOptions struct {
	ThreadID string
	// Resume continues the most recent thread when ThreadID is empty.
	Resume bool
}

// RunInteractive starts the TUI.
func RunInteractive(ctx context.Context, srv server.Server, opts InteractiveOptions) error {
	return notimpl.Err("client.RunInteractive")
}
