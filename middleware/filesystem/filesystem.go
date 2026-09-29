// Package filesystem exposes backend-backed file tools and shell execution
// to the model (Python `FilesystemMiddleware`).
package filesystem

import (
	"context"
	"time"

	"github.com/tsengmingchun/deepagents_golang/agent"
	"github.com/tsengmingchun/deepagents_golang/backend"
	"github.com/tsengmingchun/deepagents_golang/internal/notimpl"
	"github.com/tsengmingchun/deepagents_golang/internal/stubtool"
	"github.com/tsengmingchun/deepagents_golang/middleware/permissions"
	"github.com/tsengmingchun/deepagents_golang/runtime"
	"github.com/tsengmingchun/deepagents_golang/tool"
)

// ToolName is a built-in filesystem tool name (`FsToolName`).
type ToolName string

const (
	ToolLs        ToolName = "ls"
	ToolReadFile  ToolName = "read_file"
	ToolWriteFile ToolName = "write_file"
	ToolEditFile  ToolName = "edit_file"
	ToolDelete    ToolName = "delete"
	ToolGlob      ToolName = "glob"
	ToolGrep      ToolName = "grep"
	ToolExecute   ToolName = "execute"
)

// AllTools lists every built-in tool in display order.
var AllTools = []ToolName{ToolLs, ToolReadFile, ToolWriteFile, ToolEditFile, ToolDelete, ToolGlob, ToolGrep, ToolExecute}

// Options configures the middleware; zero values take the Python defaults.
type Options struct {
	// Backend defaults to a StateBackend.
	Backend                backend.Backend
	SystemPrompt           string
	CustomToolDescriptions map[string]string
	// ToolTokenLimitBeforeEvict offloads larger tool results to the backend (default 20000).
	ToolTokenLimitBeforeEvict int
	// HumanMessageTokenLimitBeforeEvict offloads large user messages (default 50000).
	HumanMessageTokenLimitBeforeEvict int
	// MaxExecuteTimeout bounds the per-call execute timeout (default 1h).
	MaxExecuteTimeout time.Duration
	// GrepMaxCount caps grep matches (default 1000).
	GrepMaxCount int
	// Tools allowlists exposed tools; nil exposes all.
	Tools       []ToolName
	Permissions []permissions.FilesystemPermission
}

// Middleware provides ls/read_file/write_file/edit_file/delete/glob/grep/execute.
type Middleware struct {
	opts Options
}

// New returns a filesystem middleware.
func New(opts Options) *Middleware { return &Middleware{opts: opts} }

func (m *Middleware) Name() string { return "FilesystemMiddleware" }

// Tools returns the enabled filesystem tools.
func (m *Middleware) Tools() []tool.Tool {
	names := m.opts.Tools
	if names == nil {
		names = AllTools
	}
	tools := make([]tool.Tool, 0, len(names))
	for _, n := range names {
		tools = append(tools, stubtool.New(string(n), ""))
	}
	return tools
}

// StateSchema declares the "files" key with its merge reducer.
func (m *Middleware) StateSchema() runtime.Schema {
	return runtime.Schema{backend.FilesStateKey: {Name: backend.FilesStateKey}}
}

// WrapModelCall injects the filesystem prompt, drops execute for non-sandbox
// backends and evicts oversized human messages.
func (m *Middleware) WrapModelCall(ctx context.Context, req *agent.ModelRequest, next agent.ModelHandler) (*agent.ModelResponse, error) {
	return nil, notimpl.Err("filesystem.Middleware.WrapModelCall")
}

// WrapToolCall enforces permissions, rejects parallel mutations of one file
// and evicts oversized tool results to the backend.
func (m *Middleware) WrapToolCall(ctx context.Context, req *agent.ToolCallRequest, next agent.ToolHandler) (*tool.Result, error) {
	return nil, notimpl.Err("filesystem.Middleware.WrapToolCall")
}
