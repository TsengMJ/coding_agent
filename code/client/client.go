// Package client drives the agent server from the terminal, either
// interactively or headless (Python `client/*`, `app.py`).
package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/tsengmingchun/deepagents_golang/code/server"
	"github.com/tsengmingchun/deepagents_golang/code/sessions"
	"github.com/tsengmingchun/deepagents_golang/internal/notimpl"
	"github.com/tsengmingchun/deepagents_golang/llm"
)

// OutputFormat selects headless output.
type OutputFormat string

const (
	OutputText OutputFormat = "text"
	OutputJSON OutputFormat = "json"
)

// NonInteractiveOptions configures a headless run (`dcode -n "prompt"`).
type NonInteractiveOptions struct {
	Prompt string
	// ThreadID continues a thread; empty starts a new one.
	ThreadID string
	// Cwd is the absolute project directory the agent works in.
	Cwd    string
	Format OutputFormat
	// AutoApprove approves tool calls that would otherwise need review.
	AutoApprove bool
	// Approver decides reviewed tool calls; nil rejects them with NoReviewerReason.
	Approver Approver
	// Quiet keeps Stderr free of the thread ID and tool activity, so only the
	// reply (on Stdout) and errors remain.
	Quiet bool
	// NoStream prints each reply once it is complete instead of token by token.
	NoStream bool
	// Timeout cancels the run and exits with ExitTimeout; zero means none.
	Timeout time.Duration
	Stdout  io.Writer
	Stderr  io.Writer
}

// Exit codes for a headless run that did not finish (as GNU timeout and shells).
const (
	ExitTimeout     = 124
	ExitInterrupted = 130
)

// RunNonInteractive sends one prompt and prints the run as it happens.
//
// Invalid options are returned as an error. Failures during the run are
// written to Stderr and reported through the exit code only.
func RunNonInteractive(ctx context.Context, srv server.Server, opts NonInteractiveOptions) (int, error) {
	if opts.Format != "" && opts.Format != OutputText {
		return 1, notimpl.Err("client.RunNonInteractive(Format " + string(opts.Format) + ")")
	}
	if opts.Prompt == "" {
		return 1, errors.New("client.RunNonInteractive: prompt is required")
	}
	if opts.Cwd == "" {
		return 1, errors.New("client.RunNonInteractive: Cwd is required")
	}
	activity := opts.Stderr
	if opts.Quiet {
		activity = io.Discard
	}
	thread := opts.ThreadID
	if thread == "" {
		thread = sessions.NewThreadID()
		fmt.Fprintln(activity, "thread:", thread)
	}
	approver := opts.Approver
	if approver == nil {
		approver = RejectApprover{Reason: NoReviewerReason}
	}
	if opts.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
	}
	p := &printer{out: opts.Stdout, errw: activity, noStream: opts.NoStream}
	req := server.RunRequest{
		ThreadID:    thread,
		Cwd:         opts.Cwd,
		Messages:    []llm.Message{llm.NewUserMessage(opts.Prompt)},
		AutoApprove: opts.AutoApprove,
	}
	err := runTurn(ctx, srv, req, p, approver)
	if p.streaming {
		fmt.Fprintln(opts.Stdout)
	}
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		fmt.Fprintf(opts.Stderr, "error: timed out after %s (thread %s)\n", opts.Timeout, thread)
		return ExitTimeout, nil
	case ctx.Err() != nil:
		fmt.Fprintf(opts.Stderr, "interrupted (thread %s)\n", thread)
		return ExitInterrupted, nil
	case err != nil:
		fmt.Fprintln(opts.Stderr, "error:", err)
		return 1, nil
	}
	return 0, nil
}

// runTurn runs req and keeps resuming it with the approver's decisions until
// the run finishes without pausing.
func runTurn(ctx context.Context, srv server.Server, req server.RunRequest, p *printer, approver Approver) error {
	for {
		var paused []server.Interrupt
		for ev, err := range srv.Run(ctx, req) {
			if err != nil {
				return err
			}
			if ev.Kind == server.EventInterrupt {
				paused = ev.Interrupts
				continue
			}
			p.handle(ev)
		}
		if len(paused) == 0 {
			return nil
		}
		decisions, err := approver.Decide(ctx, paused[0].ActionRequests)
		if err != nil {
			return err
		}
		req = server.RunRequest{
			ThreadID:    req.ThreadID,
			Cwd:         req.Cwd,
			Resume:      &server.Resume{Decisions: decisions},
			AutoApprove: req.AutoApprove,
			Interactive: req.Interactive,
		}
	}
}
