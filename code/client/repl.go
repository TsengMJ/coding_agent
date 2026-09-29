package client

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/tsengmingchun/deepagents_golang/code/server"
	"github.com/tsengmingchun/deepagents_golang/code/sessions"
	"github.com/tsengmingchun/deepagents_golang/llm"
)

// SlashHelp lists the slash commands shared by the line REPL and the TUI.
const SlashHelp = `commands:
  /new           start a new thread
  /thread        show the current thread id
  /threads       list threads in this directory
  /resume <id>   switch to another thread
  /compact       summarize older messages to free context
  /yolo          toggle auto-approval of tool calls
  /quit          exit`

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
}

// RunInteractive runs a line-oriented REPL: one prompt per line, run output
// printed as it streams, approvals asked inline. It returns on /quit or EOF.
func RunInteractive(ctx context.Context, srv server.Server, opts InteractiveOptions) error {
	if opts.Cwd == "" {
		return errors.New("client.RunInteractive: Cwd is required")
	}
	r := &repl{srv: srv, opts: opts, in: bufio.NewReader(opts.In), thread: opts.ThreadID, auto: opts.AutoApprove}
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
	name, arg, _ := strings.Cut(line, " ")
	arg = strings.TrimSpace(arg)
	out := r.opts.Out
	switch name {
	case "/quit", "/exit":
		return true
	case "/help":
		fmt.Fprintln(out, SlashHelp)
	case "/new":
		r.thread = sessions.NewThreadID()
		fmt.Fprintf(out, "thread %s (new)\n", r.thread)
	case "/thread":
		fmt.Fprintln(out, "thread", r.thread)
	case "/threads":
		list, err := r.srv.ListThreads(ctx, sessions.Filter{Cwd: r.opts.Cwd, Limit: 20})
		if err != nil {
			fmt.Fprintln(r.opts.Err, "error:", err)
			break
		}
		PrintThreads(out, list, false)
	case "/compact", "/offload":
		res, err := r.srv.Compact(ctx, r.thread)
		if err != nil {
			fmt.Fprintln(r.opts.Err, "error:", err)
			break
		}
		fmt.Fprintln(out, CompactSummary(res))
	case "/resume":
		if arg == "" {
			fmt.Fprintln(r.opts.Err, "usage: /resume <thread id>")
			break
		}
		if err := r.switchTo(ctx, arg); err != nil {
			fmt.Fprintln(r.opts.Err, "error:", err)
		}
	case "/yolo":
		r.auto = !r.auto
		fmt.Fprintf(out, "auto-approve %s\n", onOff(r.auto))
	default:
		fmt.Fprintf(r.opts.Err, "unknown command %s (try /help)\n", name)
	}
	return false
}

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
