package client

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/tsengmingchun/deepagents_golang/code/commands"
	"github.com/tsengmingchun/deepagents_golang/code/server"
	"github.com/tsengmingchun/deepagents_golang/code/sessions"
	"github.com/tsengmingchun/deepagents_golang/llm"
)

// InteractiveOptions configures the line REPL.
type InteractiveOptions struct {
	// ThreadID continues a thread; empty starts a new one.
	ThreadID string
	Cwd      string
	// AutoApprove starts with auto-approval on (toggle with /yolo).
	AutoApprove bool
	In          io.Reader
	Out         io.Writer
	Err         io.Writer
	// Interrupt delivers Ctrl-C; each signal cancels the run in progress.
	Interrupt <-chan os.Signal
}

type repl struct {
	srv    server.Server
	opts   InteractiveOptions
	in     *bufio.Reader
	thread string
	auto   bool
	cmds   *commands.Registry
	quit   bool
}

// RunInteractive runs a line-oriented REPL: one prompt per line, run output
// printed as it streams, approvals asked inline. It returns on /quit or EOF.
func RunInteractive(ctx context.Context, srv server.Server, opts InteractiveOptions) error {
	if opts.Cwd == "" {
		return errors.New("client.RunInteractive: Cwd is required")
	}
	r := &repl{srv: srv, opts: opts, in: bufio.NewReader(opts.In), thread: opts.ThreadID, auto: opts.AutoApprove}
	r.cmds = NewCommandRegistry(r, "")
	fmt.Fprintf(opts.Out, "dcode in %s — /help for commands, Ctrl-C cancels a run\n", opts.Cwd)
	if r.thread == "" {
		r.thread = sessions.NewThreadID()
		fmt.Fprintf(opts.Out, "thread %s (new)\n", r.thread)
	} else if err := r.switchTo(ctx, r.thread); err != nil {
		return err
	}
	for {
		fmt.Fprint(opts.Out, "> ")
		line, err := r.in.ReadString('\n')
		if err != nil && line == "" {
			fmt.Fprintln(opts.Out)
			return nil
		}
		line = strings.TrimSpace(line)
		switch {
		case line == "":
		case strings.HasPrefix(line, "/"):
			if r.command(ctx, line) {
				return nil
			}
		default:
			r.turn(ctx, server.RunRequest{Messages: []llm.Message{llm.NewUserMessage(line)}})
		}
	}
}

// turn runs one request (fresh messages or a resume) until it stops pausing.
func (r *repl) turn(ctx context.Context, req server.RunRequest) {
	req.ThreadID, req.Cwd, req.AutoApprove, req.Interactive = r.thread, r.opts.Cwd, r.auto, true
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		select {
		case <-r.opts.Interrupt:
			cancel()
		case <-runCtx.Done():
		}
	}()
	p := &printer{out: r.opts.Out, errw: r.opts.Err}
	err := runTurn(runCtx, r.srv, req, p, PromptApprover{In: r.in, Out: r.opts.Err, Cwd: r.opts.Cwd})
	if p.streaming {
		fmt.Fprintln(r.opts.Out)
	}
	switch {
	case err == nil:
	case runCtx.Err() != nil && ctx.Err() == nil:
		fmt.Fprintln(r.opts.Err, "(cancelled)")
	default:
		fmt.Fprintln(r.opts.Err, "error:", err)
	}
}

// command handles a slash command and reports whether the REPL should exit.
func (r *repl) command(ctx context.Context, line string) bool {
	if err := r.cmds.Dispatch(ctx, line); err != nil {
		fmt.Fprintln(r.opts.Err, err)
	}
	return r.quit
}

func (r *repl) Thread() string { return r.thread }

func (r *repl) NewThread() {
	r.thread = sessions.NewThreadID()
	fmt.Fprintf(r.opts.Out, "thread %s (new)\n", r.thread)
}

func (r *repl) ListThreads(ctx context.Context) {
	list, err := r.srv.ListThreads(ctx, sessions.Filter{Cwd: r.opts.Cwd, Limit: 20})
	if err != nil {
		fmt.Fprintln(r.opts.Err, "error:", err)
		return
	}
	PrintThreads(r.opts.Out, list, false)
}

func (r *repl) Resume(ctx context.Context, id string) {
	if err := r.switchTo(ctx, id); err != nil {
		fmt.Fprintln(r.opts.Err, "error:", err)
	}
}

func (r *repl) Compact(ctx context.Context) {
	res, err := r.srv.Compact(ctx, r.thread)
	if err != nil {
		fmt.Fprintln(r.opts.Err, "error:", err)
		return
	}
	fmt.Fprintln(r.opts.Out, CompactSummary(res))
}

func (r *repl) AutoApprove() bool { return r.auto }

func (r *repl) SetAutoApprove(on bool) {
	r.auto = on
	fmt.Fprintf(r.opts.Out, "auto-approve %s\n", onOff(r.auto))
}

func (r *repl) Quit() { r.quit = true }

func (r *repl) Info(text string) { fmt.Fprintln(r.opts.Out, text) }

// switchTo makes id the current thread and answers any pending approval.
func (r *repl) switchTo(ctx context.Context, id string) error {
	ts, err := r.srv.GetThread(ctx, id)
	if err != nil {
		return err
	}
	if ts == nil {
		return fmt.Errorf("thread %s not found", id)
	}
	r.thread = id
	fmt.Fprintf(r.opts.Out, "thread %s (%d messages)\n", id, len(ts.Messages))
	if len(ts.Interrupts) == 0 {
		return nil
	}
	fmt.Fprintln(r.opts.Out, "this thread is waiting for approval:")
	decisions, err := PromptApprover{In: r.in, Out: r.opts.Err, Cwd: r.opts.Cwd}.Decide(ctx, ts.Interrupts[0].ActionRequests)
	if err != nil {
		return err
	}
	r.turn(ctx, server.RunRequest{Resume: &server.Resume{Decisions: decisions}})
	return nil
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// CompactSummary describes a finished /compact for the user.
func CompactSummary(res *server.CompactResult) string {
	if res.FilePath == "" {
		return "compacted older messages into a summary"
	}
	return "compacted older messages into a summary; full history saved to " + res.FilePath
}
