package client

import (
	"context"
	"errors"
	"fmt"

	"github.com/tsengmingchun/deepagents_golang/code/commands"
)

// Session is the client state slash commands act on. The line REPL runs
// each method inline; the TUI may start work in the background and report
// back through its own event loop.
type Session interface {
	Thread() string
	NewThread()
	ListThreads(ctx context.Context)
	Resume(ctx context.Context, id string)
	Compact(ctx context.Context)
	AutoApprove() bool
	SetAutoApprove(on bool)
	Quit()
	// Info shows a line of command output.
	Info(text string)
}

// NewCommandRegistry binds every commands.DCode entry to s. helpFooter is
// appended to /help output (e.g. TUI key bindings).
func NewCommandRegistry(s Session, helpFooter string) *commands.Registry {
	reg := commands.NewRegistry()
	handlers := map[string]commands.Handler{
		"/new": func(context.Context, string) error {
			s.NewThread()
			return nil
		},
		"/thread": func(context.Context, string) error {
			s.Info("thread " + s.Thread())
			return nil
		},
		"/threads": func(ctx context.Context, _ string) error {
			s.ListThreads(ctx)
			return nil
		},
		"/resume": func(ctx context.Context, id string) error {
			if id == "" {
				return errors.New("usage: /resume <thread id>")
			}
			s.Resume(ctx, id)
			return nil
		},
		"/compact": func(ctx context.Context, _ string) error {
			s.Compact(ctx)
			return nil
		},
		"/yolo": func(context.Context, string) error {
			s.SetAutoApprove(!s.AutoApprove())
			return nil
		},
		"/help": func(context.Context, string) error {
			help := reg.Help()
			if helpFooter != "" {
				help += "\n" + helpFooter
			}
			s.Info(help)
			return nil
		},
		"/quit": func(context.Context, string) error {
			s.Quit()
			return nil
		},
	}
	for _, cmd := range commands.DCode {
		h, ok := handlers[cmd.Name]
		if !ok {
			panic(fmt.Sprintf("client: no handler for %s", cmd.Name))
		}
		if err := reg.Register(cmd, h); err != nil {
			panic(err)
		}
	}
	return reg
}
