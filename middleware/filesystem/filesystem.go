// Package filesystem exposes backend-backed file tools and shell execution
// to the model (Python `FilesystemMiddleware`).
package filesystem

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/tsengmingchun/deepagents_golang/agent"
	"github.com/tsengmingchun/deepagents_golang/backend"
	"github.com/tsengmingchun/deepagents_golang/llm"
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

// DefaultTools is exposed when Options.Tools is nil. `delete` must be
// requested explicitly.
var DefaultTools = []ToolName{ToolLs, ToolReadFile, ToolWriteFile, ToolEditFile, ToolGlob, ToolGrep, ToolExecute}

// Defaults applied by New.
const (
	DefaultMaxExecuteTimeout = time.Hour
	DefaultGrepMaxCount      = 1000
)

// Options configures the middleware; zero values take the Python defaults.
type Options struct {
	// Backend is required (StateBackend is not implemented yet).
	Backend backend.Backend
	// SystemPrompt is appended to the system message on every model call.
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
	// Tools allowlists exposed tools; nil exposes DefaultTools. `execute` and
	// `delete` are dropped when the backend lacks the capability.
	Tools       []ToolName
	Permissions []permissions.FilesystemPermission
}

// Middleware provides ls/read_file/write_file/edit_file/delete/glob/grep/execute.
type Middleware struct {
	opts  Options
	tools []tool.Tool
}

// New returns a filesystem middleware.
func New(opts Options) (*Middleware, error) {
	if opts.Backend == nil {
		return nil, errors.New("filesystem.New: Backend is required")
	}
	if len(opts.Permissions) > 0 {
		return nil, errors.New("filesystem.New: Permissions are not supported yet")
	}
	if opts.MaxExecuteTimeout <= 0 {
		opts.MaxExecuteTimeout = DefaultMaxExecuteTimeout
	}
	if opts.GrepMaxCount <= 0 {
		opts.GrepMaxCount = DefaultGrepMaxCount
	}
	names := opts.Tools
	if names == nil {
		names = DefaultTools
	}
	m := &Middleware{opts: opts}
	for _, n := range names {
		if !slices.Contains(AllTools, n) {
			return nil, fmt.Errorf("filesystem.New: unknown tool %q", n)
		}
		if n == ToolExecute && !backend.SupportsExecution(opts.Backend) {
			continue
		}
		if n == ToolDelete && !backend.SupportsDelete(opts.Backend) {
			continue
		}
		m.tools = append(m.tools, m.buildTool(n))
	}
	return m, nil
}

func (m *Middleware) Name() string { return "FilesystemMiddleware" }

// Tools returns the enabled filesystem tools.
func (m *Middleware) Tools() []tool.Tool { return m.tools }

// StateSchema declares the "files" key with its merge reducer.
func (m *Middleware) StateSchema() runtime.Schema {
	return runtime.Schema{backend.FilesStateKey: {Name: backend.FilesStateKey}}
}

// WrapModelCall appends Options.SystemPrompt to the system message.
func (m *Middleware) WrapModelCall(ctx context.Context, req *agent.ModelRequest, next agent.ModelHandler) (*agent.ModelResponse, error) {
	if m.opts.SystemPrompt == "" {
		return next(ctx, req)
	}
	updated := *req
	updated.SystemMessage = appendToSystemMessage(req.SystemMessage, m.opts.SystemPrompt)
	return next(ctx, &updated)
}

// appendToSystemMessage returns a copy of msg with text appended as a new
// block, keeping existing blocks (and their cache markers) intact.
func appendToSystemMessage(msg *llm.Message, text string) *llm.Message {
	if msg == nil {
		out := llm.NewSystemMessage(text)
		return &out
	}
	out := *msg
	if len(out.Content) > 0 {
		text = "\n\n" + text
	}
	out.Content = append(slices.Clone(msg.Content), llm.TextBlock(text))
	return &out
}

// WrapToolCall clips oversized text results to ToolResultTokenLimit.
func (m *Middleware) WrapToolCall(ctx context.Context, req *agent.ToolCallRequest, next agent.ToolHandler) (*tool.Result, error) {
	res, err := next(ctx, req)
	if err != nil || res == nil {
		return res, err
	}
	for i, block := range res.Content {
		if block.Type == llm.BlockText {
			res.Content[i].Text = backend.TruncateIfTooLong(block.Text)
		}
	}
	return res, nil
}
