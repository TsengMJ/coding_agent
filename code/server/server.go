// Package server runs the agent graph in a separate process and streams run
// events to clients (Python `server_graph.py`, `client/launch/server*.py`).
package server

import (
	"context"
	"fmt"
	"iter"
	"os"

	"github.com/tsengmingchun/deepagents_golang"
	"github.com/tsengmingchun/deepagents_golang/agent"
	"github.com/tsengmingchun/deepagents_golang/code/config"
	"github.com/tsengmingchun/deepagents_golang/internal/notimpl"
	"github.com/tsengmingchun/deepagents_golang/runtime"
)

// DefaultModelSpec is used when neither Config.ModelSpec nor DCODE_MODEL is set.
const DefaultModelSpec = "openai:gpt-5.5"

// ModelEnvVar overrides the default model spec.
const ModelEnvVar = "DCODE_MODEL"

// RunRequest starts or resumes a run on a thread.
type RunRequest struct {
	ThreadID  string
	AgentName string
	Input     agent.Input
	Modes     []runtime.StreamMode
}

// Server is the agent-server side of the client/server boundary.
type Server interface {
	Run(ctx context.Context, req RunRequest) iter.Seq2[runtime.Event, error]
	GetState(ctx context.Context, threadID string) (*runtime.State, error)
	Cancel(ctx context.Context, threadID string) error
	Close() error
}

// Config configures a server instance.
type Config struct {
	Settings *config.Settings
	Paths    config.Paths
	// Addr is the listen address; empty runs in-process.
	Addr string
	// ModelSpec ("provider:model") takes precedence over DCODE_MODEL.
	ModelSpec string
}

// New builds the server runtime (model, tools, MCP, sandbox, graph).
func New(ctx context.Context, cfg Config) (Server, error) {
	if cfg.Addr != "" {
		return nil, notimpl.Err("server.New(Addr)")
	}
	spec := resolveModelSpec(cfg)
	a, err := deepagents.CreateDeepAgent(deepagents.WithModelSpec(spec))
	if err != nil {
		return nil, fmt.Errorf("create agent with model %q: %w", spec, err)
	}
	return &inProcess{agent: a}, nil
}

func resolveModelSpec(cfg Config) string {
	if cfg.ModelSpec != "" {
		return cfg.ModelSpec
	}
	if spec := os.Getenv(ModelEnvVar); spec != "" {
		return spec
	}
	return DefaultModelSpec
}

// inProcess runs the agent in the calling process.
type inProcess struct {
	agent agent.Agent
}

// Run invokes the agent to completion and yields the final state as a single
// StreamValues event.
func (s *inProcess) Run(ctx context.Context, req RunRequest) iter.Seq2[runtime.Event, error] {
	return func(yield func(runtime.Event, error) bool) {
		if req.ThreadID != "" {
			yield(runtime.Event{}, notimpl.Err("server.Run(ThreadID)"))
			return
		}
		state, err := s.agent.Invoke(ctx, req.Input, runtime.RunConfig{})
		if err != nil {
			yield(runtime.Event{}, err)
			return
		}
		yield(runtime.Event{Mode: runtime.StreamValues, Data: state}, nil)
	}
}

func (s *inProcess) GetState(ctx context.Context, threadID string) (*runtime.State, error) {
	return nil, notimpl.Err("server.GetState")
}

func (s *inProcess) Cancel(ctx context.Context, threadID string) error {
	return notimpl.Err("server.Cancel")
}

func (s *inProcess) Close() error { return nil }

// Manager starts, restarts and stops a server subprocess for the client.
type Manager interface {
	Start(ctx context.Context) (Server, error)
	Restart(ctx context.Context) (Server, error)
	Stop(ctx context.Context) error
}
