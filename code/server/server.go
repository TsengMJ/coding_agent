// Package server runs the agent graph in a separate process and streams run
// events to clients (Python `server_graph.py`, `client/launch/server*.py`).
package server

import (
	"context"
	"iter"

	"github.com/tsengmingchun/deepagents_golang/agent"
	"github.com/tsengmingchun/deepagents_golang/code/config"
	"github.com/tsengmingchun/deepagents_golang/internal/notimpl"
	"github.com/tsengmingchun/deepagents_golang/runtime"
)

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
}

// New builds the server runtime (model, tools, MCP, sandbox, graph).
func New(ctx context.Context, cfg Config) (Server, error) {
	return nil, notimpl.Err("server.New")
}

// Manager starts, restarts and stops a server subprocess for the client.
type Manager interface {
	Start(ctx context.Context) (Server, error)
	Restart(ctx context.Context) (Server, error)
	Stop(ctx context.Context) error
}
