package agent

import (
	"context"

	core "github.com/tsengmingchun/quill/agent"
	"github.com/tsengmingchun/quill/backend/ossandbox"
	"github.com/tsengmingchun/quill/code/codetools"
	"github.com/tsengmingchun/quill/code/config"
	"github.com/tsengmingchun/quill/llm"
	"github.com/tsengmingchun/quill/middleware/permissions"
	"github.com/tsengmingchun/quill/tool"
)

// NewShellSandbox returns the sandbox and policy shell commands in cwd run
// under; the sandbox is nil when s leaves it off. When the platform
// sandbox cannot run, sandbox.on_unavailable decides between an error and
// unconfined commands that warn in every result.
func NewShellSandbox(s *config.Settings, cwd string) (ossandbox.Sandbox, ossandbox.Policy, error) {
	if s == nil || !s.Sandbox.Enabled() {
		return nil, ossandbox.Policy{}, nil
	}
	cfg := s.Sandbox
	policy := ossandbox.DefaultPolicy(cfg.Mode, cwd)
	policy.Network = cfg.Network
	if cfg.Mode == ossandbox.WorkspaceWrite {
		policy.WritableRoots = append(policy.WritableRoots, cfg.WritableRoots...)
	}
	policy.DenyRead = append(policy.DenyRead, cfg.DenyRead...)
	sb, err := ossandbox.Native()
	if err != nil {
		if cfg.OnUnavailable == config.SandboxUnavailableWarn {
			return ossandbox.Unconfined(err.Error()), policy, nil
		}
		return nil, policy, err
	}
	return sb, policy, nil
}

// readOnlyGuard refuses apply_patch in read-only mode; it writes through
// the backend directly, so the file permission rules do not cover it.
type readOnlyGuard struct{}

func (readOnlyGuard) Name() string { return "SandboxReadOnlyGuard" }

func (readOnlyGuard) WrapToolCall(ctx context.Context, req *core.ToolCallRequest, next core.ToolHandler) (*tool.Result, error) {
	if req.ToolCall.Name != codetools.ApplyPatchName {
		return next(ctx, req)
	}
	return &tool.Result{IsError: true, Content: []llm.ContentBlock{llm.TextBlock(
		"Not run: the sandbox is in read-only mode (sandbox.mode = \"read-only\"), so files cannot be changed. " +
			"Describe the change instead, or ask the user to switch to workspace-write.")}}, nil
}

// sandboxPermissions keeps file tools in step with the shell sandbox:
// read-only mode denies every write, and the files the sandbox protects
// inside the project stay read-only for write_file and edit_file too.
func sandboxPermissions(s *config.Settings) []permissions.FilesystemPermission {
	if s == nil || !s.Sandbox.Enabled() {
		return nil
	}
	if s.Sandbox.Mode == ossandbox.ReadOnly {
		return []permissions.FilesystemPermission{{Operations: []permissions.Operation{permissions.OpWrite}, Paths: []string{"/**"}, Mode: permissions.ModeDeny}}
	}
	return []permissions.FilesystemPermission{{
		Operations: []permissions.Operation{permissions.OpWrite},
		Paths:      []string{"/.git/hooks", "/.git/hooks/**", "/.git/config"},
		Mode:       permissions.ModeDeny,
	}}
}
