// Package client drives the agent server from the terminal, either
// interactively (TUI) or headless (Python `client/*`, `app.py`).
package client

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/tsengmingchun/deepagents_golang/agent"
	"github.com/tsengmingchun/deepagents_golang/code/server"
	"github.com/tsengmingchun/deepagents_golang/internal/notimpl"
	"github.com/tsengmingchun/deepagents_golang/llm"
	"github.com/tsengmingchun/deepagents_golang/runtime"
)

// OutputFormat selects headless output.
type OutputFormat string

const (
	OutputText OutputFormat = "text"
	OutputJSON OutputFormat = "json"
)

// ErrThreadResumeUnsupported is returned when a headless run names a thread.
var ErrThreadResumeUnsupported = errors.New("thread 續跑尚未支援")

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

// RunNonInteractive sends one prompt and prints the final assistant text.
//
// Invalid options are returned as an error. Failures during the run are
// written to Stderr and reported through the exit code only.
func RunNonInteractive(ctx context.Context, srv server.Server, opts NonInteractiveOptions) (int, error) {
	if opts.ThreadID != "" {
		return 1, ErrThreadResumeUnsupported
	}
	if opts.Format != "" && opts.Format != OutputText {
		return 1, notimpl.Err("client.RunNonInteractive(Format " + string(opts.Format) + ")")
	}
	if opts.Prompt == "" {
		return 1, errors.New("client.RunNonInteractive: prompt is required")
	}

	var final *runtime.State
	req := server.RunRequest{Input: agent.Input{Messages: []llm.Message{llm.NewUserMessage(opts.Prompt)}}}
	for ev, err := range srv.Run(ctx, req) {
		if err != nil {
			fmt.Fprintln(opts.Stderr, "error:", err)
			return 1, nil
		}
		if state, ok := ev.Data.(*runtime.State); ok && ev.Mode == runtime.StreamValues {
			final = state
		}
	}
	if final == nil {
		fmt.Fprintln(opts.Stderr, "error: run finished without a final state")
		return 1, nil
	}
	fmt.Fprintln(opts.Stdout, lastAssistantText(final.Messages))
	return 0, nil
}

func lastAssistantText(msgs []llm.Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == llm.RoleAssistant {
			return msgs[i].Text()
		}
	}
	return ""
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
