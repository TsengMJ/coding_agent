// Package filesystem exposes backend-backed file tools and shell execution
// to the model (Python `FilesystemMiddleware`).
package filesystem

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/tsengmingchun/quill/apps/agent/agent"
	"github.com/tsengmingchun/quill/apps/agent/backend"
	"github.com/tsengmingchun/quill/apps/agent/llm"
	"github.com/tsengmingchun/quill/apps/agent/middleware/permissions"
	"github.com/tsengmingchun/quill/apps/agent/runtime"
	"github.com/tsengmingchun/quill/apps/agent/tool"
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

// DefaultTools is exposed when Options.Tools is nil.
var DefaultTools = AllTools

// Defaults applied by New.
const (
	DefaultMaxExecuteTimeout = time.Hour
	DefaultGrepMaxCount      = 1000
)

// Options configures the middleware; zero values take the Python defaults.
type Options struct {
	// Backend is required; use backend.NewStateBackend() to keep files in state.
	Backend backend.Backend
	// SystemPrompt is appended to the system message on every model call.
	SystemPrompt           string
	CustomToolDescriptions map[string]string
	// ToolTokenLimitBeforeEvict offloads larger tool results to
	// {artifactsRoot}/large_tool_results (default 20000; negative disables,
	// leaving plain truncation).
	ToolTokenLimitBeforeEvict int
	// HumanMessageTokenLimitBeforeEvict offloads a larger last user message to
	// {artifactsRoot}/conversation_history (default 50000; negative disables).
	HumanMessageTokenLimitBeforeEvict int
	// MaxExecuteTimeout bounds the per-call execute timeout (default 1h).
	MaxExecuteTimeout time.Duration
	// GrepMaxCount caps grep matches (default 1000).
	GrepMaxCount int
	// Tools allowlists exposed tools; nil exposes DefaultTools. `execute` and
	// `delete` are dropped when the backend lacks the capability.
	Tools []ToolName
	// Permissions deny paths per operation and filter listings. Interrupt
	// rules only take effect through permissions.BuildInterruptOn + HITL.
	// Not allowed with execution-capable backends.
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
		if backend.SupportsExecution(opts.Backend) {
			return nil, errors.New("filesystem.New: permissions are not supported with backends that provide command execution; shell commands would bypass the path rules")
		}
		if err := permissions.ValidateAll(opts.Permissions); err != nil {
			return nil, fmt.Errorf("filesystem.New: %w", err)
		}
	}
	if opts.MaxExecuteTimeout <= 0 {
		opts.MaxExecuteTimeout = DefaultMaxExecuteTimeout
	}
	if opts.GrepMaxCount <= 0 {
		opts.GrepMaxCount = DefaultGrepMaxCount
	}
	if opts.ToolTokenLimitBeforeEvict == 0 {
		opts.ToolTokenLimitBeforeEvict = DefaultToolTokenLimitBeforeEvict
	}
	if opts.HumanMessageTokenLimitBeforeEvict == 0 {
		opts.HumanMessageTokenLimitBeforeEvict = DefaultHumanMessageTokenLimitBeforeEvict
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
	return runtime.Schema{backend.FilesStateKey: {Name: backend.FilesStateKey, Reducer: backend.FilesReducer}}
}

// WrapModelCall appends Options.SystemPrompt to the system message and
// shows oversized user messages to the model as offloaded previews.
func (m *Middleware) WrapModelCall(ctx context.Context, req *agent.ModelRequest, next agent.ModelHandler) (*agent.ModelResponse, error) {
	updated := *req
	if m.opts.SystemPrompt != "" {
		updated.SystemMessage = agent.AppendToSystemMessage(req.SystemMessage, m.opts.SystemPrompt)
	}
	limit := m.opts.HumanMessageTokenLimitBeforeEvict
	if active := llm.ModelForContext(ctx, req.Model); active != nil && active.Profile().MaxInputTokens > 0 {
		limit = min(limit, max(active.Profile().MaxInputTokens/4, 1))
	} else {
		limit = min(limit, 4000)
	}
	msgs, upd, evicted := m.evictHumanMessages(ctx, req, limit)
	if evicted {
		updated.Messages = msgs
	}
	resp, err := next(ctx, &updated)
	if err != nil || resp == nil || upd == nil {
		return resp, err
	}
	out := *resp
	if out.Update == nil {
		out.Update = upd
	} else {
		merged := *out.Update
		merged.Messages = append(slices.Clone(upd.Messages), merged.Messages...)
		if files, ok := upd.Values[backend.FilesStateKey].(map[string]*backend.FileData); ok {
			merged = *mergeFilesUpdate(&merged, files)
		}
		out.Update = &merged
	}
	return &out, nil
}

// WrapToolCall rejects a second mutation of the same file in one model turn
// and offloads oversized results (see Options.ToolTokenLimitBeforeEvict).
func (m *Middleware) WrapToolCall(ctx context.Context, req *agent.ToolCallRequest, next agent.ToolHandler) (*tool.Result, error) {
	if parallelFileMutation(req) {
		return &tool.Result{
			Content: []llm.ContentBlock{llm.TextBlock("Error: parallel file mutations to the same path are not allowed.")},
			IsError: true,
		}, nil
	}
	res, err := next(ctx, req)
	if err != nil || res == nil {
		return res, err
	}
	m.evictToolResult(ctx, req, res)
	return res, nil
}

var fileMutationTools = []string{string(ToolWriteFile), string(ToolEditFile), string(ToolDelete)}

// parallelFileMutation reports whether an earlier call in the same assistant
// message already mutates req's file (`_parallel_file_mutation_error`). Tool
// calls run concurrently, so only the first mutation of a path may run.
func parallelFileMutation(req *agent.ToolCallRequest) bool {
	target, ok := mutationPath(req.ToolCall)
	if !ok || req.State == nil {
		return false
	}
	for i := len(req.State.Messages) - 1; i >= 0; i-- {
		msg := req.State.Messages[i]
		if msg.Role != llm.RoleAssistant {
			continue
		}
		for _, call := range msg.ToolCalls {
			if call.ID == req.ToolCall.ID {
				return false
			}
			if p, ok := mutationPath(call); ok && p == target {
				return true
			}
		}
		return false
	}
	return false
}

func mutationPath(call llm.ToolCall) (string, bool) {
	if !slices.Contains(fileMutationTools, call.Name) {
		return "", false
	}
	raw, ok := call.Args["file_path"].(string)
	if !ok {
		return "", false
	}
	p, err := backend.ValidatePath(raw, nil)
	if err != nil {
		return "", false
	}
	return p, true
}
