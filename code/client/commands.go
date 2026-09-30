package client

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/tsengmingchun/deepagents_golang/code/commands"
	"github.com/tsengmingchun/deepagents_golang/llm"
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
	// Model is the "provider:model" the next run uses, for display; empty
	// when unknown.
	Model() string
	// SetModel makes later runs use spec.
	SetModel(spec string)
	Quit()
	// Info shows a line of command output.
	Info(text string)
}

// CommandOptions configures NewCommandRegistry.
type CommandOptions struct {
	// HelpFooter is appended to /help output (e.g. TUI key bindings).
	HelpFooter string
	// SaveDefaultModel persists `/model <spec> --default`; nil reports that
	// saving is unavailable.
	SaveDefaultModel func(spec string) error
}

// NewCommandRegistry binds every commands.DCode entry to s.
func NewCommandRegistry(s Session, opts CommandOptions) *commands.Registry {
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
			if opts.HelpFooter != "" {
				help += "\n" + opts.HelpFooter
			}
			s.Info(help)
			return nil
		},
		"/model": func(_ context.Context, args string) error {
			return modelCommand(s, opts.SaveDefaultModel, args)
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

const modelUsage = "usage: /model [provider:model] [--default]"

// modelCommand shows the model, switches it for later runs, and with
// --default saves it as models.default.
func modelCommand(s Session, save func(string) error, args string) error {
	var spec string
	saveDefault := false
	for _, f := range strings.Fields(args) {
		switch {
		case f == "--default":
			saveDefault = true
		case strings.HasPrefix(f, "-") || spec != "":
			return errors.New(modelUsage)
		default:
			spec = f
		}
	}
	if spec == "" && !saveDefault {
		name := s.Model()
		if name == "" {
			name = "(server default)"
		}
		s.Info("model " + name)
		return nil
	}
	if spec != "" {
		if err := llm.ValidateModelSpec(spec); err != nil {
			return err
		}
		s.SetModel(spec)
		s.Info("model " + spec + " (from the next message)")
	} else if spec = s.Model(); spec == "" {
		return errors.New(modelUsage)
	}
	if !saveDefault {
		return nil
	}
	if save == nil {
		return errors.New("saving the default model is not available here")
	}
	if err := save(spec); err != nil {
		return fmt.Errorf("save default model: %w", err)
	}
	s.Info("saved " + spec + " as the default model")
	return nil
}

// ModelChoice tracks which model a client runs with.
type ModelChoice struct {
	// Override is sent as RunRequest.Model; empty lets the server pick the
	// thread's last model or its default.
	Override string
	// Thread is the model the current thread last ran with.
	Thread string
	// Default names the server default, for display.
	Default string
}

// Current is the model the next run will use, as best the client knows;
// empty when the server has not said.
func (c ModelChoice) Current() string {
	switch {
	case c.Override != "":
		return c.Override
	case c.Thread != "":
		return c.Thread
	}
	return c.Default
}
